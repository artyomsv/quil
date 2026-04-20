package daemon_test

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestDaemon_MemoryReportRoundTrip boots a daemon on a temp socket, sends
// a memory_report request, and asserts the response is well-formed.
func TestDaemon_MemoryReportRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	cfg := config.Default()
	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { d.Stop() })

	sockPath := filepath.Join(tmp, "quild.sock")

	// Wait until socket is connectable.
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("socket %s never became connectable", sockPath)
	}
	defer conn.Close()

	// Let the collector run at least once (its synchronous first collect
	// fires inside Run() immediately).
	time.Sleep(200 * time.Millisecond)

	payload, err := json.Marshal(ipc.MemoryReportReqPayload{})
	if err != nil {
		t.Fatal(err)
	}
	req := &ipc.Message{Type: ipc.MsgMemoryReportReq, ID: "t1", Payload: payload}
	if err := ipc.WriteMessage(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := ipc.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.Type != ipc.MsgMemoryReportResp {
		t.Fatalf("resp type = %s, want %s", resp.Type, ipc.MsgMemoryReportResp)
	}
	if resp.ID != "t1" {
		t.Errorf("resp id = %s, want t1", resp.ID)
	}
	var out ipc.MemoryReportRespPayload
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SnapshotAt == 0 {
		t.Errorf("SnapshotAt = 0, want non-zero")
	}
}
