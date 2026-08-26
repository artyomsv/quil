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

// waitSendLoopQuiescent polls until the live count stops moving, and returns it.
//
// The baseline below is a DIFFERENCE, so it is only meaningful if nothing else
// is in flight when it is taken. An earlier test's conn can still be unwinding
// when this one starts — Close is asynchronous on several paths — and a
// baseline captured mid-unwind is unreachable by construction: the departing
// goroutine (-1) cancels the one this test starts (+1), the count never reaches
// baseline+1, and the assertion burns its whole timeout before failing with a
// count that looks impossible.
//
// Observed in CI as "sendLoop did not start: count=2, want 3" on a commit whose
// diff added no goroutines, and not reproducible locally at -count=10 under
// -race, which is the signature of a slower runner giving the previous
// goroutine longer to unwind. Waiting for quiescence rather than sampling once
// removes the assumption instead of widening a timeout around it.
func waitSendLoopQuiescent(timeout time.Duration) int {
	const stableSamples = 3
	deadline := time.Now().Add(timeout)
	last, stable := -1, 0
	for {
		got := sendLoopCount()
		if got == last {
			if stable++; stable >= stableSamples {
				return got
			}
		} else {
			last, stable = got, 1
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
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
	baseline := waitSendLoopQuiescent(5 * time.Second)

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

	// The queued frame's Write fails and sendLoop must exit on its own, without
	// an external Close to prompt it.
	if got := waitSendLoopCount(baseline, 5*time.Second); got != baseline {
		t.Errorf("goroutine leak after sendLoop write-error exit: sendLoop count=%d, want %d", got, baseline)
	}

	// ...and it must take the CONN down with it. This assertion is the whole
	// correction to this test: it previously asserted the exit happened
	// "before Close is ever called" and treated that as the stronger property,
	// which pinned the 2026-08-11 incident as intended behaviour — sendLoop is
	// the only drainer of critCh, so a conn that survives it can never send
	// again and nothing but the enqueue-side overflow will ever notice.
	if !c.closed.Load() {
		t.Error("sendLoop exited on a write error but left the conn open — readable, un-writable, and silent")
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
