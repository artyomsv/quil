package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitinfo"
	"github.com/artyomsv/quil/internal/prinfo"
)

// fakePR answers prProbeFn. Every call parks until the test releases it, so
// completion ORDER — the thing the generation rules exist for — is the test's
// to choose, not the scheduler's.
type fakePR struct {
	mu      sync.Mutex
	calls   []prCall
	answers map[string]prinfo.Info   // keyed by branch
	results map[string]prinfo.Result // keyed by branch; missing → Answer
	// release gates every probe; a test closes it (or sends per call) to
	// let completions land. nil means "complete at once".
	release chan struct{}
	// ctxs records the context each probe received, for the shutdown test.
	ctxs []context.Context
	// availCalls counts prAvailableFn calls (the LookPath the cache must not
	// repeat every tick).
	availCalls int
	// done receives one value per COMPLETED runPRProbe (after its result was
	// applied or discarded), so a test can wait for a completion to have
	// landed rather than sleep and hope.
	done chan struct{}
	// abort is closed by the test's cleanup so a probe a FAILED test left
	// parked returns instead of outliving the test — a leaked goroutine
	// holds a permit the next test needs and reads package seams the cleanup
	// is restoring (a data race CI caught on 2026-10-27).
	abort chan struct{}
}

type prCall struct{ dir, branch string }

// installFakePR swaps the three PR seams for the fake. The cleanup order is
// load-bearing: abort parked probes, WAIT for every probe goroutine of c to
// finish, and only then restore the seams — restoring first races a
// goroutine still reading them.
func installFakePR(t *testing.T, c *gitCache, f *fakePR, available bool) {
	t.Helper()
	origProbe, origAvail, origHook := prProbeFn, prAvailableFn, prCompletionHook
	f.done = make(chan struct{}, 64)
	f.abort = make(chan struct{})
	t.Cleanup(func() {
		close(f.abort)
		c.prWG.Wait()
		prProbeFn, prAvailableFn, prCompletionHook = origProbe, origAvail, origHook
	})
	prCompletionHook = func() { f.done <- struct{}{} }
	prAvailableFn = func() bool {
		f.mu.Lock()
		f.availCalls++
		f.mu.Unlock()
		return available
	}
	prProbeFn = func(ctx context.Context, dir, branch string) (prinfo.Info, prinfo.Result) {
		f.mu.Lock()
		f.calls = append(f.calls, prCall{dir, branch})
		f.ctxs = append(f.ctxs, ctx)
		rel := f.release
		f.mu.Unlock()
		if rel != nil {
			select {
			case <-rel:
			case <-ctx.Done():
				return prinfo.Info{}, prinfo.Unknown
			case <-f.abort:
				return prinfo.Info{}, prinfo.Unknown
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if res, ok := f.results[branch]; ok && res != prinfo.Answer {
			return prinfo.Info{}, res
		}
		return f.answers[branch], prinfo.Answer
	}
}

func (f *fakePR) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// waitDone blocks until one more runPRProbe has finished — applied or
// discarded — so an assertion about "the completion did NOT change anything"
// runs after the completion, not before it.
func (f *fakePR) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no probe completion within 2s")
	}
}

func (f *fakePR) branches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.branch)
	}
	return out
}

// prFixture is one checkout at /repo on the given branch, PR probing on.
func prFixture(t *testing.T, branch string) (*gitCache, *fakeGitProbes) {
	t.Helper()
	g := &fakeGitProbes{
		dirs:  map[string][2]string{"/repo": {"/repo/.git", "/repo/.git"}},
		infos: map[string]gitinfo.Info{"/repo": {Branch: branch}},
	}
	installFakeGit(t, g)
	c := newGitCache()
	c.prEnabled = true
	return c, g
}

func (c *gitCache) entry(t *testing.T, dir string) *gitEntry {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.byDir[dir]
	if e == nil {
		t.Fatalf("no entry for %q", dir)
	}
	return e
}

// setPRAt moves a checkout's PR clock; a checkout not resolved yet (no
// refresh has run) has no clock to move and is left alone.
func (c *gitCache) setPRAt(dir string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.byDir[dir]; e != nil {
		e.prAt = at
	}
}

func TestPRProbe_RunsOnceOnFirstSightOfABranch(t *testing.T) {
	c, _ := prFixture(t, "main")
	f := &fakePR{answers: map[string]prinfo.Info{"main": {Number: 277, Status: prinfo.StatusApproved}}}
	installFakePR(t, c, f, true)

	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()

	if got := f.branches(); len(got) != 1 || got[0] != "main" {
		t.Fatalf("probes = %v, want one for main", got)
	}
	info, known, stale := c.lookupPR("/repo")
	if !known || stale || info.Number != 277 || info.Status != prinfo.StatusApproved {
		t.Fatalf("lookupPR = %+v known=%v stale=%v", info, known, stale)
	}
}

