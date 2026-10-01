package ipc_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// WaitConns covers the disconnect callback, not only the read loop: Stop
// closes the conns, and a caller that then tears down what those callbacks
// write to must be able to wait for them.
func TestWaitConns_WaitsForTheDisconnectCallback(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "waitconns.sock")
	inCallback := make(chan struct{})
	release := make(chan struct{})
	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, func(*ipc.Conn) {
		close(inCallback)
		<-release
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	c, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.Close()
	waitForConnCount(t, srv, 1, 2*time.Second)

	srv.Stop()
	select {
	case <-inCallback:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop never ran the conn's disconnect callback")
	}
	if srv.WaitConns(50 * time.Millisecond) {
		t.Fatal("WaitConns returned true while a disconnect callback was still running")
	}
	close(release)
	if !srv.WaitConns(3 * time.Second) {
		t.Fatal("WaitConns timed out after the callback returned")
	}
}
