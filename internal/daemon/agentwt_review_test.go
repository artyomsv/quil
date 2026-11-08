package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	if got := s.paneInDir("/w/wt-x", nil); got != "pane-a" {
		t.Errorf("paneInDir = %q, want pane-a", got)
	}
	if got := s.paneInDir("/w/wt-x2", nil); got != "" {
		t.Errorf("paneInDir(sibling) = %q, want none", got)
	}
	s.forgetPane("pane-a")
	if got := s.paneInDir("/w/wt-x", nil); got != "" {
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
	repo := &fakeRepo{branches: []string{"master"}, gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	done := make(chan bool, 1)
	go func() { done <- s.seedSync("pane-a") }()
	select {
	case <-repo.entered: // the listing has started and is held
	case <-time.After(5 * time.Second):
		t.Fatal("the seed never started its listing")
	}
	select {
	case <-done:
		t.Fatal("seedSync returned while the listing was still held")
	default:
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

	if got := s.paneInDir("/w/old", nil); got != "" {
		t.Errorf("paneInDir(/w/old) = %q, want the finished subagent cleared", got)
	}
	if got := s.paneInDir("/w/new", nil); got != "pane-a" {
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

// An incomplete pre-start read never becomes a silent baseline. A timeout
// (the fake honours the deadline) and a cut branch list each mark the
// repository Uncertain, which the cleanup view shows; a quick "not a
// repository" answer and a complete read do not.
//
// Fails if a failed seed lets the next first-sight listing adopt what the
// agent made meanwhile without saying so.
func TestAgentWT_IncompleteSeedMarksTheRepositoryUncertain(t *testing.T) {
	prev := agentWTSeedTimeout
	agentWTSeedTimeout = 20 * time.Millisecond
	t.Cleanup(func() { agentWTSeedTimeout = prev })

	for _, tc := range []struct {
		name          string
		repo          *fakeRepo
		afterSeed     func(r *fakeRepo)
		wantUncertain bool
	}{
		{"timeout", &fakeRepo{branches: []string{"master"}, gate: make(chan struct{})},
			func(r *fakeRepo) { r.gate = nil }, true},
		{"cut branch list", &fakeRepo{branches: []string{"master"}, truncated: true},
			func(r *fakeRepo) { r.truncated = false }, true},
		{"not a repository", &fakeRepo{branches: []string{"master"}, listErr: gitworktree.ErrNotRepository},
			func(r *fakeRepo) { r.listErr = nil }, false},
		{"truncated listing", &fakeRepo{branches: []string{"master"}, listErr: fmt.Errorf("%w: big", gitworktree.ErrOutputTruncated)},
			func(r *fakeRepo) { r.listErr = nil }, true},
		{"unsafe directory", &fakeRepo{branches: []string{"master"}, listErr: errors.New("exit status 128: fatal: detected dubious ownership")},
			func(r *fakeRepo) { r.listErr = nil }, true},
		{"git missing", &fakeRepo{branches: []string{"master"}, listErr: exec.ErrNotFound},
			func(r *fakeRepo) { r.listErr = nil }, true},
		{"complete", &fakeRepo{branches: []string{"master"}}, func(*fakeRepo) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestAgentWT(t, tc.repo, agentPanes)
			ok := s.seedSync("pane-a")
			if ok != (tc.name == "complete") {
				t.Fatalf("seedSync = %v", ok)
			}
			tc.afterSeed(tc.repo)
			// The agent acts, then its first probe is the first listing
			// that reads (for the failed seeds).
			tc.repo.worktrees = []gitworktree.Worktree{{Path: "/w/early", Branch: "early"}}
			tc.repo.branches = []string{"master", "early"}
			s.probe(probeFor("pane-a", "/repo"))

			got := s.uncertainRepos()
			if tc.wantUncertain && (len(got) != 1 || got[0] != "/repo") {
				t.Errorf("uncertainRepos = %v, want /repo", got)
			}
			if !tc.wantUncertain && len(got) != 0 {
				t.Errorf("uncertainRepos = %v, want none", got)
			}
		})
	}
}

// The cleanup view's list answer carries the uncertain repositories.
func TestAgentWorktreesResponse_CarriesUncertainRepos(t *testing.T) {
	d := daemonWithRecord(t)
	d.agentWT.repos["/repo"] = &agentWTRepo{Path: "/repo", Uncertain: true}

	got := d.agentWorktreesResponse()

	if len(got.UncertainRepos) != 1 || got.UncertainRepos[0] != "/repo" {
		t.Errorf("UncertainRepos = %v, want [/repo]", got.UncertainRepos)
	}
}

// A child that dies past the subagent bound stops blocking, and its exit
// clears ONLY its own records: old child exits → the pane restarts → the new
// child's subagent probes → the old exit's cleanup runs late. The new
// subagent keeps its worktree; the old overflow is gone.
//
// Fails if the exit cleanup is bound to a clock instead of the generation.
func TestAgentWT_LateExitCleanupKeepsTheRestartedChildsRecords(t *testing.T) {
	gen := uint64(1)
	s, _ := newTestAgentWT(t, &fakeRepo{branches: []string{"master"}}, nil)
	s.paneFn = func(id string) (agentWTPane, bool) {
		return agentWTPane{Name: "claude", CWD: "/repo", Gen: gen}, id == "pane-a"
	}
	for i := 0; i <= maxAgentWTSubagents; i++ { // the old child overflows
		p := probeFor("pane-a", "/w/old")
		p.Data[hookevents.DataProbeAgent] = fmt.Sprintf("old-%d", i)
		s.probe(p)
	}
	if got := s.paneInDir("/w/unrelated", nil); got != "pane-a" {
		t.Fatalf("paneInDir = %q before the exit, want the overflowing pane", got)
	}

	gen = 2 // the old child exited and the pane restarted …
	p := probeFor("pane-a", "/w/new")
	p.Data[hookevents.DataProbeAgent] = "new-1" // … the new child's subagent probes …
	s.probe(p)
	s.subagentsExited("pane-a", 1) // … and only now the old exit is handled

	if got := s.paneInDir("/w/unrelated", nil); got != "" {
		t.Errorf("paneInDir(/w/unrelated) = %q, want the old overflow cleared", got)
	}
	if got := s.paneInDir("/w/old", nil); got != "" {
		t.Errorf("paneInDir(/w/old) = %q, want the old child's records cleared", got)
	}
	if got := s.paneInDir("/w/new", nil); got != "pane-a" {
		t.Errorf("paneInDir(/w/new) = %q, want the new child's subagent kept", got)
	}
}

// The daemon's exit handler is what calls the cleanup.
//
// Fails if onPaneExitGeneration stops clearing the exited child's records.
func TestOnPaneExit_ClearsTheChildsSubagentRecords(t *testing.T) {
	d := daemonWithRecord(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	d.agentWT.subDirs[pane.ID] = map[string]agentDir{"sub": {Dir: "/w/a"}}
	d.agentWT.subOverflow[pane.ID] = agentDir{}

	d.onPaneExit(pane, 1)

	if got := d.agentWT.paneInDir("/w/a", nil); got != "" {
		t.Errorf("paneInDir(/w/a) = %q after the exit, want none", got)
	}
}

// The daemon refuses a cleanup in an uncertain repository unless the request
// acknowledges it — any IPC client can send the act.
//
// Fails if the Uncertain mark is display-only.
func TestAgentWTAct_UncertainRepoNeedsTheAcknowledgment(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	d.agentWT.repos["/repo"] = &agentWTRepo{Path: "/repo", Uncertain: true}
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true}); got.Error == "" || len(*calls) != 0 {
		t.Fatalf("act = %+v, removals %v; want a refusal without the acknowledgment", got, *calls)
	}
	if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true, AcceptUncertain: true}); got.Error != "" || len(*calls) != 1 {
		t.Errorf("act = %+v, removals %v; want the removal once acknowledged", got, *calls)
	}
}