func TestPRProbe_NotBeforeIntervalElapses(t *testing.T) {
	c, _ := prFixture(t, "main")
	f := &fakePR{answers: map[string]prinfo.Info{"main": {Number: 1}}}
	installFakePR(t, c, f, true)

	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if n := f.count(); n != 1 {
		t.Fatalf("%d probes within the interval, want 1", n)
	}
	c.setPRAt("/repo/.git", time.Now().Add(-prRefreshInterval-time.Second))
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if n := f.count(); n != 2 {
		t.Fatalf("%d probes after the interval elapsed, want 2", n)
	}
}

func TestPRProbe_BranchChangeAsksAtOnceAndClearsTheOldNumber(t *testing.T) {
	c, g := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}, "B": {Number: 300}}}
	installFakePR(t, c, f, true)
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()

	// The branch moves. Probes now park until released.
	g.infos["/repo"] = gitinfo.Info{Branch: "B"}
	rel := make(chan struct{})
	f.mu.Lock()
	f.release = rel
	f.mu.Unlock()
	c.refresh(context.Background(), []string{"/repo"})

	if _, known, _ := c.lookupPR("/repo"); known {
		t.Fatal("A's number still shown beside branch B before B answered")
	}
	// The probe is recorded when its goroutine starts, not when refresh
	// launches it.
	waitUntil(t, "B's probe to start", func() bool { return f.count() == 2 })
	if got := f.branches(); got[1] != "B" {
		t.Fatalf("probes = %v, want A then B at once", got)
	}
	close(rel)
	c.prWG.Wait()
	if info, known, _ := c.lookupPR("/repo"); !known || info.Number != 300 {
		t.Fatalf("after B answered: %+v known=%v, want 300", info, known)
	}
}

func TestPRProbe_Gates(t *testing.T) {
	t.Run("feature off", func(t *testing.T) {
		c, _ := prFixture(t, "main")
		c.prEnabled = false
		f := &fakePR{}
		installFakePR(t, c, f, true)
		c.refresh(context.Background(), []string{"/repo"})
		c.prWG.Wait()
		if f.count() != 0 {
			t.Fatal("probe ran with pr_status off")
		}
	})
	t.Run("gh not installed", func(t *testing.T) {
		c, _ := prFixture(t, "main")
		f := &fakePR{}
		installFakePR(t, c, f, false)
		c.refresh(context.Background(), []string{"/repo"})
		c.prWG.Wait()
		if f.count() != 0 {
			t.Fatal("probe ran without gh on PATH")
		}
		if _, known, stale := c.lookupPR("/repo"); known || stale {
			t.Fatal("no gh must leave the row exactly as today: nothing known, nothing stale")
		}
	})
	t.Run("detached head", func(t *testing.T) {
		c, _ := prFixture(t, "")
		f := &fakePR{}
		installFakePR(t, c, f, true)
		c.refresh(context.Background(), []string{"/repo"})
		c.prWG.Wait()
		if f.count() != 0 {
			t.Fatal("probe ran for a detached HEAD")
		}
	})
	t.Run("single flight", func(t *testing.T) {
		c, _ := prFixture(t, "main")
		f := &fakePR{release: make(chan struct{}), answers: map[string]prinfo.Info{"main": {Number: 1}}}
		installFakePR(t, c, f, true)
		c.refresh(context.Background(), []string{"/repo"})
		waitUntil(t, "the first probe to start", func() bool { return f.count() == 1 })
		c.refresh(context.Background(), []string{"/repo"})
		if n := f.count(); n != 1 {
			t.Fatalf("%d probes with one in flight, want 1", n)
		}
		close(f.release)
		c.prWG.Wait()
	})
}

func TestPRProbe_NoPermitMarksStaleAndRetriesNextTick(t *testing.T) {
	c, _ := prFixture(t, "main")
	f := &fakePR{answers: map[string]prinfo.Info{"main": {Number: 277}}}
	installFakePR(t, c, f, true)
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()

	// Fill the pool only up to the PR probe's own ceiling: the probe must be
	// refused there while the git probe (full pool) still runs.
	for i := int64(0); i < maxPRProbePermits; i++ {
		if !claimBlockingFSCall() {
			t.Fatal("could not fill the permit pool to the PR ceiling")
		}
	}
	c.setPRAt("/repo/.git", time.Time{})
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if _, known, stale := c.lookupPR("/repo"); !known || !stale {
		t.Fatalf("refused permit: known=%v stale=%v, want known and stale", known, stale)
	}
	if n := f.count(); n != 1 {
		t.Fatalf("%d probes with the pool at the PR ceiling, want 1", n)
	}
	for i := int64(0); i < maxPRProbePermits; i++ {
		releaseBlockingFSCall()
	}
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if n := f.count(); n != 2 {
		t.Fatalf("%d probes after the pool drained, want 2", n)
	}
	if _, _, stale := c.lookupPR("/repo"); stale {
		t.Fatal("still stale after a fresh answer")
	}
}

