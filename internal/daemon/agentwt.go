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
	Command   string    `json:"command,omitempty"`
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
}

type agentWTFile struct {
	V       int                     `json:"v"`
	Repos   map[string]*agentWTRepo `json:"repos"`
	Entries []AgentWTEntry          `json:"entries"`
}

// agentWTAttr is who a compare attributes new entries to.
type agentWTAttr struct {
	PaneID, PaneName, Source, SessionID, Trigger, Command string
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
	// baseline and never become entries.
	ownPaths    map[string]bool
	ownBranches map[string]bool
	// One compare per directory at a time; a probe that arrives meanwhile
	// runs it once more.
	running map[string]bool
	pending map[string]agentWTAttr
	saveT   *time.Timer

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
		Trigger: p.Data[hookevents.DataProbeTrigger], Command: p.Data[hookevents.DataProbeCommand],
	}
	s.mu.Lock()
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
	attr.PaneID, attr.PaneName, attr.Trigger, attr.Command = paneID, pane.Name, "turn-end", ""
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
	s.mu.Unlock()
}

// noteOwnWorktree records a worktree this daemon created, so no compare ever
// attributes it to an agent.
func (s *agentWTStore) noteOwnWorktree(path, branch string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ownPaths) < maxAgentWTEntries {
		s.ownPaths[normalizeWorktreePath(path)] = true
	}
	if branch != "" && len(s.ownBranches) < maxAgentWTEntries {
		s.ownBranches[branch] = true
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
	if !claimBlockingFSCall() {
		return // the turn-end compare will ask again
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentWTTimeout)
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
		return
	}

	s.mu.Lock()
	changed := s.apply(list, branchesOK, branches, def, attr, listedAt)
	s.mu.Unlock()
	if changed {
		s.scheduleSave()
	}
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
			changed = s.markGone(func(e AgentWTEntry) bool {
				return e.Kind == agentWTKindWorktree && normalizeWorktreePath(e.Path) == p
			}, now) || changed
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
			if s.ownBranches[b] {
				delete(s.ownBranches, b)
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
				changed = s.markGone(func(e AgentWTEntry) bool {
					return e.Kind == agentWTKindBranch && e.Repo == repoPath && e.Branch == bb
				}, now) || changed
			}
		}
		r.Branches = names
	}
	return changed
}

func (s *agentWTStore) add(e AgentWTEntry, attr agentWTAttr, now time.Time) {
	e.ID = uuid.NewString()
	e.FirstSeen = now
	e.PaneID, e.PaneName, e.Source, e.SessionID = attr.PaneID, attr.PaneName, attr.Source, attr.SessionID
	e.Trigger, e.Command = attr.Trigger, attr.Command
	s.entries = append(s.entries, e)
	s.evictEntries()
	log.Printf("agent worktrees: %s %s%s appeared while pane %s ran", e.Kind, e.Path, e.Branch, e.PaneID)
}

// markGone marks every live entry match selects as gone, or deletes it when
// it lived less than agentWTShortLived. Reports whether anything changed.
func (s *agentWTStore) markGone(match func(AgentWTEntry) bool, now time.Time) bool {
	changed := false
	kept := s.entries[:0]
	for _, e := range s.entries {
		if !e.Gone && match(e) {
			changed = true
			if now.Sub(e.FirstSeen) < agentWTShortLived {
				continue
			}
			e.Gone, e.GoneAt = true, now
		}
		kept = append(kept, e)
	}
	s.entries = kept
	return changed
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
