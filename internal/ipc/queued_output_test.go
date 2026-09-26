package ipc

import (
	"io"
	"net"
	"testing"
	"time"
)

func waitForConn(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// QueuedOutput counts live frames waiting in the droppable queue — the ones a
// must-deliver frame queued now would overtake — and reaches zero once the
// peer reads. Done closes when the conn dies.
func TestConn_QueuedOutputCountsWaitingLiveFramesAndDoneCloses(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { remote.Close() })
	c := newConn(local)

	out, err := NewMessage(MsgPaneOutput, PaneOutputPayload{PaneID: "p", Data: []byte("x")})
	if err != nil {
		t.Fatalf("build pane_output: %v", err)
	}
	frame, err := EncodeFrame(out)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Nobody reads the pipe: sendLoop takes the first frame and blocks in
	// its write, so the other two wait in the queue.
	for i := 0; i < 3; i++ {
		if err := c.enqueue(frame, true); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	waitForConn(t, "two frames waiting", func() bool { return c.QueuedOutput() == 2 })

	go func() { _, _ = io.Copy(io.Discard, remote) }()
	waitForConn(t, "the queue to drain", func() bool { return c.QueuedOutput() == 0 })

	select {
	case <-c.Done():
		t.Fatal("Done closed on a live conn")
	default:
	}
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done not closed after Close")
	}
}