// --- generation rules ---

// moveBranch makes the git probe report a new branch on the next refresh.
// (The git probe always re-runs here: /repo/.git/HEAD does not exist, so the
// HEAD-mtime shortcut never fires in these tests.)
func moveBranch(t *testing.T, c *gitCache, g *fakeGitProbes, branch string) {
	t.Helper()
	g.infos["/repo"] = gitinfo.Info{Branch: branch}
}

// perCallRelease hands out one release channel per probe, in call order.
type perCallRelease struct {
	mu    sync.Mutex
	chans []chan struct{}
}

func (p *perCallRelease) install(f *fakePR) {
	// release is read once per call under f.mu; swap a fresh channel in
	// before each refresh so each probe parks on its own.
	f.mu.Lock()
	ch := make(chan struct{})
	p.chans = append(p.chans, ch)
	f.release = ch
	f.mu.Unlock()
}

func (p *perCallRelease) release(i int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.chans[i])
}

func TestPRProbe_LateAnswerForOldBranchIsDiscarded(t *testing.T) {
	c, g := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}, "B": {Number: 300}}}
	installFakePR(t, c, f, true)
	rel := &perCallRelease{}

	rel.install(f)
	c.refresh(context.Background(), []string{"/repo"}) // A in flight
	// A must have read ITS release channel before the next install swaps it.
	waitUntil(t, "A's probe to start", func() bool { return f.count() == 1 })
	moveBranch(t, c, g, "B")
	rel.install(f)
	c.refresh(context.Background(), []string{"/repo"}) // B in flight, gen 2
	waitUntil(t, "B's probe to start", func() bool { return f.count() == 2 })

	rel.release(0) // A's late answer
	f.waitDone(t)  // ...has now been applied or discarded
	e := c.entry(t, "/repo/.git")
	c.mu.Lock()
	known, inFlight := e.prKnown, e.prInFlightGen
	c.mu.Unlock()
	if known {
		t.Fatal("A's late answer was shown beside B")
	}
	if inFlight == 0 {
		t.Fatal("A's completion cleared B's in-flight flag")
	}
	rel.release(1)
	c.prWG.Wait()
	if info, known, _ := c.lookupPR("/repo"); !known || info.Number != 300 {
		t.Fatalf("after B: %+v known=%v, want 300", info, known)
	}
}

func TestPRProbe_UnknownKeepsOnlyACurrentGenerationAnswer(t *testing.T) {
	c, g := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}}, results: map[string]prinfo.Result{}}
	installFakePR(t, c, f, true)
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()

	f.mu.Lock()
	f.results["A"] = prinfo.Unknown
	f.mu.Unlock()
	c.setPRAt("/repo/.git", time.Time{})
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if info, known, stale := c.lookupPR("/repo"); !known || !stale || info.Number != 277 {
		t.Fatalf("Unknown on the same branch: %+v known=%v stale=%v, want 277 kept and stale", info, known, stale)
	}

	f.mu.Lock()
	f.results["B"] = prinfo.Unknown
	f.mu.Unlock()
	moveBranch(t, c, g, "B")
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if _, known, _ := c.lookupPR("/repo"); known {
		t.Fatal("A's answer crossed a generation into B's Unknown")
	}
}

func TestPRProbe_DetachmentDuringRequestClearsAndDiscards(t *testing.T) {
	c, g := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}}, release: make(chan struct{})}
	installFakePR(t, c, f, true)
	c.refresh(context.Background(), []string{"/repo"}) // A in flight
	waitUntil(t, "A's probe to start", func() bool { return f.count() == 1 })

	moveBranch(t, c, g, "")
	c.refresh(context.Background(), []string{"/repo"})
	close(f.release)
	c.prWG.Wait()

	if _, known, _ := c.lookupPR("/repo"); known {
		t.Fatal("A's answer landed on a detached checkout")
	}
	for _, b := range f.branches() {
		if b == "" {
			t.Fatal("a probe ran for the detached HEAD")
		}
	}
}

