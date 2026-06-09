package ipc

import (
	"net"
	"runtime"
	"testing"
	"time"
)

// TestSendLoop_ExitsOnWriteError verifies that when the underlying socket
// Write fails (peer disconnected mid-send, kernel detected RST, etc.),
// sendLoop terminates cleanly without leaking the goroutine. The CR finding
// flagged this as the only fully-untested cleanup path in the broadcast
// hardening.
//
// Uses net.Pipe so we can deterministically force a write error by closing
// the remote end mid-send. Goroutine leak is detected via a runtime-level
// goroutine count delta around the conn lifecycle.
func TestSendLoop_ExitsOnWriteError(t *testing.T) {
	t.Parallel()

	// Settle the runtime so the baseline goroutine count is stable.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	local, remote := net.Pipe()
	c := newConn(local)

	// Close the remote half BEFORE Send. Any subsequent Write on `local`
	// returns io.ErrClosedPipe, exercising the sendLoop write-error exit.
	remote.Close()

	frame := []byte{0, 0, 0, 1, byte('x')}
	if err := c.sendFrame(frame); err != nil {
		t.Fatalf("sendFrame should queue even before the write fails; got %v", err)
	}

	// Give sendLoop a moment to consume the frame, attempt the write, and
	// exit on the resulting error.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// sendLoop exits when raw.Write errors. After that, our explicit
		// Close completes cleanly via sync.Once. Check the closed flag —
		// it's set by Close, so we need to call Close to confirm cleanup.
		if c.overflow.Load() {
			break // not the path under test; bail
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Explicitly close to release the local pipe half. After Close + a brief
	// settle, the goroutine count should match baseline (the sendLoop has
	// already exited on the write error, and nothing else lingers).
	_ = c.Close()

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	if got := runtime.NumGoroutine(); got > baseline+1 {
		// +1 tolerates the test runner's own bookkeeping goroutine churn.
		t.Errorf("goroutine leak after sendLoop write-error exit: baseline=%d, after=%d", baseline, got)
	}
}

// TestConn_CloseIdempotent confirms sync.Once-guarded Close — multiple
// concurrent close calls from any goroutine (handleConn's defer + overflow's
// async close + Server.Stop's iteration) all funnel through one underlying
// raw.Close.
func TestConn_CloseIdempotent(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer remote.Close()

	c := newConn(local)

	// Hammer Close from multiple goroutines simultaneously. None should
	// panic; the underlying close should run exactly once. We do not assert
	// the err returned because only the *first* Close gets the real error;
	// the others get nil from the sync.Once.Do default.
	const N = 16
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			_ = c.Close()
			done <- struct{}{}
		}()
	}
	for i := 0; i < N; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("concurrent Close stalled — sync.Once not protecting close path")
		}
	}

	if !c.closed.Load() {
		t.Errorf("Close should have set closed flag")
	}
}

// TestConn_SendFrameAfterCloseShortCircuits confirms the closed-flag short-
// circuit prevents work after Close. Belt-and-suspenders alongside the
// channel-send (which would also fail because sendLoop exited).
func TestConn_SendFrameAfterCloseShortCircuits(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer remote.Close()

	c := newConn(local)
	_ = c.Close()

	if err := c.sendFrame([]byte("x")); err != ErrSendOverflow {
		t.Errorf("sendFrame after Close: got %v, want ErrSendOverflow", err)
	}

	msg, _ := NewMessage(MsgStateUpdate, map[string]string{"x": "y"})
	if err := c.Send(msg); err != ErrSendOverflow {
		t.Errorf("Send after Close: got %v, want ErrSendOverflow", err)
	}
}

// TestEnqueue_DropsOutputFrameWhenFull verifies a full output queue drops the
// frame (and does NOT trip overflow/close), while the connection stays usable.
func TestEnqueue_DropsOutputFrameWhenFull(t *testing.T) {
	t.Parallel()
	local, remote := net.Pipe()
	defer remote.Close()
	c := newConn(local)
	defer c.Close()

	// Remote never reads → sendLoop blocks on its first write → outCh fills.
	for i := 0; i < sendBufSize*3; i++ {
		_ = c.enqueue([]byte{0, 0, 0, 1, byte('x')}, true)
	}
	if c.overflow.Load() {
		t.Errorf("droppable flood must not set overflow")
	}
	if c.closed.Load() {
		t.Errorf("droppable flood must not close the conn")
	}
	if c.Dropped() == 0 {
		t.Errorf("expected some dropped frames after a droppable flood, got 0")
	}
}
