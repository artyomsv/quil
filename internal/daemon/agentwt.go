package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/atomicfile"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/hookevents"
)

// Worktrees and branches that appear while agents run.
//
// The agents' hooks only say WHERE a worktree-related command ran (a
// WorktreeProbe). The record is built by comparing that repository's `git
// worktree list` and local branches against a baseline the daemon saved the
// last time it looked — so a command the hook missed, a script, or an alias is
// still caught at the next compare, and the agent's own end of turn triggers
// one too. The compare is the truth; the hook is the trigger.
//
// An entry says a worktree or branch APPEARED while a pane ran, not that the
// pane's agent made it: a user can make one by hand in another terminal at the
// same moment. The cleanup view words it that way, and nothing here may claim
// more.

// AgentWTEntry is one worktree or branch that appeared since the baseline.
type AgentWTEntry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // "worktree" or "branch"
	Repo      string    `json:"repo"` // the main checkout, as git printed it
	Path      string    `json:"path,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	Gone      bool      `json:"gone,omitempty"`
	GoneAt    time.Time `json:"gone_at,omitempty"`
	PaneID    string    `json:"pane_id,omitempty"`
	PaneName  string    `json:"pane_name,omitempty"`
	Source    string    `json:"source,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Trigger   string    `json:"trigger,omitempty"`
	// No command text is kept: a shell command can carry a credential
	// (an inline token, an authenticated URL), and the compare needs none.
}

// Entry kinds.
const (
	agentWTKindWorktree = "worktree"
	agentWTKindBranch   = "branch"
)

// Bounds. A daemon runs for weeks and agents can loop.
const (
	maxAgentWTRepos   = 64
	maxAgentWTEntries = 500
	// maxAgentWTSubagents bounds one pane's tracked subagents between two
	// idle settles; a subagent past it keeps no directory.
	maxAgentWTSubagents = 64
	// agentWTShortLived drops an entry that is gone again this soon after it
	// appeared: Claude's own subagent worktrees come and go within a turn.
	agentWTShortLived = 60 * time.Second
	agentWTTimeout    = 10 * time.Second
	agentWTSaveDelay  = 2 * time.Second
)

// agentWTRepo is one repository's baseline.
type agentWTRepo struct {
	Worktrees     []string  `json:"worktrees"`
	Branches      []string  `json:"branches,omitempty"`
	BranchesKnown bool      `json:"branches_known,omitempty"`
	Seen          time.Time `json:"seen"`
	// PendingKept are branches of recorded worktrees that went away, waiting
	// for a COMPLETE branch listing to tell whether they survived (and then
	// get an entry of their own). Kept here, not on the worktree entry: a
	// short-lived entry is dropped the moment its worktree goes.
	PendingKept []agentWTKept `json:"pending_kept,omitempty"`
}

// agentWTKept is a surviving-branch check waiting on a branch listing, with
// the gone worktree's attribution.
type agentWTKept struct {
	Branch    string    `json:"branch"`
	PaneID    string    `json:"pane_id,omitempty"`
	PaneName  string    `json:"pane_name,omitempty"`
	Source    string    `json:"source,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Trigger   string    `json:"trigger,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
}

type agentWTFile struct {
	V       int                     `json:"v"`
	Repos   map[string]*agentWTRepo `json:"repos"`
	Entries []AgentWTEntry          `json:"entries"`
}

// agentWTAttr is who a compare attributes new entries to.
type agentWTAttr struct {
	PaneID, PaneName, Source, SessionID, Trigger string
}

// agentWTPane is what the store needs to know about a pane.
type agentWTPane struct {
	Name, CWD string
	Sandbox   bool
}

