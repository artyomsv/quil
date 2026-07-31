package daemon

import (
	"reflect"
	"testing"
	"time"
)

// hangingProbe stands in for a mapped network drive whose server has gone away:
// os.Stat parks in the kernel, the caller's timer fires, and statIsDirWithin
// reports "no answer" while its goroutine keeps the permit.
func hangingProbe(count *int) func(string, time.Duration) (bool, bool) {
	return func(string, time.Duration) (bool, bool) {
		*count++
		return false, false
	}
}

// TestSweepRoots_StopsAfterTooManyUnansweredProbes is the regression guard.
//
// Every abandoned probe keeps a maxBlockingFSCalls permit until the kernel
// returns, and that pool is process-wide and shared with directory listings,
// git discovery and kube discovery. Before the cap, a host with eight dead
// mappings drained the entire pool in ONE sweep, after which every filesystem
// RPC in the daemon was refused until a stalled probe completed — a single press
// of "up" from C:\ denying the whole subsystem.
//
// The deadline here is an hour out on purpose: only the unanswered-probe cap can
// end this sweep, so removing that cap makes this test start 26 probes.
func TestSweepRoots_StopsAfterTooManyUnansweredProbes(t *testing.T) {
	calls := 0

	got, complete := sweepRoots(driveLetters(), time.Now().Add(time.Hour), hangingProbe(&calls))

	if calls > maxRootProbeTimeouts {
		t.Errorf("started %d probes, want at most %d — each one holds a process-wide permit (pool of %d), so a full sweep denies every filesystem RPC daemon-wide",
			calls, maxRootProbeTimeouts, maxBlockingFSCalls)
	}
	if len(got) != 0 {
		t.Errorf("roots = %v, want none — nothing answered", got)
	}
	if complete {
		t.Error("complete = true after abandoning the sweep — the caller would present an empty drive list as the whole truth")
	}
}

// The cap must leave the pool usable by everything else. This states the
// relationship the constant exists to preserve, so lowering maxBlockingFSCalls
// or raising maxRootProbeTimeouts to where one sweep could drain the pool fails
// here rather than in production.
func TestSweepRoots_LeavesPermitsForTheRestOfTheDaemon(t *testing.T) {
	if maxRootProbeTimeouts >= maxBlockingFSCalls {
		t.Fatalf("maxRootProbeTimeouts = %d, maxBlockingFSCalls = %d — one root sweep can drain the whole pool",
			maxRootProbeTimeouts, maxBlockingFSCalls)
	}
}

// TestProbeRoot_CannotTakeThePermitsAReadWillNeed is the bound the per-sweep cap
// cannot provide.
//
// maxRootProbeTimeouts stops ONE sweep from draining the pool, but nothing
// returns an abandoned permit except the kernel finally answering, so successive
// sweeps accumulate: with a cap of k, ⌈maxBlockingFSCalls / k⌉ presses of "up"
// still reach the ceiling, and pressing "up" four times is navigation rather
// than an attack. The reserve is what makes that unreachable — past
// maxRootProbePermits the sweep is refused while the directory read the user
// actually asked for can still claim.
func TestProbeRoot_CannotTakeThePermitsAReadWillNeed(t *testing.T) {
	// The counter is process-wide, so start from a known floor or a straggler
	// from an earlier test decides this one.
	waitForFSCallsDrained(t)

	// Stand in for probes abandoned by earlier sweeps: permits held, never
	// returned, exactly as a goroutine parked in the SMB redirector holds one.
	held := 0
	for blockingFSCalls.Load() < maxRootProbePermits {
		if !claimBlockingFSCall() {
			t.Fatalf("could not reach the reserve; outstanding = %d", blockingFSCalls.Load())
		}
		held++
	}
	t.Cleanup(func() {
		for i := 0; i < held; i++ {
			releaseBlockingFSCall()
		}
	})

	if _, answered := probeRoot(t.TempDir(), time.Second); answered {
		t.Error("a root probe claimed a permit past the reserve — successive sweeps can still drain the pool")
	}

	// The reserve exists FOR this: the read must still go through.
	if !claimBlockingFSCall() {
		t.Error("a directory read was refused while the reserve was intact — the reserve protects nothing")
	} else {
		releaseBlockingFSCall()
	}
}

// The reserve is only meaningful if it leaves something behind. Stated as its
// own check so tuning either constant into uselessness fails here.
func TestRootProbeReserve_LeavesPermitsBehind(t *testing.T) {
	if maxRootProbePermits >= maxBlockingFSCalls {
		t.Fatalf("maxRootProbePermits = %d, maxBlockingFSCalls = %d — the sweep can consume the entire pool",
			maxRootProbePermits, maxBlockingFSCalls)
	}
	if maxRootProbePermits < 1 {
		t.Fatalf("maxRootProbePermits = %d — the sweep can never probe anything", maxRootProbePermits)
	}
}

