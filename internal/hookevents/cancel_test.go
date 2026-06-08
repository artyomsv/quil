package hookevents

import (
	"sync"
	"testing"
	"time"
)

// TestIngester_Cancel_DropsPendingForPane covers the C1 finding: when a
// pane is destroyed, its in-flight coalesce buffer must not fire after
// destroy. Without Cancel the AfterFunc timer would deliver one final
// stale event ~50 ms later through the emit callback.
func TestIngester_Cancel_DropsPendingForPane(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	// Submit one event for the doomed pane; the coalesce timer arms a
	// 50 ms wait before flush.
	ing.Submit(basePayload(1))

	// Cancel immediately — the pending timer must be stopped and the
	// pending entry removed BEFORE the natural 50 ms window closes.
	ing.Cancel("pane-1")

	// Wait past the original coalesce window with a margin. If Cancel
	// didn't stop the timer, the flush would fire and the recorder would
	// see the event.
	time.Sleep(150 * time.Millisecond)

	if got := rec.drain(); len(got) != 0 {
		t.Errorf("Cancel must stop pending coalesce; emit recorder saw %d payloads", len(got))
	}
}

func TestIngester_Cancel_DoesNotAffectOtherPanes(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	a := basePayload(1)
	a.PaneID = "pane-a"
	b := basePayload(2)
	b.PaneID = "pane-b"

	ing.Submit(a)
	ing.Submit(b)
	ing.Cancel("pane-a")

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 1 || got[0].PaneID != "pane-b" {
		t.Errorf("Cancel must not affect other panes; got %d emits %+v, want 1 for pane-b", len(got), got)
	}
}

func TestIngester_Cancel_Idempotent(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	// Cancel for a pane with no pending state must not panic.
	ing.Cancel("nonexistent-pane")

	// Submit + Cancel + Cancel (second Cancel is the test).
	ing.Submit(basePayload(1))
	ing.Cancel("pane-1")
	ing.Cancel("pane-1")

	time.Sleep(150 * time.Millisecond)

	if got := rec.drain(); len(got) != 0 {
		t.Errorf("idempotent Cancel still suppresses pending; recorder saw %d payloads", len(got))
	}
}

// TestIngester_Submit_AfterFlushAll_IsNoOp covers the H1 finding: after
// FlushAll closes the Ingester, a stray late Submit from any goroutine
// must NOT repopulate the pending buffer. Otherwise the AfterFunc timer
// from the new pending entry would fire after the daemon's broadcast
// pipeline has torn down.
func TestIngester_Submit_AfterFlushAll_IsNoOp(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	ing.FlushAll()
	ing.Submit(basePayload(1))

	time.Sleep(150 * time.Millisecond)

	if got := rec.drain(); len(got) != 0 {
		t.Errorf("Submit after FlushAll must be a no-op; recorder saw %d payloads", len(got))
	}
}

// TestIngester_FlushAll_StopsPendingTimers verifies the FlushAll
// invariant that no AfterFunc timer fires after FlushAll returns. Without
// this guarantee a Submit just before FlushAll could enqueue work that
// arrives at emit after the daemon has shut down.
func TestIngester_FlushAll_StopsPendingTimers(t *testing.T) {
	t.Parallel()
	var emitMu sync.Mutex
	var emitCountAfterFlush int
	flushDone := make(chan struct{})

	ing := NewIngester(func(p Payload) {
		emitMu.Lock()
		select {
		case <-flushDone:
			emitCountAfterFlush++
		default:
		}
		emitMu.Unlock()
	})

	// Submit something that would normally fire 50 ms later via AfterFunc.
	ing.Submit(basePayload(1))

	// FlushAll: drains the pending buffer synchronously AND stops the
	// timer. After FlushAll returns, the AfterFunc must not run.
	ing.FlushAll()
	close(flushDone)

	// Wait past the natural coalesce window. If the timer was NOT
	// stopped, we'd see one emit fired here.
	time.Sleep(150 * time.Millisecond)

	emitMu.Lock()
	got := emitCountAfterFlush
	emitMu.Unlock()
	if got != 0 {
		t.Errorf("FlushAll must stop AfterFunc timers; got %d emits after flush returned", got)
	}
}
