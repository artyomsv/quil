package ipc

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// ghostFrameMsg builds a MsgPaneOutput ghost frame (the message shape ghost
// replay sends) and returns it together with its marshaled wire length so
// tests can compute expected byte totals on the reader side.
func ghostFrameMsg(t *testing.T) (*Message, int) {
	t.Helper()
	msg, err := NewMessage(MsgPaneOutput, PaneOutputPayload{
		PaneID: "pane-test",
		Data:   bytes.Repeat([]byte{'g'}, 1024),
		Ghost:  true,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	return msg, buf.Len()
}

// TestConn_SendBlocking_SlowReader_DeliversAllWithoutOverflow is the core
// regression test for the production attach kick-loop: a peer that drains
// slowly (a TUI busy applying workspace state) must receive EVERY frame of a
// bulk transfer that is several times larger than the critical queue, and the
// conn must never trip the overflow close.
func TestConn_SendBlocking_SlowReader_DeliversAllWithoutOverflow(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer remote.Close()
	c := newConn(local)
	defer c.Close()

	msg, frameLen := ghostFrameMsg(t)
	const n = sendBufSize * 3 // 3x queue capacity — the prod failure shape

	got := make(chan int, 1)
	go func() {
		total := 0
		buf := make([]byte, 2048)
		for total < n*frameLen {
			nr, err := remote.Read(buf)
			total += nr
			if err != nil {
				break
			}
			time.Sleep(200 * time.Microsecond) // simulate a busy event loop
		}
		got <- total
	}()

	for i := 0; i < n; i++ {
		if err := c.SendBlocking(msg, nil); err != nil {
			t.Fatalf("SendBlocking frame %d/%d: %v", i, n, err)
		}
	}

	select {
	case total := <-got:
		if total != n*frameLen {
			t.Errorf("reader received %d bytes, want %d", total, n*frameLen)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reader never received the full transfer")
	}

	if c.overflow.Load() {
		t.Error("SendBlocking must never trip the overflow close")
	}
	if c.closed.Load() {
		t.Error("conn must survive a slow-but-alive reader")
	}
}

// TestConn_SendBlocking_WedgedPeer_AbortsOnClose verifies a SendBlocking
// stuck behind a peer that never reads returns promptly (with an error)
// when the conn is closed, instead of hanging forever.
func TestConn_SendBlocking_WedgedPeer_AbortsOnClose(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer remote.Close()
	c := newConn(local)

	msg, _ := ghostFrameMsg(t)
	errCh := make(chan error, 1)
	go func() {
		for {
			if err := c.SendBlocking(msg, nil); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Let the sender saturate: sendLoop blocks on the unread pipe, the
	// queue fills to the blocking threshold, SendBlocking parks.
	time.Sleep(100 * time.Millisecond)
	_ = c.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("SendBlocking after Close: got nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendBlocking did not abort after Close")
	}
}

// TestConn_SendBlocking_WedgedPeer_AbortsOnCancel verifies the caller-supplied
// cancel channel (daemon shutdown) unblocks a parked SendBlocking without
// closing the conn.
func TestConn_SendBlocking_WedgedPeer_AbortsOnCancel(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer remote.Close()
	c := newConn(local)
	defer c.Close()

	msg, _ := ghostFrameMsg(t)
	cancel := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		for {
			if err := c.SendBlocking(msg, cancel); err != nil {
				errCh <- err
				return
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(cancel)

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("SendBlocking after cancel: got nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendBlocking did not abort after cancel")
	}

	if c.closed.Load() {
		t.Error("cancel must not close the conn")
	}
	if c.overflow.Load() {
		t.Error("cancel must not trip overflow")
	}
}

// TestConn_SendBlocking_LeavesBroadcastHeadroom pins the headroom contract:
// a saturated bulk sender must leave at least half the critical queue free so
// concurrent Broadcast criticals (state updates, pane events) never hit a
// replay-full queue and kill the client mid-attach.
func TestConn_SendBlocking_LeavesBroadcastHeadroom(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer remote.Close()
	c := newConn(local)

	msg, _ := ghostFrameMsg(t)
	errCh := make(chan error, 1)
	go func() {
		for {
			if err := c.SendBlocking(msg, nil); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Peer never reads: sendLoop wedges on its first write, the bulk sender
	// saturates up to its threshold and parks.
	time.Sleep(150 * time.Millisecond)

	if depth := len(c.critCh); depth > sendBufSize/2 {
		t.Errorf("saturated SendBlocking queued %d critical frames, want <= %d (headroom for broadcasts)",
			depth, sendBufSize/2)
	}

	// The broadcast path (non-blocking critical enqueue) must still succeed.
	frame := []byte{0, 0, 0, 1, 'x'}
	for i := 0; i < 8; i++ {
		if err := c.sendFrame(frame); err != nil {
			t.Fatalf("broadcast-path sendFrame %d failed during bulk replay: %v", i, err)
		}
	}
	if c.overflow.Load() {
		t.Error("broadcast during saturated bulk replay tripped overflow")
	}

	_ = c.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bulk sender goroutine did not exit after Close")
	}
}
