package ipc

import (
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// sendLoopCount returns the number of live (*Conn).sendLoop goroutines by
// scanning a full goroutine stack dump. Counting only this function makes
// the leak check immune to unrelated goroutine churn from the test runner
// and sibling tests — the global runtime.NumGoroutine() delta this test
// previously asserted on was flaky under parallel execution (it blocked a
// release run and a PR CI run before being replaced).
func sendLoopCount() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "(*Conn).sendLoop(")
}

// waitSendLoopCount polls until the live sendLoop count equals want or the
// deadline passes; returns the final observed count.
func waitSendLoopCount(want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		got := sendLoopCount()
		if got == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSendLoop_ExitsOnWriteError verifies that when the underlying socket
// Write fails (peer disconnected mid-send, kernel detected RST, etc.),
// sendLoop terminates cleanly without leaking the goroutine. The CR finding
// flagged this as the only fully-untested cleanup path in the broadcast
// hardening.
//
// Uses net.Pipe so we can deterministically force a write error by closing
// the remote end mid-send.
//
// Deliberately NOT t.Parallel: the assertion scans live goroutines for
// sendLoop frames, and concurrent sibling tests creating conns would race
// the count. Sequential tests run while parallel ones are still parked, so
// the count is stable here, and the poll-until-deadline makes the check
// timing-independent.
func TestSendLoop_ExitsOnWriteError(t *testing.T) {
	baseline := sendLoopCount()

	local, remote := net.Pipe()
	c := newConn(local)
	if got := waitSendLoopCount(baseline+1, 5*time.Second); got != baseline+1 {
		t.Fatalf("sendLoop did not start: count=%d, want %d", got, baseline+1)
	}

	// Close the remote half BEFORE Send. Any subsequent Write on `local`
	// returns io.ErrClosedPipe, exercising the sendLoop write-error exit.
	remote.Close()

	frame := []byte{0, 0, 0, 1, byte('x')}
	if err := c.sendFrame(frame); err != nil {
		t.Fatalf("sendFrame should queue even before the write fails; got %v", err)
	}

	// The queued frame's Write fails and sendLoop must exit on its own —
	// before Close is ever called. This is a stronger assertion than the
	// original (which only checked the count after Close).
	if got := waitSendLoopCount(baseline, 5*time.Second); got != baseline {
		t.Errorf("goroutine leak after sendLoop write-error exit: sendLoop count=%d, want %d", got, baseline)
	}

	_ = c.Close()
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
