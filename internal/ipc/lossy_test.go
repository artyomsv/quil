package ipc_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// A flood of live-output broadcasts to a client that never reads must NOT close
// that client (output is lossy — frames are dropped, the connection survives).
// Regression test for the production crash where a busy TUI was force-closed
// during a restore output storm.
func TestBroadcast_OutputFloodDoesNotCloseSlowConn(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "output-flood.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	slow, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("slow client: %v", err)
	}
	defer slow.Close()

	waitForConnCount(t, srv, 1, 2*time.Second)

	payload := ipc.PaneOutputPayload{PaneID: "pane-1", Data: make([]byte, 4000)}
	for i := 0; i < 500; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, payload)
		srv.Broadcast(msg)
		time.Sleep(time.Millisecond)
	}

	if got := srv.ConnCount(); got != 1 {
		t.Errorf("slow client closed by output flood: ConnCount=%d, want 1", got)
	}
}
