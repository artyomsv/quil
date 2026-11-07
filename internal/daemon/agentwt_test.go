package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/hookevents"
)

// fakeRepo is what the stubbed git answers: one repository at /repo.
type fakeRepo struct {
	mu        sync.Mutex
	worktrees []gitworktree.Worktree // after the main checkout
	branches  []string
	truncated bool
	listErr   error
	lists     int
}

func (f *fakeRepo) list(context.Context, string) ([]gitworktree.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]gitworktree.Worktree{{Path: "/repo", Branch: "master", Main: true}}, f.worktrees...), nil
}

func (f *fakeRepo) branchList(context.Context, string) ([]gitworktree.Branch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gitworktree.Branch
	for _, b := range f.branches {
		out = append(out, gitworktree.Branch{Name: b})
	}
	return out, f.truncated, nil
}

// newTestAgentWT builds a store on fakeRepo with synchronous compares, a
// fixed clock and the given panes.
func newTestAgentWT(t *testing.T, repo *fakeRepo, panes map[string]agentWTPane) (*agentWTStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s := newAgentWTStore("", func(id string) (agentWTPane, bool) { p, ok := panes[id]; return p, ok })
	s.listFn, s.branchFn = repo.list, repo.branchList
	s.defaultFn = func(context.Context, string) string { return "refs/heads/master" }
	s.now = func() time.Time { return now }
	s.goFn = func(f func()) { f() }
	return s, &now
}

func probeFor(pane, dir string) hookevents.Payload {
	return hookevents.Payload{PaneID: pane, Source: "claude", SessionID: "s1", CWD: dir,
		HookEvent: hookevents.EventWorktreeProbe,
		Data:      map[string]string{hookevents.DataProbeTrigger: hookevents.ProbeShell, hookevents.DataProbeCommand: "git worktree add"}}
}

var agentPanes = map[string]agentWTPane{"pane-a": {Name: "claude", CWD: "/repo"}}