func TestPRProbe_AtoBtoAIsANewGeneration(t *testing.T) {
	c, g := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}, "B": {Number: 300}}}
	installFakePR(t, c, f, true)
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	moveBranch(t, c, g, "B")
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	moveBranch(t, c, g, "A")
	rel := make(chan struct{})
	f.mu.Lock()
	f.release = rel
	f.mu.Unlock()
	c.refresh(context.Background(), []string{"/repo"})

	if _, known, _ := c.lookupPR("/repo"); known {
		t.Fatal("back on A, gen 1's number was reused before gen 3 answered")
	}
	waitUntil(t, "gen 3's probe to start", func() bool { return f.count() == 3 })
	if got := f.branches(); got[2] != "A" {
		t.Fatalf("probes = %v, want A, B, A", got)
	}
	close(rel)
	c.prWG.Wait()
	if info, known, _ := c.lookupPR("/repo"); !known || info.Number != 277 {
		t.Fatalf("gen 3: %+v known=%v", info, known)
	}
}

func TestPRProbe_SweptAndRecreatedEntryIgnoresTheOldCompletion(t *testing.T) {
	c, _ := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}}}
	installFakePR(t, c, f, true)
	rel := &perCallRelease{}

	rel.install(f)
	c.refresh(context.Background(), []string{"/repo"}) // old probe in flight
	waitUntil(t, "the old probe to start", func() bool { return f.count() == 1 })
	c.refresh(context.Background(), nil) // sweep: entry gone
	rel.install(f)
	c.refresh(context.Background(), []string{"/repo"}) // new entry, new probe
	waitUntil(t, "the second probe to start", func() bool { return f.count() == 2 })

	rel.release(0) // the OLD completion
	f.waitDone(t)  // ...has now been applied or discarded
	e := c.entry(t, "/repo/.git")
	c.mu.Lock()
	known, inFlight := e.prKnown, e.prInFlightGen
	c.mu.Unlock()
	if known || inFlight == 0 {
		t.Fatalf("old completion touched the recreated entry: known=%v inFlight=%d", known, inFlight)
	}
	rel.release(1)
	c.prWG.Wait()
	if _, known, _ := c.lookupPR("/repo"); !known {
		t.Fatal("the new probe's answer did not land")
	}
}

// --- backoff, change callback, shutdown ---

func TestPRBackoff_Ladder(t *testing.T) {
	want := map[int]time.Duration{
		0: 2 * time.Minute, 1: 4 * time.Minute, 2: 8 * time.Minute,
		3: 16 * time.Minute, 4: 30 * time.Minute, 9: 30 * time.Minute, -1: 2 * time.Minute,
	}
	for failures, d := range want {
		if got := prBackoff(failures); got != d {
			t.Errorf("prBackoff(%d) = %v, want %v", failures, got, d)
		}
	}
}

func TestPRProbe_ConsecutiveUnknownsBackOffAndAnAnswerResets(t *testing.T) {
	c, _ := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}}, results: map[string]prinfo.Result{"A": prinfo.Unknown}}
	installFakePR(t, c, f, true)
	for i := 0; i < 3; i++ {
		c.setPRAt("/repo/.git", time.Time{})
		c.refresh(context.Background(), []string{"/repo"})
		c.prWG.Wait()
	}
	e := c.entry(t, "/repo/.git")
	c.mu.Lock()
	failures := e.prFailures
	c.mu.Unlock()
	if failures != 3 {
		t.Fatalf("prFailures = %d after three Unknowns, want 3", failures)
	}

	c.setPRAt("/repo/.git", time.Now().Add(-15*time.Minute))
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if n := f.count(); n != 3 {
		t.Fatalf("%d probes at 15 min into a 16 min backoff, want 3", n)
	}
	c.setPRAt("/repo/.git", time.Now().Add(-17*time.Minute))
	f.mu.Lock()
	delete(f.results, "A")
	f.mu.Unlock()
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if n := f.count(); n != 4 {
		t.Fatalf("%d probes past the backoff, want 4", n)
	}
	c.mu.Lock()
	failures = e.prFailures
	c.mu.Unlock()
	if failures != 0 {
		t.Fatalf("prFailures = %d after an Answer, want 0", failures)
	}
}

