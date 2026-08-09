package ipc

import (
	"context"
	"net"
	"testing"
	"time"
)

// The overflow→Close policy in enqueue is a SERVER defense: a daemon
// broadcasting to many clients must be able to disconnect one wedged peer
// rather than let it block the others. A client has no fan-out to protect, and
// applying the same policy to itself means "my outbound queue backed up,
// therefore I should die".
//
// That is what happened on 2026-08-09: the TUI queued more than sendBufSize
// must-deliver frames while the daemon was busy, its own Conn tripped the
// overflow, closed the socket, and the TUI reported a clean exit.
func TestClientSend_BlocksInsteadOfSelfClosingWhenThePeerLags(t *testing.T) {
	t.Parallel()
	local, remote := net.Pipe()
	peer := newConn(remote)
	defer peer.Close()

	cl, err := NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return local, nil })
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	defer cl.Close()

	// net.Pipe is unbuffered, so sendLoop parks on its very first Write and the
	// critical queue fills behind it — the production shape, compressed.
	const n = sendBufSize * 2
	sendErr := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			msg, err := NewMessage(MsgStateUpdate, map[string]int{"i": i})
			if err != nil {
				sendErr <- err
				return
			}
			if err := cl.Send(msg); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- nil
	}()

	// Drain. Every frame must arrive: a must-deliver frame that the caller was
	// told was accepted may not be silently discarded.
	for i := 0; i < n; i++ {
		if err := peer.raw.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		if _, err := peer.Receive(); err != nil {
			t.Fatalf("receive frame %d of %d: %v — the client hung up on itself "+
				"rather than waiting for the queue to drain", i+1, n, err)
		}
	}

	select {
	case err := <-sendErr:
		if err != nil {
			t.Fatalf("Send returned %v — a lagging peer must apply backpressure, "+
				"not terminate the connection", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sender never finished")
	}

	if cl.conn.overflow.Load() {
		t.Error("client conn tripped the slow-peer overflow flag on its own send path")
	}
	if cl.conn.closed.Load() {
		t.Error("client conn closed itself while the peer was merely slow")
	}
}