// The new child's hook can arrive after spawnPane published startingGen but
// before ptyGen moves. Its subagent record must carry the NEW generation, so
// a late exit cleanup of the old child keeps it.
//
// Fails if paneForAgentWT reports ptyGen alone.
func TestAgentWT_ProbeBeforeGenerationBumpCarriesTheStartingGen(t *testing.T) {
	d := daemonWithRecord(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	pane.PluginMu.Lock()
	pane.ptyGen, pane.startingGen = 1, 2 // child 2 is starting; ptyGen not bumped yet
	pane.PluginMu.Unlock()

	p := probeFor(pane.ID, "/w/new")
	p.Data[hookevents.DataProbeAgent] = "new-1"
	d.agentWT.probe(p)
	d.agentWT.subagentsExited(pane.ID, 1) // the old child's exit, handled late

	if got := d.agentWT.paneInDir("/w/new", nil); got != pane.ID {
		t.Errorf("paneInDir(/w/new) = %q, want the starting child's subagent kept", got)
	}
}

// Only a Quil window may acknowledge an uncertain record: a bridge, the web
// gateway or a conn that never said hello is refused; a plain request is not
// affected.
func TestUncertainAckRefusal_OnlyTheTUI(t *testing.T) {
	ack := ipc.AgentWorktreeActReqPayload{ID: "e", RemoveWorktree: true, AcceptUncertain: true}
	for _, role := range []string{"bridge", "web", ""} {
		if uncertainAckRefusal(ack, role) == "" {
			t.Errorf("role %q may acknowledge", role)
		}
	}
	if r := uncertainAckRefusal(ack, "tui"); r != "" {
		t.Errorf("tui refused: %s", r)
	}
	if r := uncertainAckRefusal(ipc.AgentWorktreeActReqPayload{ID: "e", RemoveWorktree: true}, "bridge"); r != "" {
		t.Errorf("a request without the acknowledgment refused: %s", r)
	}
}

// An agent or a shell working in the worktree THROUGH A SYMLINK still keeps
// it: git lists the real path, the probe and the pane CWD name the link.
//
// Fails if the delete check compares paths only as text.
func TestPaneInWorktree_SeesResidentsThroughASymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// A subagent probing through the link, its pane elsewhere.
	d := daemonWithRecord(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	p := probeFor(pane.ID, filepath.Join(link, "src"))
	p.Data[hookevents.DataProbeAgent] = "sub-1"
	d.agentWT.probe(p)
	if got := d.paneInWorktree(real, true); got != pane.ID {
		t.Errorf("subagent via link: paneInWorktree = %q, want %s", got, pane.ID)
	}

	// A shell whose CWD is the link.
	d2 := daemonWithRecord(t)
	tab2 := d2.session.CreateTab("t")
	shell, err := d2.session.CreatePane(tab2.ID, link)
	if err != nil {
		t.Fatal(err)
	}
	if got := d2.paneInWorktree(real, true); got != shell.ID {
		t.Errorf("shell via link: paneInWorktree = %q, want %s", got, shell.ID)
	}
	// The text-only compare (the listing's hint) does not resolve.
	if got := d2.paneInWorktree(real, false); got != "" {
		t.Errorf("lexical compare resolved a link: %q", got)
	}
}

// A pane on a share that never answers cannot hold the delete check: the
// resolution gives up on the shared deadline and the worktree is KEPT.
//
// Fails if the check resolves symlinks without a bound, or deletes on an
// unanswered resolution.
func TestPaneInWorktree_HungResolutionKeepsTheWorktreeAndReturns(t *testing.T) {
	prevT, prevE := resolveTimeout, evalSymlinksFn
	resolveTimeout = 50 * time.Millisecond
	hang := make(chan struct{})
	evalSymlinksFn = func(p string) (string, error) {
		if p == "/mnt/dead" {
			<-hang
		}
		return p, nil
	}
	t.Cleanup(func() { close(hang); resolveTimeout, evalSymlinksFn = prevT, prevE })

	d := daemonWithRecord(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/mnt/dead")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan string, 1)
	go func() { done <- d.paneInWorktree("/w/healthy", true) }()
	select {
	case got := <-done:
		if got != pane.ID {
			t.Errorf("paneInWorktree = %q, want the unanswered pane (keep the worktree)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the delete check hung on an unanswered resolution")
	}
}

// A subagent last reported a subfolder THROUGH A LINK, and that subfolder was
// then renamed: the stale path no longer resolves, but it still names the
// real worktree it was under. The close's delete gate keeps the worktree.
//
// Fails if a missing path is read as "nobody there".
func TestRemoveOwnedWorktrees_RenamedDescendantThroughALinkKeepsTheWorktree(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "wt")
	sub := filepath.Join(real, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	d := daemonWithRecord(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	p := probeFor(pane.ID, filepath.Join(link, "sub"))
	p.Data[hookevents.DataProbeAgent] = "sub-1"
	d.agentWT.probe(p)
	if err := os.Rename(sub, filepath.Join(real, "moved")); err != nil {
		t.Fatal(err)
	}
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: real, Branch: "feat"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	d.removeOwnedWorktreesWith([]string{real}, branchDeleteOpts{Tokens: map[string]string{real: "test-token"}})

	if len(*calls) != 0 {
		t.Errorf("removed %v while a subagent's (renamed) folder is inside it", *calls)
	}
}