func TestPRProbe_OnChangeFiresOnlyWhenTheTupleMoves(t *testing.T) {
	c, _ := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}}, results: map[string]prinfo.Result{}}
	installFakePR(t, c, f, true)
	var mu sync.Mutex
	fired := 0
	c.onPRChange = func() {
		// Would deadlock if the callback ran under c.mu.
		c.lookupPR("/repo")
		mu.Lock()
		fired++
		mu.Unlock()
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return fired }

	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if count() != 1 {
		t.Fatalf("fired %d after the first answer, want 1", count())
	}
	c.setPRAt("/repo/.git", time.Time{})
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if count() != 1 {
		t.Fatalf("fired %d after an identical answer, want still 1", count())
	}
	f.mu.Lock()
	f.results["A"] = prinfo.Unknown
	f.mu.Unlock()
	c.setPRAt("/repo/.git", time.Time{})
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if count() != 2 {
		t.Fatalf("fired %d after stale flipped, want 2", count())
	}
}

func TestPRProbe_ShutdownCancelsTheProbeContext(t *testing.T) {
	c, _ := prFixture(t, "A")
	f := &fakePR{answers: map[string]prinfo.Info{"A": {Number: 277}}}
	installFakePR(t, c, f, true)
	// A completion after shutdown must not ask the daemon for a broadcast.
	var mu sync.Mutex
	fired := 0
	c.onPRChange = func() { mu.Lock(); fired++; mu.Unlock() }
	// Hold an Answer first, so the cancelled probe's Unknown WOULD flip stale.
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	mu.Lock()
	fired = 0
	mu.Unlock()
	c.setPRAt("/repo/.git", time.Time{})
	f.mu.Lock()
	f.release = make(chan struct{}) // the second probe parks "on the network"
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	c.refresh(ctx, []string{"/repo"})
	waitUntil(t, "the parked probe to start", func() bool { return f.count() == 2 })

	cancel()
	c.prWG.Wait()
	mu.Lock()
	n := fired
	mu.Unlock()
	if n != 0 {
		t.Fatalf("onPRChange fired %d times after shutdown, want 0", n)
	}
	f.mu.Lock()
	probeCtx := f.ctxs[1]
	f.mu.Unlock()
	select {
	case <-probeCtx.Done():
	default:
		t.Fatal("the probe's context outlived the watcher's")
	}
	if n := blockingFSCalls.Load(); n != 0 {
		t.Fatalf("%d permits still held after shutdown", n)
	}
}

// PR probes are the pool's only CONCURRENT holders — every checkout fires
// one at daemon start — and each can hold its permit for the whole 10 s
// timeout while GitHub is unreachable. They therefore claim against a ceiling
// below the pool, so the user's own requests (the git probe, a browse, a
// worktree add) can never be refused because gh is busy not answering.
func TestPRProbe_NeverTakesTheLastPermits(t *testing.T) {
	if maxPRProbePermits >= maxBlockingFSCalls {
		t.Fatalf("maxPRProbePermits = %d, must leave a reserve under maxBlockingFSCalls = %d", maxPRProbePermits, maxBlockingFSCalls)
	}
	c, _ := prFixture(t, "main")
	f := &fakePR{answers: map[string]prinfo.Info{"main": {Number: 277}}}
	installFakePR(t, c, f, true)

	for i := int64(0); i < maxPRProbePermits; i++ {
		if !claimBlockingFSCall() {
			t.Fatal("could not fill the pool to the PR ceiling")
		}
	}
	defer func() {
		for i := int64(0); i < maxPRProbePermits; i++ {
			releaseBlockingFSCall()
		}
	}()
	c.refresh(context.Background(), []string{"/repo"})
	c.prWG.Wait()
	if n := f.count(); n != 0 {
		t.Fatalf("%d PR probes ran with the pool at the PR ceiling, want 0", n)
	}
	// The reserve is still there for everyone else.
	if !claimBlockingFSCall() {
		t.Fatal("a non-PR caller was refused a permit the PR ceiling should have reserved")
	}
	releaseBlockingFSCall()
}

// exec.LookPath per checkout per 5 s tick, forever, is what a missing gh used
// to cost (on Windows: PATH entries × PATHEXT stats). The answer is cached
// for the refresh interval.
func TestPRProbe_AvailabilityIsCheckedOncePerInterval(t *testing.T) {
	c, _ := prFixture(t, "main")
	f := &fakePR{}
	installFakePR(t, c, f, false)
	for i := 0; i < 5; i++ {
		c.refresh(context.Background(), []string{"/repo"})
	}
	c.prWG.Wait()
	f.mu.Lock()
	n := f.availCalls
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("prAvailableFn called %d times over five ticks, want 1", n)
	}
	c.mu.Lock()
	c.prAvailAt = time.Now().Add(-prRefreshInterval - time.Second)
	c.mu.Unlock()
	c.refresh(context.Background(), []string{"/repo"})
	f.mu.Lock()
	n = f.availCalls
	f.mu.Unlock()
	if n != 2 {
		t.Fatalf("prAvailableFn called %d times after the interval elapsed, want 2", n)
	}
}