type agentWTStore struct {
	mu      sync.Mutex
	path    string // "" = not persisted
	repos   map[string]*agentWTRepo
	entries []AgentWTEntry
	// touched is, per pane, the directories its probes named since its last
	// turn end; lastAttr carries the pane's source and session to that end.
	touched  map[string]map[string]bool
	lastAttr map[string]agentWTAttr
	// own are paths and branches this daemon created itself: they join the
	// baseline and never become entries. ownBranches is keyed by
	// ownBranchKey(repo, branch): a same-named branch in another repository is
	// somebody else's.
	ownPaths    map[string]bool
	ownBranches map[string]bool
	// agentDirs is where each pane's AGENT last worked, from its probes. The
	// pane's own CWD does not move when Claude enters a worktree, so this is
	// what tells a removal that the worktree is in use.
	agentDirs map[string]string
	// subDirs is where each SUBAGENT of a pane last worked (pane → agent id →
	// dir). Several can work in different worktrees at once, so one value per
	// pane would forget all but the last. Kept until the pane settles idle:
	// only then has every subagent finished. Each carries when it was
	// recorded, so an idle settle clears only what came before it.
	subDirs map[string]map[string]agentDir
	// subOverflow marks a pane that had more subagents than
	// maxAgentWTSubagents since its last idle settle (with when): some
	// subagent's directory is unknown, so the pane counts as in use for
	// EVERY path until the settle clears it.
	subOverflow map[string]time.Time
	// seeded is the directory each pane's baseline was last seeded for.
	seeded map[string]string
	// One compare per directory at a time; a probe that arrives meanwhile
	// runs it once more.
	running map[string]bool
	pending map[string]agentWTAttr
	saveT   *time.Timer
	// saveMu orders whole saves (snapshot + write), so an older snapshot can
	// never be written after a newer one.
	saveMu sync.Mutex

	now       func() time.Time
	listFn    func(ctx context.Context, dir string) ([]gitworktree.Worktree, error)
	branchFn  func(ctx context.Context, dir string) ([]gitworktree.Branch, bool, error)
	defaultFn func(ctx context.Context, repo string) string
	paneFn    func(id string) (agentWTPane, bool)
	goFn      func(func())
}

func newAgentWTStore(path string, paneFn func(string) (agentWTPane, bool)) *agentWTStore {
	return &agentWTStore{
		path:        path,
		repos:       map[string]*agentWTRepo{},
		touched:     map[string]map[string]bool{},
		lastAttr:    map[string]agentWTAttr{},
		ownPaths:    map[string]bool{},
		ownBranches: map[string]bool{},
		agentDirs:   map[string]string{},
		subDirs:     map[string]map[string]agentDir{},
		subOverflow: map[string]time.Time{},
		seeded:      map[string]string{},
		running:     map[string]bool{},
		pending:     map[string]agentWTAttr{},
		now:         time.Now,
		listFn:      func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) { return worktreeListFn(ctx, dir) },
		branchFn:    gitworktree.Branches,
		defaultFn:   gitworktree.DefaultBranchRef,
		paneFn:      paneFn,
		goFn:        func(f func()) { go f() },
	}
}

// paneForAgentWT is the daemon's paneFn.
func (d *Daemon) paneForAgentWT(id string) (agentWTPane, bool) {
	p := d.session.Pane(id)
	if p == nil {
		return agentWTPane{}, false
	}
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return agentWTPane{Name: p.Name, CWD: p.CWD, Sandbox: p.SandboxImage != "" || p.SandboxBackend != ""}, true
}

