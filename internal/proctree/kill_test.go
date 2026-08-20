package proctree

import (
	"errors"
	"testing"
	"time"
)

// fakeProcs is a tiny process world: PIDs with start times, some of which exit
// during the grace period and some of which are then RECYCLED under a new
// process wearing the same number.
type fakeProcs struct {
	// live maps PID -> start time of whatever currently holds that PID.
	live map[int]time.Time
	// exitOnTerm are PIDs that stop when signalled.
	exitOnTerm map[int]bool
	// recycleAfterTerm maps a PID to the start time of the DIFFERENT process
	// that takes over that number during the grace period.
	recycleAfterTerm map[int]time.Time

	termed  []int
	killed  []int
	grace   bool
	refused []int
}

var errGone = errors.New("no such process")

func (f *fakeProcs) ops() KillOps {
	return KillOps{
		Term: func(pid int, start time.Time) error {
			cur, ok := f.live[pid]
			if !ok || !cur.Equal(start) {
				f.refused = append(f.refused, pid)
				return errGone
			}
			f.termed = append(f.termed, pid)
			return nil
		},
		Kill: func(pid int, start time.Time) error {
			cur, ok := f.live[pid]
			if !ok || !cur.Equal(start) {
				f.refused = append(f.refused, pid)
				return errGone
			}
			f.killed = append(f.killed, pid)
			delete(f.live, pid)
			return nil
		},
		Alive: func(pid int, start time.Time) bool {
			cur, ok := f.live[pid]
			return ok && cur.Equal(start)
		},
		Sleep: func(time.Duration) {
			f.grace = true
			// The grace period is where the world changes underneath us.
			for pid := range f.exitOnTerm {
				delete(f.live, pid)
			}
			for pid, newStart := range f.recycleAfterTerm {
				f.live[pid] = newStart
			}
		},
	}
}

func tree() *Node {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
		p(300, 200, "esbuild", 20),
	}
	return Build(table, []int{100})[100]
}

func TestSweep_SignalsChildrenBeforeParents(t *testing.T) {
	f := &fakeProcs{live: map[int]time.Time{100: at(0), 200: at(10), 300: at(20)}}

	res := Sweep(tree(), KillGrace, f.ops())

	if res.Signalled != 3 {
		t.Fatalf("signalled %d, want 3 (%v)", res.Signalled, f.termed)
	}
	pos := map[int]int{}
	for i, pid := range f.termed {
		pos[pid] = i
	}
	// A parent signalled first can exit and reparent its children mid-sweep.
	if pos[300] > pos[200] || pos[200] > pos[100] {
		t.Errorf("term order %v puts a parent before its child", f.termed)
	}
}

// The reviewer's top finding: escalating on PID liveness alone kills whatever
// currently holds the number, which during the grace period may be a completely
// unrelated process.
func TestSweep_DoesNotEscalateOntoRecycledPID(t *testing.T) {
	f := &fakeProcs{
		live:       map[int]time.Time{100: at(0), 200: at(10), 300: at(20)},
		exitOnTerm: map[int]bool{300: true},
		// 300 exits, and something else immediately takes its PID.
		recycleAfterTerm: map[int]time.Time{300: at(999)},
	}

	res := Sweep(tree(), KillGrace, f.ops())

	if !f.grace {
		t.Fatal("grace period never ran")
	}
	for _, pid := range f.killed {
		if pid == 300 {
			t.Error("escalated onto PID 300 after it was recycled — this is a " +
				"wrong-process kill from inside the mechanism that exists to " +
				"prevent wrong-process kills")
		}
	}
	// The two that really survived are still escalated.
	if res.Escalated != 2 {
		t.Errorf("escalated %d, want 2 (%v)", res.Escalated, f.killed)
	}
}

// A process whose identity no longer matches at TERM time is skipped, not
// signalled. Children are re-derived from a fresh tree, but a child can still
// be recycled between that rebuild and its turn in the sweep.
func TestSweep_SkipsProcessRecycledBeforeTerm(t *testing.T) {
	f := &fakeProcs{live: map[int]time.Time{
		100: at(0),
		200: at(10),
		// 300 is now a different process than the tree recorded.
		300: at(999),
	}}

	res := Sweep(tree(), KillGrace, f.ops())

	for _, pid := range f.termed {
		if pid == 300 {
			t.Error("signalled PID 300 whose start time no longer matches the tree")
		}
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", res.Skipped)
	}
	if res.Signalled != 2 {
		t.Errorf("signalled = %d, want 2", res.Signalled)
	}
}

