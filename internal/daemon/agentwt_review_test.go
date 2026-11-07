package daemon

import (
	"errors"
	"fmt"
	"testing"
	"time"

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

	d.agentWT.subagentsDone(pane.ID, d.agentWT.now())
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

// A new agent pane's start WAITS for the baseline: seedSync does not return
// while the listing is held, and once it does the baseline is in place, so
// the agent's first worktree is recorded.
//
// Fails if the spawn-time seed only queues work and returns.
func TestAgentWT_SeedSyncHoldsTheStartUntilTheBaselineIsIn(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}, gate: make(chan struct{})}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	done := make(chan bool, 1)
	go func() { done <- s.seedSync("pane-a") }()
	select {
	case <-done:
		t.Fatal("seedSync returned while the listing was still held")
	case <-time.After(50 * time.Millisecond):
	}
	close(repo.gate)
	if ok := <-done; !ok {
		t.Fatal("seedSync = false after a successful listing")
	}

	// The agent starts only now; its first worktree is new to the baseline.
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/first", Branch: "first"}}
	repo.branches = []string{"master", "first"}
	s.probe(probeFor("pane-a", "/repo"))
	if got := s.entriesSnapshot(); len(got) != 1 || got[0].Path != "/w/first" {
		t.Errorf("entries = %+v, want the first worktree recorded", got)
	}
}

// A failed baseline read says so, and the pane is not counted as seeded.
func TestAgentWT_SeedSyncReportsAFailedListing(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}, listErr: errors.New("dead mount")}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	if s.seedSync("pane-a") {
		t.Fatal("seedSync = true for a listing that failed")
	}
	s.mu.Lock()
	seeded := s.seeded["pane-a"]
	s.mu.Unlock()
	if seeded != "" {
		t.Errorf("seeded = %q after a failed listing, want none", seeded)
	}
}

// A worktree that goes again within the short-lived window is dropped from
// the record — but its branch, if it survives, still gets an entry: with a
// complete listing, and with a truncated one followed by a complete one.
//
// Fails if the kept-branch check depends on the dropped worktree entry.
func TestAgentWT_ShortLivedWorktreeKeepsItsBranchEntry(t *testing.T) {
	for _, truncatedFirst := range []bool{false, true} {
		repo := &fakeRepo{branches: []string{"master"}}
		s, now := newTestAgentWT(t, repo, agentPanes)
		s.probe(probeFor("pane-a", "/repo"))
		repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
		repo.branches = []string{"master", "x"}
		s.probe(probeFor("pane-a", "/repo"))

		*now = now.Add(agentWTShortLived / 2)
		repo.worktrees, repo.truncated = nil, truncatedFirst
		s.probe(probeFor("pane-a", "/repo"))
		repo.truncated = false
		s.probe(probeFor("pane-a", "/repo"))
		s.probe(probeFor("pane-a", "/repo"))

		var live, wt int
		for _, e := range s.entriesSnapshot() {
			switch {
			case e.Kind == agentWTKindBranch && !e.Gone && e.Branch == "x" && e.PaneID == "pane-a":
				live++
			case e.Kind == agentWTKindWorktree:
				wt++
			}
		}
		if live != 1 || wt != 0 {
			t.Errorf("truncatedFirst=%v: entries = %+v, want the short-lived worktree dropped and one live branch entry for x",
				truncatedFirst, s.entriesSnapshot())
		}
	}
}

// The idle settle clears only what came before it. A subagent of the NEXT
// turn that probes between the settle's idle check and its clear keeps its
// directory.
//
// Fails if subagentsDone drops every subagent directory of the pane.
func TestAgentWT_IdleSettleKeepsANewerTurnsSubagent(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	old := probeFor("pane-a", "/w/old")
	old.Data[hookevents.DataProbeAgent] = "sub-old"
	s.probe(old)

	idleAt := *now // the settle confirms idle here …
	*now = now.Add(time.Second)
	next := probeFor("pane-a", "/w/new") // … a new turn's subagent probes …
	next.Data[hookevents.DataProbeAgent] = "sub-new"
	s.probe(next)
	s.subagentsDone("pane-a", idleAt) // … and only then the clear runs

	if got := s.paneInDir("/w/old"); got != "" {
		t.Errorf("paneInDir(/w/old) = %q, want the finished subagent cleared", got)
	}
	if got := s.paneInDir("/w/new"); got != "pane-a" {
		t.Errorf("paneInDir(/w/new) = %q, want the newer subagent kept", got)
	}
}

// Past the bound on tracked subagents, the pane fails closed: the cleanup
// refuses every worktree while the pane is live, until it settles idle.
//
// Fails if an overflowing subagent's worktree can be removed.
func TestAgentWTAct_SubagentOverflowRefusesCleanup(t *testing.T) {
	last := AgentWTEntry{ID: "e-last", Kind: agentWTKindWorktree, Repo: "/repo", Path: "/w/last", Branch: "last"}
	d := daemonWithRecord(t, last)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/last", Branch: "last"}}, nil)
	calls := stubRemoveWorktree(t, nil)
	for i := 0; i < maxAgentWTSubagents; i++ {
		p := probeFor(pane.ID, "/w/sub")
		p.Data[hookevents.DataProbeAgent] = fmt.Sprintf("sub-%d", i)
		d.agentWT.probe(p)
	}
	p := probeFor(pane.ID, "/w/last")
	p.Data[hookevents.DataProbeAgent] = "sub-overflow"
	d.agentWT.probe(p)

	if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-last", RemoveWorktree: true}); got.Error == "" || len(*calls) != 0 {
		t.Fatalf("act = %+v, removals %v; want a refusal past the subagent bound", got, *calls)
	}

	d.agentWT.subagentsDone(pane.ID, d.agentWT.now())
	if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-last", RemoveWorktree: true}); got.Error != "" || len(*calls) != 1 {
		t.Errorf("act after idle = %+v, removals %v; want /w/last removed", got, *calls)
	}
}

// A process that dies past the subagent bound stops blocking: the exit
// clears its subagent records (an abort never reaches the idle settle).
//
// Fails if only settleIdle clears subOverflow.
func TestAgentWT_ProcessExitClearsSubagentOverflow(t *testing.T) {
	d := newTestDaemon(t)
	pane := spawnedPane(t, d)
	s, _ := newTestAgentWT(t, &fakeRepo{branches: []string{"master"}}, nil)
	s.paneFn, s.now = d.paneForAgentWT, time.Now
	d.agentWT = s
	for i := 0; i <= maxAgentWTSubagents; i++ {
		p := probeFor(pane.ID, "/w/sub")
		p.Data[hookevents.DataProbeAgent] = fmt.Sprintf("sub-%d", i)
		s.probe(p)
	}
	if got := s.paneInDir("/w/unrelated"); got != pane.ID {
		t.Fatalf("paneInDir = %q before the exit, want the overflowing pane", got)
	}

	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(pane, "process_exit", map[string]string{"exit_code": "1"}))

	deadline := time.Now().Add(time.Second)
	for s.paneInDir("/w/unrelated") != "" {
		if time.Now().After(deadline) {
			t.Fatal("the exited pane still blocks every path")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
