package ipc

import (
	"net"
	"testing"
	"time"
)

// TestConn_FlushWaitsForQueuedFrames pins the contract that makes a clean
// shutdown possible: Send is non-blocking, so an empty critCh does not mean the
// peer has the frame. Close stops sendLoop without writing what is left, so a
// caller that closes straight after Send discards frames it was told were
// accepted. Flush is what closes that gap.
func TestConn_FlushWaitsForQueuedFrames(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })

	c := newConn(client)
	t.Cleanup(func() { c.Close() })

	const frames = 5
	for i := 0; i < frames; i++ {
		msg, err := NewMessage(MsgPaneInput, PaneInputPayload{PaneID: "p1", Data: []byte{byte('0' + i)}})
		if err != nil {
			t.Fatalf("NewMessage: %v", err)
		}
		if err := c.Send(msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	// net.Pipe is unbuffered: nothing is written until the peer reads, so the
	// frames are provably still pending here.
	if got := c.pending.Load(); got == 0 {
		t.Fatal("no frames pending — the test cannot distinguish a real flush from a no-op")
	}

	read := make(chan struct{})
	go func() {
		defer close(read)
		buf := make([]byte, 4096)
		for i := 0; i < frames; i++ {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	if !c.Flush(2 * time.Second) {
		t.Fatalf("Flush reported the queue undrained; %d frames still pending", c.pending.Load())
	}
	if got := c.pending.Load(); got != 0 {
		t.Errorf("pending after Flush = %d, want 0", got)
	}
	<-read
}

// TestConn_FlushOnClosedConnReturnsImmediately: sendLoop is gone once the conn
// is closed, so nothing further will ever be written. Waiting could only burn
// the whole timeout on the exit path.
func TestConn_FlushOnClosedConnReturnsImmediately(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() { server.Close() })

	c := newConn(client)
	msg, err := NewMessage(MsgPaneInput, PaneInputPayload{PaneID: "p1", Data: []byte("x")})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if err := c.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	c.Close()

	start := time.Now()
	if c.Flush(5 * time.Second) {
		t.Error("Flush reported success on a closed conn with a frame still pending")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Flush on a closed conn took %s — it must not wait out the timeout", elapsed)
	}
}