// The same recycled-PID case, but against a platform primitive that CANNOT
// pin identity — a raw kill(2), which is what Darwin has: no pidfd, no handle,
// so verification and signalling are separate steps with a window between them.
//
// This test exists because the one above could not fail. Its fake refuses a
// mismatched Kill on its own, so it covers for a missing guard in Sweep and the
// mutation "escalate on liveness alone" survived the entire suite. With a raw
// killer, Sweep's Alive check is the only thing between a recycled PID and a
// SIGKILL, which is what the check is actually for.
func TestSweep_RawKillerStillNotEscalatedOntoRecycledPID(t *testing.T) {
	f := &fakeProcs{
		live:             map[int]time.Time{100: at(0), 200: at(10), 300: at(20)},
		exitOnTerm:       map[int]bool{300: true},
		recycleAfterTerm: map[int]time.Time{300: at(999)},
	}
	ops := f.ops()
	// A killer that signals whatever holds the number, no questions asked.
	ops.Kill = func(pid int, _ time.Time) error {
		f.killed = append(f.killed, pid)
		delete(f.live, pid)
		return nil
	}

	Sweep(tree(), KillGrace, ops)

	for _, pid := range f.killed {
		if pid == 300 {
			t.Fatal("SIGKILLed a recycled PID: Sweep escalated without " +
				"confirming identity, and the platform primitive could not " +
				"catch it either")
		}
	}
}

func TestSweep_NothingSurvivingMeansNoEscalation(t *testing.T) {
	f := &fakeProcs{
		live:       map[int]time.Time{100: at(0), 200: at(10), 300: at(20)},
		exitOnTerm: map[int]bool{100: true, 200: true, 300: true},
	}

	res := Sweep(tree(), KillGrace, f.ops())

	if res.Escalated != 0 {
		t.Errorf("escalated %d after everything exited gracefully (%v)", res.Escalated, f.killed)
	}
	if res.Signalled != 3 {
		t.Errorf("signalled %d, want 3", res.Signalled)
	}
}

func TestSweep_NilTargetDoesNothing(t *testing.T) {
	f := &fakeProcs{live: map[int]time.Time{}}
	if res := Sweep(nil, KillGrace, f.ops()); res != (SweepResult{}) {
		t.Errorf("nil target produced %+v", res)
	}
}

// If nothing could be signalled at all, the grace period is pointless and the
// sweep must not sit through it.
func TestSweep_NoGraceWhenNothingSignalled(t *testing.T) {
	f := &fakeProcs{live: map[int]time.Time{}} // every Term refuses

	res := Sweep(tree(), KillGrace, f.ops())

	if f.grace {
		t.Error("waited out the grace period having signalled nothing")
	}
	if res.Skipped != 3 || res.Signalled != 0 {
		t.Errorf("result = %+v, want 3 skipped and 0 signalled", res)
	}
}

func TestSweep_SubtreeOnlyNotWholeTree(t *testing.T) {
	// Killing the middle node must not touch its parent.
	root := tree()
	target := Find(root, 200)

	f := &fakeProcs{live: map[int]time.Time{100: at(0), 200: at(10), 300: at(20)}}
	Sweep(target, KillGrace, f.ops())

	for _, pid := range f.termed {
		if pid == 100 {
			t.Error("killing a descendant signalled the pane's own shell")
		}
	}
	if len(f.termed) != 2 {
		t.Errorf("termed %v, want exactly 200 and 300", f.termed)
	}
}

// A node whose own start time is unreadable has an unverifiable link to the
// pane's tree. Build keeps the link (dropping it would hide a real child), but
// the sweep must not treat "visible" as "killable" — and it must not treat the
// node's CHILDREN as killable either, which is the half that was missing: they
// have perfectly readable start times, so every per-node identity check passed
// and they were terminated on the strength of an unconfirmed ancestry.
func TestSweep_SkipsDescendantsOfAnUnverifiableNode(t *testing.T) {
	table := []ProcessEntry{
		p(100, 1, "zsh", 0),
		p(200, 100, "node", 10),
		// 300's start could not be read — the Windows second-pass failure.
		{PID: 300, PPID: 200, Name: "unverifiable"},
		// ...but ITS child is perfectly readable.
		p(400, 300, "someone-elses-process", 30),
	}
	root := Build(table, []int{100})[100]
	if Find(root, 400) == nil {
		t.Fatal("fixture: 400 should be present in the tree")
	}

	f := &fakeProcs{live: map[int]time.Time{
		100: at(0), 200: at(10), 400: at(30),
	}}

	Sweep(root, KillGrace, f.ops())

	for _, pid := range f.termed {
		if pid == 400 {
			t.Error("terminated a process whose ancestry passes through a node " +
				"with no confirmable identity; its membership in this pane's " +
				"tree was never established")
		}
		if pid == 300 {
			t.Error("terminated the unverifiable node itself")
		}
	}
	// The verifiable part of the subtree is still killed.
	got := map[int]bool{}
	for _, pid := range f.termed {
		got[pid] = true
	}
	if !got[100] || !got[200] {
		t.Errorf("termed %v, want 100 and 200 — refusing an unverifiable branch "+
			"must not abandon the rest of the sweep", f.termed)
	}
}
