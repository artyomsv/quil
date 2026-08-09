package ipc

import (
	"context"
	"net"
	"testing"
	"time"
)

// Client.Send waits for room instead of declaring the peer dead the instant the
// queue is full — but the wait is BOUNDED, and both halves of that matter.
//
// Unbounded, a wedged daemon parks the forwarder for the full 30 s write
// deadline. inputForwardBuffer is 1024 deep and enqueueInput blocks rather than
// drop a keystroke, so a parked forwarder that fills the buffer blocks the
// Update goroutine itself — the whole TUI, not just input. The bound keeps the
// grace period long enough to absorb a momentarily busy daemon (which drains 64
// frames in microseconds) and short enough that a genuinely wedged one is
// reported rather than rendered as a freeze.
func TestClientSend_WedgedPeerFailsWithinTheBound(t *testing.T) {
	t.Parallel()
	prev := clientSendTimeout
	clientSendTimeout = 150 * time.Millisecond
	t.Cleanup(func() { clientSendTimeout = prev })

	local, remote := net.Pipe()
	defer remote.Close() // never read from — the wedged-peer shape

	cl, err := NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return local, nil })
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	defer cl.Close()

	done := make(chan error, 1)
	go func() {
		for {
			msg, err := NewMessage(MsgStateUpdate, map[string]string{"x": "y"})
			if err != nil {
				done <- err
				return
			}
			if err := cl.Send(msg); err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("sender stopped without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never returned against a peer that reads nothing — an " +
			"unbounded wait parks the input forwarder, and a full input buffer " +
			"then blocks the Update goroutine")
	}

	// A must-deliver frame that could not be delivered must not be reported as
	// accepted: giving up has to take the connection down so the loss surfaces
	// as a link error rather than vanishing.
	if !cl.conn.closed.Load() && !cl.conn.overflow.Load() {
		t.Error("Send gave up but left the connection open — the undelivered " +
			"must-deliver frame would be silently lost")
	}
}
