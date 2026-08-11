package ipc

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// These tests cover the 2026-08-11 production incident: a daemon connection
// whose sendLoop died on the write deadline while its READ side stayed alive.
// The daemon went on accepting requests and queueing must-deliver answers onto
// a queue nobody drained; four minutes later the critical queue hit 64/64 and
// the overflow path finally closed the conn, by which time the user's TUI had
// been silently starved and showed "the local daemon is gone" over a daemon
// that was healthy the whole time.
//
// The shrunken write window is passed PER CONN rather than by reassigning the
// package-level writeDeadline. A sendLoop outlives the test that created its
// conn, so a mutated global is read by goroutines belonging to parallel
// siblings — a data race the detector reports only when the timing lines up,
// which it did here on the first test-race run.

// waitClosed polls until the conn reports closed, or the budget expires.
func waitClosed(c *Conn, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.closed.Load() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return c.closed.Load()
}

// TestWrite_ClosesConnWhenThePeerNeverDrains is the core regression. A peer
// that accepts NOTHING for a whole deadline window is wedged, and the conn must
// be torn down there — not left readable-but-write-dead until the critical
// queue overflows minutes later.
//
// net.Pipe is used rather than a fake net.Conn because it implements REAL
// deadline machinery: SetWriteDeadline genuinely produces os.ErrDeadlineExceeded
// here. A fake returning that error from Write would keep passing even if
// sendLoop stopped calling SetWriteDeadline at all.
func TestWrite_ClosesConnWhenThePeerNeverDrains(t *testing.T) {
	const window = 50 * time.Millisecond

	local, remote := net.Pipe()
	defer remote.Close()

	c := newConnWithWriteWindow(local, window)
	defer c.Close()

	// Nobody ever reads `remote`, so the write parks and consumes zero bytes.
	if err := c.sendFrame([]byte{0, 0, 0, 1, byte('x')}); err != nil {
		t.Fatalf("sendFrame should queue: %v", err)
	}

	if !waitClosed(c, 5*time.Second) {
		t.Error("conn still open after the write deadline expired on a peer that drained nothing — " +
			"sendLoop exited without tearing the conn down, so critCh can never drain again")
	}
}

// TestWrite_ParkedReceiveUnparksWhenTheWriteSideDies pins the seam the daemon's
// cleanup actually hangs off. Server.handleConn only tears a conn down when its
// READ returns an error, and onDisconnect runs from that defer — so a write-side
// death that leaves the reader parked never reaches requestSnapshot or
// RemoveWatchersByConn, whatever else it sets.
func TestWrite_ParkedReceiveUnparksWhenTheWriteSideDies(t *testing.T) {
	const window = 50 * time.Millisecond

	local, remote := net.Pipe()
	defer remote.Close()

	c := newConnWithWriteWindow(local, window)
	defer c.Close()

	readErr := make(chan error, 1)
	go func() {
		_, err := c.Receive()
		readErr <- err
	}()

	if err := c.sendFrame([]byte{0, 0, 0, 1, byte('x')}); err != nil {
		t.Fatalf("sendFrame should queue: %v", err)
	}

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("Receive returned a message; expected the read to fail once the conn was torn down")
		}
	case <-time.After(5 * time.Second):
		t.Error("Receive still parked after the write side died — handleConn's defer never runs, " +
			"so the conn is never removed and onDisconnect never fires")
	}
}

// TestSendBlocking_ReturnsWhenTheWriteSideDies pins the contract SendBlocking
// already documents: "a genuinely wedged peer is still bounded by sendLoop's
// writeDeadline (deadline trips -> conn closes -> done fires -> this returns)".
// That was false — the deadline tripped, sendLoop exited, done never fired, and
// a ghost-replay attach spun on its 2 ms poll until daemon shutdown.
func TestSendBlocking_ReturnsWhenTheWriteSideDies(t *testing.T) {
	const window = 50 * time.Millisecond

	local, remote := net.Pipe()
	defer remote.Close()

	c := newConnWithWriteWindow(local, window)
	defer c.Close()

	// Park sendLoop on a write nobody drains, then fill critCh past
	// sendHeadroom so SendBlocking has to wait for room rather than enqueue.
	frame := []byte{0, 0, 0, 1, byte('x')}
	for i := 0; i < sendHeadroom+1; i++ {
		if err := c.sendFrame(frame); err != nil {
			t.Fatalf("sendFrame %d should queue: %v", i, err)
		}
	}

	msg, err := NewMessage(MsgPaneOutput, PaneOutputPayload{PaneID: "pane-1"})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.SendBlocking(msg, nil) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrConnClosed) {
			t.Errorf("SendBlocking returned %v, want ErrConnClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("SendBlocking never returned after the write side died — it polls every 2ms " +
			"forever, which on the daemon is a ghost-replay attach spinning until shutdown")
	}
}

// TestWrite_SlowPeerKeepsTheConnAndDeliversTheFrameIntact is the other half of
// the policy, and the reason the fix is not a bare close-on-error.
//
// A peer that is DRAINING, just slower than one deadline window, is alive — a
// large state frame into a nearly-full buffer can legitimately exceed the
// window while the peer makes continuous progress. Dropping it there is what
// made the incident unrecoverable. It must keep its conn AND receive the frame
// byte-for-byte: a resume that re-sends already-written bytes corrupts the
// length-prefixed stream, which is why this asserts the payload rather than
// just the conn's survival.
func TestWrite_SlowPeerKeepsTheConnAndDeliversTheFrameIntact(t *testing.T) {
	const window = 25 * time.Millisecond

	local, remote := net.Pipe()
	c := newConnWithWriteWindow(local, window)
	defer c.Close()

	payload := bytes.Repeat([]byte("quil"), 2048) // 8 KiB, many windows at this drip rate
	frame := append([]byte{0, 0, 0x20, 0}, payload...)

	var mu sync.Mutex
	got := make([]byte, 0, len(frame))
	reading := make(chan struct{})
	drained := make(chan struct{})

	go func() {
		defer close(drained)
		close(reading)
		buf := make([]byte, 128)
		for {
			n, err := remote.Read(buf)
			if n > 0 {
				mu.Lock()
				got = append(got, buf[:n]...)
				full := len(got) >= len(frame)
				mu.Unlock()
				if full {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Errorf("slow reader: %v", err)
				}
				return
			}
			time.Sleep(time.Millisecond) // drip: guarantees several deadline windows
		}
	}()

	<-reading
	if err := c.sendFrame(frame); err != nil {
		t.Fatalf("sendFrame should queue: %v", err)
	}

	select {
	case <-drained:
	case <-time.After(15 * time.Second):
		t.Fatal("slow peer never received the whole frame — the writer gave up on a peer that was draining")
	}

	if c.closed.Load() {
		t.Error("conn was closed on a peer that was draining continuously; only zero progress means wedged")
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(got, frame) {
		t.Errorf("frame corrupted across deadline windows: got %d bytes, want %d (resume must continue at frame[n:], never re-send written bytes)",
			len(got), len(frame))
	}
	remote.Close()
}