// What exists the first time a repository is seen is attributed to nobody.
func TestAgentWT_FirstSightIsBaselineOnly(t *testing.T) {
	repo := &fakeRepo{worktrees: []gitworktree.Worktree{{Path: "/w/old", Branch: "old"}}, branches: []string{"master", "old"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 0 {
		t.Errorf("entries = %+v, want none on first sight", got)
	}
}

// A worktree that appears after the baseline is recorded once, attributed to
// the pane whose probe saw it, with its branch on the same entry.
func TestAgentWT_NewWorktreeAttributedToProbingPane(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	repo.worktrees = []gitworktree.Worktree{{Path: "/w/wt-bash", Branch: "bash-branch"}}
	repo.branches = []string{"master", "bash-branch"}
	s.probe(probeFor("pane-a", "/repo"))

	got := s.entriesSnapshot()
	if len(got) != 1 {
		t.Fatalf("entries = %+v, want one (the worktree carries its branch)", got)
	}
	e := got[0]
	if e.Kind != agentWTKindWorktree || e.Path != "/w/wt-bash" || e.Branch != "bash-branch" ||
		e.PaneID != "pane-a" || e.PaneName != "claude" || e.Source != "claude" || e.Trigger != hookevents.ProbeShell {
		t.Errorf("entry = %+v", e)
	}
}

// Two probes for two directories in one tick are both compared.
func TestAgentWT_TwoProbesInOneTickBothCompared(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.probe(probeFor("pane-a", "/repo"))
	s.probe(probeFor("pane-a", "/w/elsewhere"))

	if repo.lists != 2 {
		t.Errorf("listed %d times for two directories, want 2", repo.lists)
	}
}

// A burst of probes for one directory while a compare runs costs one more
// compare, not one per probe, and records the worktree once.
func TestAgentWT_BurstOfProbesListsOncePerRepo(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo")) // baseline

	var queued []func()
	s.goFn = func(f func()) { queued = append(queued, f) }
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	repo.branches = []string{"master", "x"}
	lists := repo.lists
	for i := 0; i < 5; i++ {
		s.probe(probeFor("pane-a", "/repo"))
	}
	for _, f := range queued {
		f()
	}

	if n := repo.lists - lists; n > 2 {
		t.Errorf("listed %d times for a burst of 5, want at most 2", n)
	}
	if got := s.entriesSnapshot(); len(got) != 1 {
		t.Errorf("entries = %+v, want one", got)
	}
}

// A worktree Quil itself created is never recorded as an agent's.
func TestAgentWT_OwnWorktreeIsNotRecorded(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	s.noteOwnWorktree("/w/quil-made", "feat/quil")
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/quil-made", Branch: "feat/quil"}}
	repo.branches = []string{"master", "feat/quil"}
	s.turnEnded("pane-a")

	if got := s.entriesSnapshot(); len(got) != 0 {
		t.Errorf("entries = %+v, want none for a Quil-made worktree", got)
	}
}

// An unreadable listing changes nothing: no entry marked gone.
func TestAgentWT_ListErrorMarksNothingGone(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	s.probe(probeFor("pane-a", "/repo"))
	*now = now.Add(time.Hour)

	repo.listErr = errors.New("dead mount")
	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 1 || got[0].Gone {
		t.Errorf("entries = %+v, want the one entry, not gone", got)
	}
}

// A worktree that goes away after the short-lived window is marked gone and
// kept; one gone within the window is dropped.
func TestAgentWT_RemovedWorktreeMarkedGone(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	s.probe(probeFor("pane-a", "/repo"))

	*now = now.Add(2 * agentWTShortLived)
	repo.worktrees = nil
	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 1 || !got[0].Gone {
		t.Errorf("entries = %+v, want one gone entry", got)
	}
}

func TestAgentWT_ShortLivedEntryIsDropped(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/repo/.claude/worktrees/agent-1", Branch: "worktree-agent-1"}}
	s.probe(probeFor("pane-a", "/repo"))

	*now = now.Add(agentWTShortLived / 2)
	repo.worktrees = nil
	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 0 {
		t.Errorf("entries = %+v, want a worktree gone within the window dropped", got)
	}
}

// A sandbox pane's paths are inside its container: never listed.
func TestAgentWT_SandboxPaneIgnored(t *testing.T) {
	repo := &fakeRepo{}
	s, _ := newTestAgentWT(t, repo, map[string]agentWTPane{"pane-s": {Name: "boxed", Sandbox: true}})

	s.probe(probeFor("pane-s", "/workspace"))
	s.turnEnded("pane-s")

	if repo.lists != 0 {
		t.Errorf("listed %d times for a sandbox pane, want 0", repo.lists)
	}
}

// The turn end compares what the pane probed AND its own directory, so a
// worktree made by a command no probe matched is still caught.
func TestAgentWT_TurnEndComparesProbedReposAndPaneCWD(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	panes := map[string]agentWTPane{"pane-a": {Name: "claude", CWD: "/repo"}}
	s, _ := newTestAgentWT(t, repo, panes)
	s.probe(probeFor("pane-a", "/w/other"))
	lists := repo.lists

	s.turnEnded("pane-a")

	if n := repo.lists - lists; n != 2 {
		t.Errorf("turn end listed %d directories, want 2 (the probed one and the pane's)", n)
	}
}

// The baseline survives a restart, or the next compare would record nothing
// it already knew — and everything as new after a lost file.
func TestAgentWT_PersistRoundTripKeepsBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-worktrees.json")
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.path = path
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	s.probe(probeFor("pane-a", "/repo"))
	s.close() // stops the debounced save and writes now

	s2, _ := newTestAgentWT(t, repo, agentPanes)
	s2.path = path
	s2.load()
	t.Cleanup(s2.close) // before TempDir's own cleanup, which runs later
	repo.worktrees = append(repo.worktrees, gitworktree.Worktree{Path: "/w/y", Branch: "y"})
	s2.probe(probeFor("pane-a", "/repo"))

	got := s2.entriesSnapshot()
	if len(got) != 2 {
		t.Fatalf("entries after reload = %+v, want x (kept) and y (new)", got)
	}
}

// Past the cap, gone entries go first.
func TestAgentWT_CapsEvictGoneFirst(t *testing.T) {
	s, _ := newTestAgentWT(t, &fakeRepo{}, agentPanes)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.entries = append(s.entries, AgentWTEntry{ID: "gone", Gone: true, FirstSeen: t0.Add(time.Hour)})
	for i := 0; i < maxAgentWTEntries; i++ {
		s.entries = append(s.entries, AgentWTEntry{ID: "live", FirstSeen: t0})
	}
	s.evictEntries()
	for _, e := range s.entries {
		if e.ID == "gone" {
			t.Fatal("evicted a live entry while a gone one was kept")
		}
	}
	if len(s.entries) != maxAgentWTEntries {
		t.Errorf("len = %d, want %d", len(s.entries), maxAgentWTEntries)
	}
}

// Two probes reach the record even when they arrive together, and neither
// reaches the notification queue.
//
// Fails if routeHookPayload sends a probe through the ingester (the
// coalescer keeps only the last of the two).
func TestRouteHookPayload_ProbeBypassesCoalescerAndQueue(t *testing.T) {
	d := newTestDaemon(t)
	d.hookIngester = hookevents.NewIngester(d.emitHookEvent)
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	d.agentWT = s

	d.routeHookPayload(probeFor("pane-a", "/repo"))
	d.routeHookPayload(probeFor("pane-a", "/w/two"))

	if repo.lists != 2 {
		t.Errorf("record compared %d directories, want 2", repo.lists)
	}
	d.hookIngester.FlushAll()
	if n := d.events.Count(); n != 0 {
		t.Errorf("probes reached the event queue: %d events", n)
	}
}

// A new branch with no worktree is recorded; the default branch never is.
func TestAgentWT_NewBranchRecorded(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	repo.branches = []string{"master", "feat/plain"}
	s.probe(probeFor("pane-a", "/repo"))

	got := s.entriesSnapshot()
	if len(got) != 1 || got[0].Kind != agentWTKindBranch || got[0].Branch != "feat/plain" || got[0].Path != "" {
		t.Errorf("entries = %+v, want one branch entry feat/plain", got)
	}
}

func TestAgentWT_BranchOfNewWorktreeIsNotASecondEntry(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	repo.branches = []string{"master", "x"}
	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 1 || got[0].Kind != agentWTKindWorktree {
		t.Errorf("entries = %+v, want only the worktree entry", got)
	}
}

// A capped branch list is no evidence: nothing is recorded or marked gone from
// it, while the worktree half still runs.
func TestAgentWT_TruncatedBranchListSkipsBranchHalfOnly(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master", "a"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.branches = []string{"master", "a", "b"}
	s.probe(probeFor("pane-a", "/repo")) // records branch b
	*now = now.Add(time.Hour)

	repo.truncated = true
	repo.branches = []string{"master"} // cut off
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	s.probe(probeFor("pane-a", "/repo"))

	var gotB, gotWT bool
	for _, e := range s.entriesSnapshot() {
		if e.Kind == agentWTKindBranch && e.Branch == "b" && e.Gone {
			t.Error("a capped branch list marked branch b gone")
		}
		gotB = gotB || e.Branch == "b"
		gotWT = gotWT || e.Path == "/w/x"
	}
	if !gotB || !gotWT {
		t.Errorf("want branch b kept and worktree x recorded: %+v", s.entriesSnapshot())
	}
}

func TestAgentWT_DefaultBranchNeverRecorded(t *testing.T) {
	repo := &fakeRepo{branches: []string{"feat"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	repo.branches = []string{"feat", "master"} // the default appears later
	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 0 {
		t.Errorf("entries = %+v, want the default branch never recorded", got)
	}
}