// probe answers one WorktreeProbe: it remembers the directory for the pane's
// turn end and compares it now.
func (s *agentWTStore) probe(p hookevents.Payload) {
	if s == nil || p.CWD == "" || len(p.CWD) > hookevents.MaxProbeDirBytes {
		return
	}
	pane, ok := s.paneFn(p.PaneID)
	// A sandbox pane's paths are inside its container; asking the host's
	// git about them describes nothing the agent did.
	if !ok || pane.Sandbox {
		return
	}
	attr := agentWTAttr{
		PaneID: p.PaneID, PaneName: pane.Name, Source: p.Source, SessionID: p.SessionID,
		Trigger: p.Data[hookevents.DataProbeTrigger],
	}
	s.mu.Lock()
	// The agent is (or, after ExitWorktree, is back) in this directory.
	if agent := p.Data[hookevents.DataProbeAgent]; agent == "" {
		s.agentDirs[p.PaneID] = p.CWD
	} else {
		if s.subDirs[p.PaneID] == nil {
			s.subDirs[p.PaneID] = map[string]agentDir{}
		}
		if _, known := s.subDirs[p.PaneID][agent]; known || len(s.subDirs[p.PaneID]) < maxAgentWTSubagents {
			s.subDirs[p.PaneID][agent] = agentDir{Dir: p.CWD, At: s.now()}
		} else {
			// Fail closed: this subagent's directory is not kept, so no
			// path of this pane may be treated as free.
			s.subOverflow[p.PaneID] = s.now()
		}
	}
	if s.touched[p.PaneID] == nil {
		s.touched[p.PaneID] = map[string]bool{}
	}
	if len(s.touched[p.PaneID]) < maxAgentWTRepos {
		s.touched[p.PaneID][p.CWD] = true
	}
	s.lastAttr[p.PaneID] = agentWTAttr{PaneID: p.PaneID, PaneName: pane.Name, Source: p.Source, SessionID: p.SessionID}
	s.mu.Unlock()
	s.schedule(p.CWD, attr)
}

// turnEnded compares every directory the pane probed since its last turn
// end, plus the pane's own directory: the backstop for a command no probe
// matched.
func (s *agentWTStore) turnEnded(paneID string) {
	if s == nil {
		return
	}
	pane, ok := s.paneFn(paneID)
	if !ok || pane.Sandbox {
		return
	}
	s.mu.Lock()
	dirs := s.touched[paneID]
	delete(s.touched, paneID)
	attr := s.lastAttr[paneID]
	s.mu.Unlock()
	attr.PaneID, attr.PaneName, attr.Trigger = paneID, pane.Name, "turn-end"
	if pane.CWD != "" {
		if dirs == nil {
			dirs = map[string]bool{}
		}
		dirs[pane.CWD] = true
	}
	for dir := range dirs {
		s.schedule(dir, attr)
	}
}

// forgetPane drops a destroyed pane's turn state.
func (s *agentWTStore) forgetPane(paneID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.touched, paneID)
	delete(s.lastAttr, paneID)
	delete(s.agentDirs, paneID)
	delete(s.subDirs, paneID)
	delete(s.subOverflow, paneID)
	delete(s.seeded, paneID)
	s.mu.Unlock()
}

// agentDir is one subagent's last directory and when it was recorded.
type agentDir struct {
	Dir string
	At  time.Time
}

// subagentsDone drops a pane's subagent directories recorded at or before
// idleAt: called when the pane settles idle, the one moment every subagent
// is known to have finished. idleAt is taken while the idle state is
// confirmed, so a probe from a turn that started after it — between the
// settle's check and this call — is kept. The main agent's directory stays:
// its session continues there.
func (s *agentWTStore) subagentsDone(paneID string, idleAt time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for agent, d := range s.subDirs[paneID] {
		if !d.At.After(idleAt) {
			delete(s.subDirs[paneID], agent)
		}
	}
	if len(s.subDirs[paneID]) == 0 {
		delete(s.subDirs, paneID)
	}
	if at, ok := s.subOverflow[paneID]; ok && !at.After(idleAt) {
		delete(s.subOverflow, paneID)
	}
}

// seed takes the baseline of the pane's repository before its agent acts:
// called on a turn's START edge. A worktree made in a repository with no
// baseline yet would otherwise be read as pre-existing by the first compare
// and never recorded. Once per pane and directory; a later compare of an
// already-seen repository costs one listing.
func (s *agentWTStore) seed(paneID string) {
	if s == nil {
		return
	}
	pane, ok := s.paneFn(paneID)
	if !ok || pane.Sandbox || pane.CWD == "" {
		return
	}
	s.mu.Lock()
	done := s.seeded[paneID] == normalizeWorktreePath(pane.CWD)
	s.mu.Unlock()
	if done {
		return
	}
	// Marked seeded only by a compare that READ the repository
	// (compare → apply), so a failed or refused listing is retried at the
	// next turn start rather than counted as a baseline.
	s.schedule(pane.CWD, agentWTAttr{PaneID: paneID, PaneName: pane.Name, Trigger: agentWTSeedTrigger})
}

