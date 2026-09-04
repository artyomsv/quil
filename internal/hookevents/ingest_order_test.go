package hookevents

import (
	"strings"
	"testing"
	"time"
)

// orderedPayload builds a payload whose coalesce key is distinct per hookEvent.
func orderedPayload(hookEvent string, seq uint64) Payload {
	p := basePayload(seq)
	p.HookEvent = hookEvent
	return p
}

// eventNames renders an emit sequence for assertions and failure messages.
func eventNames(ps []Payload) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.HookEvent)
	}
	return strings.Join(names, ",")
}

// waitEmits polls the recorder until n emits have landed or the deadline
// passes, then drains it. Polling rather than a fixed sleep: a slow CI run
// would otherwise report an EMPTY sequence, which reads as a reordering bug
// rather than a timeout, and the fast path returns as soon as the 50 ms window
// closes instead of sleeping past it.
func waitEmits(rec *emitRecorder, n int, deadline time.Duration) []Payload {
	stop := time.Now().Add(deadline)
	for {
		rec.mu.Lock()
		have := len(rec.events)
		rec.mu.Unlock()
		if have >= n || time.Now().After(stop) {
			return rec.drain()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const emitDeadline = 2 * time.Second

// The daemon's spool watcher submits every line one 200 ms tick read, in file
// order. Two events for one pane in the same tick have DIFFERENT keys almost
// always (a start edge and a stop edge, a heartbeat and the permission prompt
// it precedes), so they must reach the consumer in the order they were
// written: the TUI's work state is a replay of edges, and a stop applied before
// the start it belongs to leaves the pane lit until session end.
//
// Measured before the fix: 300 of 300 runs on Windows and 299 of 300 on Linux
// emitted the SECOND submission first. Each coalesce key armed its own
// time.AfterFunc, both expired in the same timer pass, and Go schedules the
// most recently created goroutine first.
func TestIngester_Submit_DistinctKeysEmitInArrivalOrder(t *testing.T) {
	t.Parallel()
	for run := 0; run < 20; run++ {
		rec := &emitRecorder{}
		ing := NewIngester(rec.emit)

		ing.Submit(orderedPayload("SubagentStart", 1))
		ing.Submit(orderedPayload("SubagentStop", 2))

		got := waitEmits(rec, 2, emitDeadline)
		if want := "SubagentStart,SubagentStop"; eventNames(got) != want {
			t.Fatalf("run %d: emit order = %s, want %s (arrival order)", run, eventNames(got), want)
		}
	}
}

// Three in one tick: with per-key timers the LAST one jumped to the front
// (observed C,A,B in 94 of 100 runs). Arrival order must hold for any count.
func TestIngester_Submit_ThreeKeysEmitInArrivalOrder(t *testing.T) {
	t.Parallel()
	for run := 0; run < 10; run++ {
		rec := &emitRecorder{}
		ing := NewIngester(rec.emit)

		for _, ev := range []string{"UserPromptSubmit", "PreToolUse", "Stop"} {
			ing.Submit(orderedPayload(ev, 1))
		}

		got := waitEmits(rec, 3, emitDeadline)
		if want := "UserPromptSubmit,PreToolUse,Stop"; eventNames(got) != want {
			t.Fatalf("run %d: emit order = %s, want %s", run, eventNames(got), want)
		}
	}
}

// A key that coalesces keeps the slot its FIRST arrival opened: a burst
// A, B, A' emits A (with the burst count) before B, because A's window opened
// first. Last-wins applies to the payload inside the slot, not to its place in
// line. Looped like its siblings: a single run caught the per-key mutation in
// only 29 of 30 tries.
func TestIngester_Submit_CoalescedKeyKeepsItsArrivalSlot(t *testing.T) {
	t.Parallel()
	for run := 0; run < 10; run++ {
		rec := &emitRecorder{}
		ing := NewIngester(rec.emit)

		ing.Submit(orderedPayload("SubagentStart", 1))
		ing.Submit(orderedPayload("Stop", 2))
		ing.Submit(orderedPayload("SubagentStart", 3))

		got := waitEmits(rec, 2, emitDeadline)
		if want := "SubagentStart,Stop"; eventNames(got) != want {
			t.Fatalf("run %d: emit order = %s, want %s", run, eventNames(got), want)
		}
		if got[0].Seq != 3 || got[0].Data["coalesced"] != "2" {
			t.Fatalf("run %d: coalesced slot: got Seq=%d coalesced=%q, want the newest payload (Seq 3) with burst count 2", run, got[0].Seq, got[0].Data["coalesced"])
		}
	}
}

// Shutdown drains in arrival order too. Sorting by key looked deterministic
// and was: deterministically wrong for a Stop that arrived after a
// UserPromptSubmit but sorts before it.
func TestIngester_FlushAll_EmitsInArrivalOrder(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	ing.Submit(orderedPayload("UserPromptSubmit", 1))
	ing.Submit(orderedPayload("PreToolUse", 2))
	ing.Submit(orderedPayload("Stop", 3))
	ing.FlushAll()

	got := rec.drain()
	if want := "UserPromptSubmit,PreToolUse,Stop"; eventNames(got) != want {
		t.Fatalf("FlushAll emit order = %s, want %s", eventNames(got), want)
	}
}

// Cancel must take a pane's keys out of the arrival queue, not only out of
// pending. A stale slot at the front would let a key resubmitted for the same
// pane jump ahead of everything that arrived in between — and drainDue's
// defensive skip of a missing pending entry hides that from every other test
// (a mutation that left `order` untouched stayed green on the full suite).
func TestIngester_Cancel_ResubmittedKeyTakesAFreshSlot(t *testing.T) {
	t.Parallel()
	for run := 0; run < 20; run++ {
		rec := &emitRecorder{}
		ing := NewIngester(rec.emit)

		ing.Submit(orderedPayload("SubagentStart", 1))
		ing.Cancel("pane-1")
		ing.Submit(orderedPayload("Stop", 2))
		ing.Submit(orderedPayload("SubagentStart", 3))

		got := waitEmits(rec, 2, emitDeadline)
		if want := "Stop,SubagentStart"; eventNames(got) != want {
			t.Fatalf("run %d: emit order = %s, want %s (post-cancel arrival order)", run, eventNames(got), want)
		}
	}
}

// Cancel can uncover a key that is already due. drainDue is driven only by a
// key's own timer, and that timer has already run for a key marked due behind
// a head that was not — so when Cancel removes the head, nothing else will
// ever release the key behind it. Demonstrated on a daemon that then went
// quiet: the event sat until an unrelated pane's timer fired. Cancel must
// drive the drain itself.
//
// White-box on purpose: the stall needs the LATER key's timer to have run
// first, which the scheduler decides. The test stops the real timers and runs
// the later key's flush by hand, which is exactly the state the race produces.
func TestIngester_Cancel_ReleasesTheDueKeyItUncovers(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	head := orderedPayload("Stop", 1)
	head.PaneID = "pane-a"
	behind := orderedPayload("SubagentStop", 2)
	behind.PaneID = "pane-b"
	ing.Submit(head)
	ing.Submit(behind)

	ing.mu.Lock()
	for _, p := range ing.pending {
		p.timer.Stop()
	}
	ing.mu.Unlock()
	ing.flush(coalesceKey("pane-b", "SubagentStop", ""))
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("a due key must wait behind a head that is not; emitted %s", eventNames(got))
	}

	ing.Cancel("pane-a")

	got := waitEmits(rec, 1, emitDeadline)
	if want := "SubagentStop"; eventNames(got) != want {
		t.Fatalf("after the head is cancelled the due key behind it must be released; emitted %q, want %q", eventNames(got), want)
	}
}

// Submit checks `closed`, drops the lock, and only then coalesces. FlushAll can
// run entirely in that gap: it sets closed, drains, returns — and the late
// coalesce then repopulates pending and arms a timer that fires into a
// pipeline the daemon has already torn down, breaking FlushAll's own promise
// that no emit follows it. The second half must re-check under the lock.
func TestIngester_Submit_RacingFlushAllIsDropped(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	ing.FlushAll()
	// The race: Submit's closed check already passed before FlushAll ran.
	ing.coalesce(orderedPayload("Stop", 1))

	ing.mu.Lock()
	pending, queued := len(ing.pending), len(ing.order)
	ing.mu.Unlock()
	if pending != 0 || queued != 0 {
		t.Fatalf("a submit racing FlushAll must be dropped; pending=%d queued=%d", pending, queued)
	}
	if got := waitEmits(rec, 1, 150*time.Millisecond); len(got) != 0 {
		t.Fatalf("nothing may emit after FlushAll returned; got %s", eventNames(got))
	}
}
