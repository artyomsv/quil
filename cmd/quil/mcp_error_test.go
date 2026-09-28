package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// A daemon that answers a request with an error reply (the request type is
// unknown to it, or the payload was bad) must fail the bridge call at once,
// with the daemon's own reason — not leave the caller waiting out the full
// mcpRequestTimeout for a response that will never arrive. The same
// paneInputBridge-style real-server shape used in mcp_paneinput_test.go: a
// fake sender recording the send would pass even if requestWithTimeout never
// looked at the reply's type.
func TestMCPBridge_ErrorReply_BecomesAnErrorNotATimeout(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mcp-err.sock")

	srv := ipc.NewServer(sockPath, func(conn *ipc.Conn, m *ipc.Message) {
		resp, err := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{
			Code:    ipc.ErrCodeUnknownType,
			Message: "unknown message type",
			Type:    m.Type,
		})
		if err != nil {
			return
		}
		resp.ID = m.ID
		conn.Send(resp)
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	client, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	bridge := newMCPBridge(client)
	go bridge.readLoop(context.Background())

	start := time.Now()
	_, err = bridge.requestWithTimeout("some_req", struct{}{}, mcpRequestTimeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("requestWithTimeout returned success for a daemon error reply")
	}
	if !strings.Contains(err.Error(), "unknown message type") {
		t.Errorf("err = %v, want it to carry the daemon's own message", err)
	}
	if elapsed >= mcpRequestTimeout {
		t.Errorf("requestWithTimeout took %v — it waited out the timeout instead of failing on the error reply", elapsed)
	}
}