// agentWTSeedTrigger marks a compare that takes a baseline.
const agentWTSeedTrigger = "seed"

// seedSoon is seed off the caller's goroutine, for a RESTORED pane: its
// agent resumes a session and does not act before it is prompted, and a
// restore must not wait on git before the daemon listens.
func (s *agentWTStore) seedSoon(paneID string) {
	if s == nil {
		return
	}
	s.goFn(func() { s.seed(paneID) })
}

// agentWTSeedTimeout bounds how long a new agent pane's start waits for the
// baseline: long enough for a listing on a healthy disk, short enough that
// a dead mount costs a slow spawn, never a hung one.
var agentWTSeedTimeout = 3 * time.Second

// seedSync takes the baseline of the pane's directory BEFORE its agent
// starts: spawnPane calls it ahead of the PTY start, holding no pane lock
// (the pane lookup takes PluginMu). It reports whether a baseline is in
// place; on false the spawn goes on and the start edge retries (the
// explicit failure path — an agent is never held back by its record).
func (s *agentWTStore) seedSync(paneID string) bool {
	if s == nil {
		return false
	}
	pane, ok := s.paneFn(paneID)
	if !ok || pane.Sandbox || pane.CWD == "" {
		return false
	}
	s.mu.Lock()
	done := s.seeded[paneID] == normalizeWorktreePath(pane.CWD)
	s.mu.Unlock()
	if done {
		return true
	}
	return s.compareWithin(pane.CWD, agentWTAttr{PaneID: paneID, PaneName: pane.Name, Trigger: agentWTSeedTrigger}, agentWTSeedTimeout)
}

// paneInDir reports a live pane whose AGENT last worked inside path (from its
// probes), or "". The pane's own CWD stays where it was when Claude enters a
// worktree, so a removal must ask this as well.
func (s *agentWTStore) paneInDir(path string) string {
	if s == nil {
		return ""
	}
	type paneDir struct{ id, dir string }
	s.mu.Lock()
	var dirs []paneDir
	for id, dir := range s.agentDirs {
		dirs = append(dirs, paneDir{id, dir})
	}
	for id, subs := range s.subDirs {
		for _, d := range subs {
			dirs = append(dirs, paneDir{id, d.Dir})
		}
	}
	var overflow []string
	for id := range s.subOverflow {
		overflow = append(overflow, id)
	}
	s.mu.Unlock()
	// A pane whose subagents overflowed the bound counts as in use for
	// every path: one of its subagents works somewhere unrecorded.
	for _, id := range overflow {
		if _, live := s.paneFn(id); live {
			return id
		}
	}
	for _, d := range dirs {
		if !pathWithin(path, d.dir) {
			continue
		}
		if _, live := s.paneFn(d.id); live {
			return d.id
		}
	}
	return ""
}

// ownBranchKey scopes a branch name to its repository.
func ownBranchKey(repo, branch string) string {
	return normalizeWorktreePath(repo) + "\x00" + branch
}

// noteOwnWorktree records a worktree this daemon created in repo, so no
// compare ever attributes it to an agent.
func (s *agentWTStore) noteOwnWorktree(repo, path, branch string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ownPaths) < maxAgentWTEntries {
		s.ownPaths[normalizeWorktreePath(path)] = true
	}
	if branch != "" && len(s.ownBranches) < maxAgentWTEntries {
		s.ownBranches[ownBranchKey(repo, branch)] = true
	}
}

