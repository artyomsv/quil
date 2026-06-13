package daemon

import (
	"sync"
	"testing"
	"time"

	apty "github.com/artyomsv/quil/internal/pty"
)

// Regression tests for the 2026-06-11/12 daemon wedge: a child process that
// stops reading stdin (claude after compaction) or refuses to die must never
// park an IPC dispatch goroutine or the session write lock. See
// techdebt/1-4-daemon-ipc-wedge-blocking-attach-handler.md.

// wedgedSession is an apty.Session whose Write and Close block until the
// test releases them — modeling a child with a full stdin buffer that also
// can't be reaped promptly.
type wedgedSession struct {
	fakeSession
	release   chan struct{} // closed by the test to unblock Write/Close
	mu        sync.Mutex
	writes    [][]byte
	closeOnce sync.Once
	closed    chan struct{} // closed when Close was CALLED (not finished)
}

func newWedgedSession() *wedgedSession {
	return &wedgedSession{
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (w *wedgedSession) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.writes = append(w.writes, append([]byte(nil), data...))
	w.mu.Unlock()
	<-w.release // block like a full kernel PTY buffer
	return len(data), nil
}

func (w *wedgedSession) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	<-w.release // block like cmd.Wait() on an unreapable child
	return nil
}

func (w *wedgedSession) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

// TestEnqueueInput_NeverBlocksDispatch: with the PTY Write wedged, the
// enqueue path must stay non-blocking — fill the queue, then report drops.
func TestEnqueueInput_NeverBlocksDispatch(t *testing.T) {
	w := newWedgedSession()
	defer close(w.release)

	pane := &Pane{ID: "pane-test", PTY: w}

	done := make(chan struct{})
	var dropped int
	go func() {
		defer close(done)
		// First write parks the writer goroutine; queue absorbs the rest;
		// overflow must return false, not block.
		for i := 0; i < inputQueueSize+10; i++ {
			if !pane.EnqueueInput([]byte{'x'}) {
				dropped++
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueInput blocked the caller — dispatch-goroutine wedge regression")
	}
	if dropped == 0 {
		t.Error("expected overflow drops once the queue filled behind a wedged Write, got none")
	}

	pane.StopInput()
}

// TestInputWriter_DeliversInOrderWhenHealthy: the async pipeline must not
// reorder or lose input for a normally-draining child.
func TestInputWriter_DeliversInOrderWhenHealthy(t *testing.T) {
	w := newWedgedSession()
	close(w.release) // healthy: writes never block

	pane := &Pane{ID: "pane-test", PTY: w}
	for _, s := range []string{"a", "b", "c"} {
		if !pane.EnqueueInput([]byte(s)) {
			t.Fatalf("enqueue %q failed on healthy pane", s)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for w.writeCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("writer delivered %d/3 inputs", w.writeCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.mu.Lock()
	got := string(w.writes[0]) + string(w.writes[1]) + string(w.writes[2])
	w.mu.Unlock()
	if got != "abc" {
		t.Errorf("input reordered: got %q, want %q", got, "abc")
	}
	pane.StopInput()
}

// TestDestroyPane_DoesNotHoldLockDuringClose: destroying a pane whose child
// can't be reaped must release sm.mu promptly — a blocked reader here is
// exactly the snapshot/attach starvation from the production incidents.
func TestDestroyPane_DoesNotHoldLockDuringClose(t *testing.T) {
	w := newWedgedSession()
	defer close(w.release)

	sm := NewSessionManager(4096)
	tab := sm.CreateTab("t")
	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PTY = w

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sm.DestroyPane(pane.ID); err != nil {
			t.Errorf("DestroyPane: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DestroyPane blocked on the wedged PTY Close — sm.mu starvation regression")
	}

	// The lock must be free for readers immediately, while Close is still
	// parked in its own goroutine.
	lockFree := make(chan struct{})
	go func() {
		sm.SnapshotState()
		close(lockFree)
	}()
	select {
	case <-lockFree:
	case <-time.After(5 * time.Second):
		t.Fatal("SnapshotState blocked after DestroyPane — write lock leaked across Close")
	}

	// And the close must actually have been initiated asynchronously.
	select {
	case <-w.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY Close was never called for the destroyed pane")
	}
}

// TestReplacePane_DoesNotHoldLockDuringClose: same invariant for the
// replace path (dead-pane auto-replacement).
func TestReplacePane_DoesNotHoldLockDuringClose(t *testing.T) {
	w := newWedgedSession()
	defer close(w.release)

	sm := NewSessionManager(4096)
	tab := sm.CreateTab("t")
	oldPane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	oldPane.PTY = w
	newPane := sm.NewPane("")

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sm.ReplacePane(oldPane.ID, newPane); err != nil {
			t.Errorf("ReplacePane: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReplacePane blocked on the wedged PTY Close")
	}
	if got := sm.Pane(newPane.ID); got == nil {
		t.Error("new pane not registered after replace")
	}
}

// TestDestroyTab_DoesNotHoldLockDuringClose: same invariant for whole-tab
// teardown with a wedged child inside.
func TestDestroyTab_DoesNotHoldLockDuringClose(t *testing.T) {
	w := newWedgedSession()
	defer close(w.release)

	sm := NewSessionManager(4096)
	tab := sm.CreateTab("t")
	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PTY = w

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sm.DestroyTab(tab.ID); err != nil {
			t.Errorf("DestroyTab: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DestroyTab blocked on the wedged PTY Close")
	}
}

// Compile-time check that wedgedSession satisfies the PTY interface.
var _ apty.Session = (*wedgedSession)(nil)