// A probe that ANSWERS releases its permit immediately and accumulates nothing,
// so answered probes must not count toward the cap. A version that counted every
// probe would stop after maxRootProbeTimeouts drives and hide real ones — the
// failure would look like "my D: drive vanished", with nothing wrong.
func TestSweepRoots_AnsweredProbesNeverCountTowardTheCap(t *testing.T) {
	candidates := driveLetters()
	probe := func(string, time.Duration) (bool, bool) { return true, true }

	got, complete := sweepRoots(candidates, time.Now().Add(time.Hour), probe)

	if !reflect.DeepEqual(got, candidates) {
		t.Errorf("returned %d roots, want all %d — a live drive costs nothing lasting and must never be capped",
			len(got), len(candidates))
	}
	if !complete {
		t.Error("complete = false when every drive answered — a healthy sweep must not warn")
	}
}

// maxRootProbeTimeouts is above 1 precisely so ONE dead mapping does not hide
// every drive that sorts after it. A laptop with a stale H: share must still see
// its local D:.
func TestSweepRoots_OneStuckRootDoesNotAbortTheSweep(t *testing.T) {
	probe := func(root string, _ time.Duration) (bool, bool) {
		if root == `H:\` {
			return false, false
		}
		return true, true
	}

	got, complete := sweepRoots([]string{`C:\`, `H:\`, `D:\`}, time.Now().Add(time.Hour), probe)

	want := []string{`C:\`, `D:\`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roots = %v, want %v — a single stuck drive must not hide the ones behind it", got, want)
	}
	// The sweep reached the end, but H:\ never answered, so the list is missing
	// a drive for a reason the user cannot see. Finishing the loop does not make
	// the answer whole.
	if complete {
		t.Error("complete = true with a drive left unanswered — its absence is indistinguishable from never being mapped")
	}
}

// An expired shared deadline must start nothing at all. A goroutine spawned only
// to be abandoned in the same breath is pure cost: it takes a permit and pins an
// OS thread for a result no one is left to read.
func TestSweepRoots_SpentDeadlineStartsNoProbes(t *testing.T) {
	calls := 0

	got, complete := sweepRoots(driveLetters(), time.Now().Add(-time.Second), hangingProbe(&calls))

	if calls != 0 {
		t.Errorf("started %d probes with the budget already spent, want 0", calls)
	}
	if got != nil {
		t.Errorf("roots = %v, want nil", got)
	}
	if complete {
		t.Error("complete = true having probed nothing at all")
	}
}

// Without the per-probe clamp the first dead mapping spends the entire response
// budget, and the directory the user actually asked for is never read.
func TestSweepRoots_NeverGivesOneProbeMoreThanRootProbeTimeout(t *testing.T) {
	var budgets []time.Duration
	probe := func(_ string, d time.Duration) (bool, bool) {
		budgets = append(budgets, d)
		return true, true
	}

	_, _ = sweepRoots([]string{`C:\`, `D:\`}, time.Now().Add(time.Hour), probe)

	for i, d := range budgets {
		if d > rootProbeTimeout {
			t.Errorf("probe %d got %v, want at most %v", i, d, rootProbeTimeout)
		}
	}
}

// The clamp works the other way too: near the end of the response budget a probe
// must not be handed a full second, because the caller still has to read a
// directory and send a reply within the same deadline.
func TestSweepRoots_ClampsTheProbeBudgetToWhatRemains(t *testing.T) {
	const remaining = 40 * time.Millisecond
	var budgets []time.Duration
	probe := func(_ string, d time.Duration) (bool, bool) {
		budgets = append(budgets, d)
		return true, true
	}

	_, _ = sweepRoots([]string{`C:\`}, time.Now().Add(remaining), probe)

	if len(budgets) != 1 {
		t.Fatalf("probes started = %d, want 1", len(budgets))
	}
	if budgets[0] > remaining {
		t.Errorf("probe budget %v exceeds the %v left on the shared deadline", budgets[0], remaining)
	}
}

// Answering is not enough — an empty optical drive answers and is not a
// directory. Offering it would put a row in the browser that fails on selection,
// which is the reason this sweep stats at all instead of reading the
// GetLogicalDrives bitmask.
func TestSweepRoots_OmitsRootsThatAnswerButAreNotDirectories(t *testing.T) {
	probe := func(root string, _ time.Duration) (bool, bool) {
		return root != `E:\`, true
	}

	got, complete := sweepRoots([]string{`C:\`, `E:\`}, time.Now().Add(time.Hour), probe)

	want := []string{`C:\`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roots = %v, want %v — E:\\ answered but is not listable", got, want)
	}
	// "Answered, and it is not a directory" IS definitive, so the list is
	// complete even though E:\ is absent from it.
	if !complete {
		t.Error("complete = false when every drive answered — a non-directory is an answer, not a gap")
	}
}