func (s *agentWTStore) schedule(dir string, attr agentWTAttr) {
	key := normalizeWorktreePath(dir)
	s.mu.Lock()
	if s.running[key] {
		s.pending[key] = attr
		s.mu.Unlock()
		return
	}
	s.running[key] = true
	s.mu.Unlock()
	s.goFn(func() {
		for {
			s.compare(dir, attr)
			s.mu.Lock()
			next, again := s.pending[key]
			delete(s.pending, key)
			if !again {
				delete(s.running, key)
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			attr = next
		}
	})
}

// compare lists the repository containing dir and folds the difference from
// its baseline into the record.
func (s *agentWTStore) compare(dir string, attr agentWTAttr) {
	s.compareWithin(dir, attr, agentWTTimeout)
}

// compareWithin is compare with its own deadline. It reports whether the
// repository was read.
func (s *agentWTStore) compareWithin(dir string, attr agentWTAttr, timeout time.Duration) bool {
	if !claimBlockingFSCall() {
		return false // the turn-end compare will ask again
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// Stamped BEFORE the listing: two compares of one repository (its main
	// checkout and a new worktree, probed in one tick) run concurrently, and
	// apply refuses a listing taken before the one it already holds.
	listedAt := s.now()
	list, err := s.listFn(ctx, dir)
	var (
		branches   []gitworktree.Branch
		branchesOK bool
		def        string
	)
	if err == nil && len(list) > 0 && list[0].Path != "" {
		b, truncated, berr := s.branchFn(ctx, list[0].Path)
		// A capped or failed branch list is no evidence of absence: the
		// branch half is skipped rather than reading every branch past the
		// cap as gone.
		if berr == nil && !truncated {
			branches, branchesOK = b, true
			def = gitworktree.ShortBranchName(s.defaultFn(ctx, list[0].Path))
		}
	}
	cancel()
	releaseBlockingFSCall()
	// An unreadable listing compares NOTHING and marks nothing gone, the rule
	// worktreeRegistered follows: an unknown answer never authorises a change.
	if err != nil || len(list) == 0 || list[0].Path == "" {
		return false
	}

	s.mu.Lock()
	changed := s.apply(list, branchesOK, branches, def, attr, listedAt)
	if attr.Trigger == agentWTSeedTrigger && attr.PaneID != "" {
		s.seeded[attr.PaneID] = normalizeWorktreePath(dir)
	}
	s.mu.Unlock()
	if changed {
		s.scheduleSave()
	}
	return true
}

// apply folds one listing, taken at listedAt, into the baseline and the
// entries. Caller holds mu. A listing older than the baseline's is refused:
// applying it would undo what a newer one recorded.
func (s *agentWTStore) apply(list []gitworktree.Worktree, branchesOK bool, branches []gitworktree.Branch, def string, attr agentWTAttr, listedAt time.Time) bool {
	now := listedAt
	repoPath := list[0].Path
	key := normalizeWorktreePath(repoPath)

	cur := make([]string, 0, len(list)-1)
	curSet := map[string]gitworktree.Worktree{}
	for _, w := range list[1:] {
		n := normalizeWorktreePath(w.Path)
		cur = append(cur, n)
		curSet[n] = w
	}
	var names []string
	for _, b := range branches {
		names = append(names, b.Name)
	}

	r := s.repos[key]
	if r == nil {
		// First sight: everything already there is the baseline, attributed
		// to nobody.
		s.repos[key] = &agentWTRepo{Worktrees: cur, Branches: names, BranchesKnown: branchesOK, Seen: now}
		s.evictRepos()
		return true
	}
	if now.Before(r.Seen) {
		return false
	}
	r.Seen = now
	changed := false

	// Worktree half.
	base := map[string]bool{}
	for _, p := range r.Worktrees {
		base[p] = true
	}
	madeBranch := map[string]bool{}
	for _, n := range cur {
		if base[n] {
			continue
		}
		w := curSet[n]
		if s.ownPaths[n] {
			delete(s.ownPaths, n)
			continue
		}
		s.add(AgentWTEntry{Kind: agentWTKindWorktree, Repo: repoPath, Path: w.Path, Branch: w.Branch}, attr, now)
		if w.Branch != "" {
			madeBranch[w.Branch] = true
		}
		changed = true
	}
	for p := range base {
		if _, still := curSet[p]; !still {
			gone := s.markGone(func(e AgentWTEntry) bool {
				return e.Kind == agentWTKindWorktree && normalizeWorktreePath(e.Path) == p
			}, now)
			changed = changed || len(gone) > 0
			// Its branch may survive it. Queued on the REPOSITORY, not
			// on the entry: a short-lived entry is dropped right here,
			// and the branch was in the baseline since the worktree
			// appeared, so nothing else would ever look at it again.
			for _, ge := range gone {
				if ge.Branch != "" && len(r.PendingKept) < maxAgentWTEntries {
					r.PendingKept = append(r.PendingKept, agentWTKept{Branch: ge.Branch, PaneID: ge.PaneID,
						PaneName: ge.PaneName, Source: ge.Source, SessionID: ge.SessionID, Trigger: ge.Trigger, FirstSeen: ge.FirstSeen})
				}
			}
		}
	}
	if len(r.Worktrees) != len(cur) {
		changed = true
	}
	r.Worktrees = cur

	// Branch half.
	if branchesOK {
		if !r.BranchesKnown {
			r.Branches, r.BranchesKnown = names, true
			return true
		}
		baseB := map[string]bool{}
		for _, b := range r.Branches {
			baseB[b] = true
		}
		curB := map[string]bool{}
		for _, b := range names {
			curB[b] = true
			if baseB[b] || b == def {
				continue
			}
			if ok := ownBranchKey(repoPath, b); s.ownBranches[ok] {
				delete(s.ownBranches, ok)
				continue
			}
			if madeBranch[b] {
				continue // the worktree entry already carries it
			}
			s.add(AgentWTEntry{Kind: agentWTKindBranch, Repo: repoPath, Branch: b}, attr, now)
			changed = true
		}
		for b := range baseB {
			if !curB[b] {
				bb := b
				gone := s.markGone(func(e AgentWTEntry) bool {
					return e.Kind == agentWTKindBranch && e.Repo == repoPath && e.Branch == bb
				}, now)
				changed = changed || len(gone) > 0
			}
		}
		// A recorded worktree removed WITHOUT its branch leaves the branch
		// behind; its entry is gone, so the branch gets an entry of its own
		// (same attribution) or it could never be cleaned up from the view.
		// Settled from the repository's queue (PendingKept), and only on a
		// complete branch listing: a removal seen while the branch list was
		// unreadable is settled by the next listing that reads, once.
		for _, k := range r.PendingKept {
			if !curB[k.Branch] || k.Branch == def || s.liveBranchEntry(repoPath, k.Branch) {
				continue
			}
			s.add(AgentWTEntry{Kind: agentWTKindBranch, Repo: repoPath, Branch: k.Branch},
				agentWTAttr{PaneID: k.PaneID, PaneName: k.PaneName, Source: k.Source, SessionID: k.SessionID, Trigger: k.Trigger}, k.FirstSeen)
		}
		if len(r.PendingKept) > 0 {
			r.PendingKept = nil
			changed = true
		}
		r.Branches = names
	}
	return changed
}

// liveBranchEntry reports a recorded, not-gone branch entry. Caller holds mu.
func (s *agentWTStore) liveBranchEntry(repo, branch string) bool {
	for _, e := range s.entries {
		if e.Kind == agentWTKindBranch && !e.Gone && e.Repo == repo && e.Branch == branch {
			return true
		}
	}
	return false
}

func (s *agentWTStore) add(e AgentWTEntry, attr agentWTAttr, now time.Time) {
	e.ID = uuid.NewString()
	e.FirstSeen = now
	e.PaneID, e.PaneName, e.Source, e.SessionID = attr.PaneID, attr.PaneName, attr.Source, attr.SessionID
	e.Trigger = attr.Trigger
	s.entries = append(s.entries, e)
	s.evictEntries()
	log.Printf("agent worktrees: %s %s%s appeared while pane %s ran", e.Kind, e.Path, e.Branch, e.PaneID)
}

// markGone marks every live entry match selects as gone, or deletes it when
// it lived less than agentWTShortLived. It returns the entries it matched, as
// they were before.
func (s *agentWTStore) markGone(match func(AgentWTEntry) bool, now time.Time) []AgentWTEntry {
	var matched []AgentWTEntry
	kept := s.entries[:0]
	for _, e := range s.entries {
		if !e.Gone && match(e) {
			matched = append(matched, e)
			if now.Sub(e.FirstSeen) < agentWTShortLived {
				continue
			}
			e.Gone, e.GoneAt = true, now
		}
		kept = append(kept, e)
	}
	s.entries = kept
	return matched
}

// evictEntries keeps at most maxAgentWTEntries: gone ones first, oldest first.
func (s *agentWTStore) evictEntries() {
	for len(s.entries) > maxAgentWTEntries {
		victim := -1
		for i, e := range s.entries {
			if e.Gone && (victim < 0 || e.FirstSeen.Before(s.entries[victim].FirstSeen)) {
				victim = i
			}
		}
		if victim < 0 {
			victim = 0 // entries are appended in time order
		}
		s.entries = append(s.entries[:victim], s.entries[victim+1:]...)
	}
}

// evictRepos keeps at most maxAgentWTRepos baselines, dropping the least
// recently seen.
func (s *agentWTStore) evictRepos() {
	for len(s.repos) > maxAgentWTRepos {
		var oldest string
		for k, r := range s.repos {
			if oldest == "" || r.Seen.Before(s.repos[oldest].Seen) {
				oldest = k
			}
		}
		delete(s.repos, oldest)
	}
}

// entriesSnapshot returns a copy of the record, newest first.
func (s *agentWTStore) entriesSnapshot() []AgentWTEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := append([]AgentWTEntry(nil), s.entries...)
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].FirstSeen.After(out[j].FirstSeen) })
	return out
}

