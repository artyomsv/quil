package ipc

import (
	"context"
	"net"
	"testing"
	"time"
)

// Flush counts queued must-deliver frames through Conn.pending, and every path
// that puts a frame on critCh has to maintain it. enqueue does; SendBlocking
// did not — sendLoop decrements per frame written, so a connection whose sends
// go through SendBlocking drives the counter NEGATIVE and Flush's
// `for pending > 0` exits on its first check, reporting success without having
// waited for anything.
//
// That is invisible while SendBlocking is only used for ghost replay, and
// becomes the TUI exit path the moment Client.Send routes through it:
// closeClient flushes before Close precisely so the user's last keystrokes
// reach the socket, and a Flush that always returns true discards them.
func TestFlush_ClientSendsAreCounted(t *testing.T) {
	t.Parallel()
	local, remote := net.Pipe()
	defer remote.Close() // never read — frames stay queued

	cl, err := NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return local, nil })
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	defer cl.Close()

	// One send is enough: net.Pipe is unbuffered, so sendLoop parks on the
	// write and the frame is still outstanding.
	msg, err := NewMessage(MsgStateUpdate, map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if err := cl.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := cl.conn.pending.Load(); got < 1 {
		t.Errorf("pending = %d after one client Send, want >= 1 — sendLoop "+
			"decrements per frame written, so an uncounted enqueue drives this "+
			"negative and Flush stops waiting for anything", got)
	}

	if cl.Flush(100 * time.Millisecond) {
		t.Error("Flush reported success while a frame was still queued against " +
			"a peer that reads nothing — closeClient would Close here and " +
			"discard the frames the caller was told were accepted")
	}
}
