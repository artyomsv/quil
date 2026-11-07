package daemon

import (
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
)

// The turn's START edge takes the baseline, so a worktree made in a
// repository nobody listed before is still recorded by the first probe.
func TestAgentWT_SeedRecordsBaselineBeforeTheAgentActs(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.seed("pane-a")
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/first", Branch: "first"}}
	repo.branches = []string{"master", "first"}
	s.probe(probeFor("pane-a", "/repo"))

	got := s.entriesSnapshot()
	if len(got) != 1 || got[0].Path != "/w/first" {
		t.Errorf("entries = %+v, want the first worktree recorded", got)
	}
}

// A seed runs once per pane and directory: later start edges cost nothing.
func TestAgentWT_SeedOncePerPaneAndDir(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.seed("pane-a")
	s.seed("pane-a")

	if repo.lists != 1 {
		t.Errorf("listed %d times for two seeds, want 1", repo.lists)
	}
}

// A pane whose AGENT works inside a worktree (Claude's EnterWorktree leaves
// the pane's CWD behind) counts as using it, so no cleanup removes it.
func TestAgentWT_PaneInDirSeesTheAgentsDirectory(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.probe(probeFor("pane-a", "/w/wt-x/sub"))

	if got := s.paneInDir("/w/wt-x"); got != "pane-a" {
		t.Errorf("paneInDir = %q, want pane-a", got)
	}
	if got := s.paneInDir("/w/wt-x2"); got != "" {
		t.Errorf("paneInDir(sibling) = %q, want none", got)
	}
	s.forgetPane("pane-a")
	if got := s.paneInDir("/w/wt-x"); got != "" {
		t.Errorf("paneInDir after forget = %q, want none", got)
	}
}

// Quil's own branch is scoped to its repository: the same name appearing in
// another repository is still an agent's.
func TestAgentWT_OwnBranchIsScopedToItsRepository(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	s.noteOwnWorktree("/other", "/w/other-made", "feat/x")
	repo.branches = []string{"master", "feat/x"}
	s.probe(probeFor("pane-a", "/repo"))

	got := s.entriesSnapshot()
	if len(got) != 1 || got[0].Kind != agentWTKindBranch || got[0].Branch != "feat/x" {
		t.Errorf("entries = %+v, want feat/x recorded in /repo", got)
	}
}

// A recorded worktree removed WITHOUT its branch leaves a branch entry with
// the same attribution, so the branch can still be cleaned up.
func TestAgentWT_KeptBranchGetsItsOwnEntry(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	repo.branches = []string{"master", "x"}
	s.probe(probeFor("pane-a", "/repo"))

	*now = now.Add(2 * agentWTShortLived)
	repo.worktrees = nil
	s.probe(probeFor("pane-a", "/repo"))

	var wt, br int
	for _, e := range s.entriesSnapshot() {
		switch {
		case e.Kind == agentWTKindWorktree && e.Gone:
			wt++
		case e.Kind == agentWTKindBranch && !e.Gone && e.Branch == "x" && e.PaneID == "pane-a":
			br++
		}
	}
	if wt != 1 || br != 1 {
		t.Errorf("entries = %+v, want a gone worktree and a live branch entry for x", s.entriesSnapshot())
	}
}

// Two subagents of ONE pane working in two worktrees: both worktrees are in
// use, and the cleanup removes neither. Once the pane settles idle (every
// subagent finished) they are free.
//
// Fails if the store keeps one agent directory per pane.
func TestAgentWTAct_TwoSubagentsKeepBothWorktrees(t *testing.T) {
	a := AgentWTEntry{ID: "e-a", Kind: agentWTKindWorktree, Repo: "/repo", Path: "/w/a", Branch: "a"}
	b := AgentWTEntry{ID: "e-b", Kind: agentWTKindWorktree, Repo: "/repo", Path: "/w/b", Branch: "b"}
	d := daemonWithRecord(t, a, b)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/a", Branch: "a"}, {Path: "/w/b", Branch: "b"}}, nil)
	calls := stubRemoveWorktree(t, nil)
	for agent, dir := range map[string]string{"sub-1": "/w/a/src", "sub-2": "/w/b"} {
		p := probeFor(pane.ID, dir)
		p.Data[hookevents.DataProbeAgent] = agent
		d.agentWT.probe(p)
	}

	for _, id := range []string{"e-a", "e-b"} {
		if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: id, RemoveWorktree: true}); got.Error == "" {
			t.Errorf("act(%s) = %+v, want a refusal while a subagent works there", id, got)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("removals %v while subagents work in both worktrees", *calls)
	}

	d.agentWT.subagentsDone(pane.ID)
	if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-a", RemoveWorktree: true}); got.Error != "" || len(*calls) != 1 {
		t.Errorf("act after idle = %+v, removals %v; want /w/a removed", got, *calls)
	}
}

// The spawn-time seed is queued on a worker; it runs before the agent can
// act (the agent process is still starting), so the agent's first worktree
// is recorded, and the turn's start edge does not list again.
//
// Fails if the baseline is taken only on the start edge or by the first probe.
func TestAgentWT_SpawnSeedPrecedesTheFirstMutation(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	var queued []func()
	s.goFn = func(f func()) { queued = append(queued, f) }

	s.seedSoon("pane-a") // spawnPane
	for len(queued) > 0 {
		f := queued[0]
		queued = queued[1:]
		f()
	}
	s.goFn = func(f func()) { f() }
	lists := repo.lists

	s.seed("pane-a") // the turn's start edge: already seeded
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/first", Branch: "first"}}
	repo.branches = []string{"master", "first"}
	s.probe(probeFor("pane-a", "/repo"))

	if got := s.entriesSnapshot(); len(got) != 1 || got[0].Path != "/w/first" {
		t.Errorf("entries = %+v, want the first worktree recorded", got)
	}
	if n := repo.lists - lists; n != 1 {
		t.Errorf("listed %d times after the spawn seed, want 1 (the probe only)", n)
	}
}

// A seed whose listing failed is not a baseline: the next start edge seeds
// again.
//
// Fails if seed marks the pane seeded before a listing succeeded.
func TestAgentWT_FailedSeedIsRetried(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}, listErr: errors.New("dead mount")}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.seed("pane-a")
	repo.mu.Lock()
	repo.listErr = nil
	repo.mu.Unlock()
	s.seed("pane-a")
	s.seed("pane-a")

	if repo.lists != 2 {
		t.Errorf("listed %d times, want 2 (the failed seed, then one retry)", repo.lists)
	}
}

// A worktree removal seen while the branch list could not be read in full
// still gives the surviving branch its entry, at the next full listing —
// exactly once.
//
// Fails if the kept-branch step only looks at worktrees gone in the same
// compare.
func TestAgentWT_KeptBranchSettledAfterATruncatedBranchList(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	repo.branches = []string{"master", "x"}
	s.probe(probeFor("pane-a", "/repo"))

	*now = now.Add(2 * agentWTShortLived)
	repo.worktrees, repo.truncated = nil, true
	s.probe(probeFor("pane-a", "/repo"))
	repo.truncated = false
	s.probe(probeFor("pane-a", "/repo"))
	s.probe(probeFor("pane-a", "/repo"))

	live := 0
	for _, e := range s.entriesSnapshot() {
		if e.Kind == agentWTKindBranch && !e.Gone && e.Branch == "x" {
			live++
		}
	}
	if live != 1 {
		t.Errorf("entries = %+v, want exactly one live branch entry for x", s.entriesSnapshot())
	}
}
