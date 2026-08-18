package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// The bridge must actually SEND the opt-out, with the right shape.
//
// Extracted from runMCP so it can be driven at all: runMCP builds an MCP server
// over stdio and blocks, so the send was previously verifiable only by reading
// it. The daemon-side effect is covered separately
// (TestHandleSubscribe_DispatchArmStopsPaneOutputForThatClientOnly); this pins
// the producer.
func TestMCPBridge_DeclinePaneOutputSendsSubscribeOptOut(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mcp-sub.sock")

	got := make(chan *ipc.Message, 4)
	srv := ipc.NewServer(sockPath, func(_ *ipc.Conn, m *ipc.Message) {
		got <- m
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	client, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer client.Close()

	if err := newMCPBridge(client).declinePaneOutput(); err != nil {
		t.Fatalf("declinePaneOutput: %v", err)
	}

	select {
	case msg := <-got:
		if msg.Type != ipc.MsgSubscribe {
			t.Fatalf("daemon received %q, want %q", msg.Type, ipc.MsgSubscribe)
		}
		var p ipc.SubscribePayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if p.PaneOutput == nil {
			t.Fatal("pane_output absent — an omitted field means 'leave it alone', " +
				"so the bridge would stay subscribed")
		}
		if *p.PaneOutput {
			t.Error("pane_output = true — the bridge asked to KEEP the stream it exists to decline")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon never received a subscribe message from the bridge")
	}
}