// byID finds one entry.
func (s *agentWTStore) byID(id string) (AgentWTEntry, bool) {
	if s == nil {
		return AgentWTEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return AgentWTEntry{}, false
}

// forget drops one entry. The baseline keeps the worktree or branch, so the
// next compare does not record it again.
func (s *agentWTStore) forget(id string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	found := false
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.ID == id {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	s.entries = kept
	s.mu.Unlock()
	if found {
		s.scheduleSave()
	}
	return found
}

// knownPath reports a recorded, not-gone worktree at the normalised path.
func (s *agentWTStore) knownPath(norm string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Kind == agentWTKindWorktree && !e.Gone && normalizeWorktreePath(e.Path) == norm {
			return true
		}
	}
	return false
}

// load reads the saved record. A missing file is an empty record; an
// unreadable one is logged and ignored — the record is a convenience, and the
// next compare rebuilds the baselines.
func (s *agentWTStore) load() {
	if s == nil || s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("agent worktrees: read %s: %v", s.path, err)
		}
		return
	}
	var f agentWTFile
	if err := json.Unmarshal(b, &f); err != nil || f.V != 1 {
		log.Printf("agent worktrees: %s is not a record this daemon reads; starting empty", s.path)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Repos != nil {
		s.repos = f.Repos
	}
	s.entries = f.Entries
	s.evictRepos()
	s.evictEntries()
}

func (s *agentWTStore) scheduleSave() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveT != nil {
		return
	}
	s.saveT = time.AfterFunc(agentWTSaveDelay, func() {
		s.mu.Lock()
		s.saveT = nil
		s.mu.Unlock()
		s.save()
	})
}

// close stops a pending debounced save and writes the record now. Daemon
// stop calls it after the final snapshot.
func (s *agentWTStore) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.saveT != nil {
		s.saveT.Stop()
		s.saveT = nil
	}
	s.mu.Unlock()
	s.save()
}

// save writes the record now. Called by the debounce and by close.
func (s *agentWTStore) save() {
	if s == nil || s.path == "" {
		return
	}
	// The whole save — snapshot AND write — under saveMu: two saves (the
	// debounce and close, or two debounces) take their snapshots in the order
	// they write, so an older record can never land after a newer one. close
	// also waits here for a save already running.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	b, err := json.Marshal(agentWTFile{V: 1, Repos: s.repos, Entries: s.entries})
	s.mu.Unlock()
	if err != nil {
		log.Printf("agent worktrees: encode: %v", err)
		return
	}
	if err := atomicfile.Write(s.path, b); err != nil {
		log.Printf("agent worktrees: write %s: %v", s.path, err)
	}
}
