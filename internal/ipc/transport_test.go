package ipc_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestServerClientRoundTrip(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	var received *ipc.Message
	var mu sync.Mutex
	done := make(chan struct{})

	handler := func(conn *ipc.Conn, msg *ipc.Message) {
		mu.Lock()
		received = msg
		mu.Unlock()

		resp, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
			PaneID: "p1",
			Data:   []byte("hello back"),
		})
		conn.Send(resp)
		close(done)
	}

	srv := ipc.NewServer(sockPath, handler, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	client, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer client.Close()

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
		PaneID: "p1",
		Data:   []byte("ls\n"),
	})
	if err := client.Send(msg); err != nil {
		t.Fatalf("client send: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server to receive message")
	}

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("server never received message")
	}
	if received.Type != ipc.MsgPaneInput {
		t.Errorf("type: got %q, want %q", received.Type, ipc.MsgPaneInput)
	}

	resp, err := client.Receive()
	if err != nil {
		t.Fatalf("client receive: %v", err)
	}
	if resp.Type != ipc.MsgPaneOutput {
		t.Errorf("response type: got %q, want %q", resp.Type, ipc.MsgPaneOutput)
	}
}

func TestServerBroadcast(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "broadcast.sock")

	handler := func(conn *ipc.Conn, msg *ipc.Message) {}

	srv := ipc.NewServer(sockPath, handler, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	// Connect two clients
	c1, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client1 connect: %v", err)
	}
	defer c1.Close()

	c2, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client2 connect: %v", err)
	}
	defer c2.Close()

	// Give server time to register connections
	time.Sleep(50 * time.Millisecond)

	// Broadcast
	msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, map[string]string{"action": "test"})
	srv.Broadcast(msg)

	// Both clients should receive
	r1, err := c1.Receive()
	if err != nil {
		t.Fatalf("c1 receive: %v", err)
	}
	if r1.Type != ipc.MsgStateUpdate {
		t.Errorf("c1 type: got %q, want %q", r1.Type, ipc.MsgStateUpdate)
	}

	r2, err := c2.Receive()
	if err != nil {
		t.Fatalf("c2 receive: %v", err)
	}
	if r2.Type != ipc.MsgStateUpdate {
		t.Errorf("c2 type: got %q, want %q", r2.Type, ipc.MsgStateUpdate)
	}
}
