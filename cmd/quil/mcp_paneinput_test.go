package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// paneInputBridge stands up a real IPC server that answers pane_input with a
// scripted pane_input_resp, and returns a bridge wired to it.
//
// The same shape TestMCPBridge_DeclinePaneOutputSendsSubscribeOptOut uses. A
// real server rather than a fake sender because the thing under test is a ROUND
// TRIP — the request id has to come back for the bridge to match it, and a fake
// that just records the send would pass against the old fire-and-forget code.
func paneInputBridge(t *testing.T, reply ipc.PaneInputRespPayload) *mcpBridge {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "mcp-input.sock")

	srv := ipc.NewServer(sockPath, func(conn *ipc.Conn, m *ipc.Message) {
		if m.Type != ipc.MsgPaneInput {
			return
		}
		resp, err := ipc.NewMessage(ipc.MsgPaneInputResp, reply)
		if err != nil {
			return
		}
		// Echo the request id, or the bridge cannot match the answer.
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
	return bridge
}

// A refusal must surface as an ERROR carrying the daemon's reason. Reporting
// success for input the daemon dropped is what made an agent wait forever for
// output from a command that never ran.
func TestSendPaneInput_ReturnsTheDaemonsRefusal(t *testing.T) {
	bridge := paneInputBridge(t, ipc.PaneInputRespPayload{
		PaneID:    "pane-1",
		Delivered: false,
		Error:     "pane is still waiting for its worktree (feat/x) and has no process yet",
	})

	err := sendPaneInput(bridge, "pane-1", []byte("ls\n"))
	if err == nil {
		t.Fatal("sendPaneInput reported success for input the daemon refused")
	}
	if !strings.Contains(err.Error(), "waiting for its worktree") {
		t.Errorf("err = %v, want it to carry the daemon's own reason", err)
	}
	if !strings.Contains(err.Error(), "pane-1") {
		t.Errorf("err = %v, want it to name the pane", err)
	}
}

// A refusal with no reason still has to fail, or a daemon that answers
// Delivered=false with an empty Error would read as success.
func TestSendPaneInput_FailsOnAReasonlessRefusal(t *testing.T) {
	bridge := paneInputBridge(t, ipc.PaneInputRespPayload{PaneID: "pane-1"})

	if err := sendPaneInput(bridge, "pane-1", []byte("ls\n")); err == nil {
		t.Fatal("a Delivered=false answer with no Error was treated as success")
	}
}

// And a real delivery must NOT fail, or every ordinary send breaks.
func TestSendPaneInput_SucceedsWhenDelivered(t *testing.T) {
	bridge := paneInputBridge(t, ipc.PaneInputRespPayload{PaneID: "pane-1", Delivered: true})

	if err := sendPaneInput(bridge, "pane-1", []byte("ls\n")); err != nil {
		t.Errorf("sendPaneInput: %v, want success for delivered input", err)
	}
}

// The request must carry an ID and the payload the caller asked for. The daemon
// answers ONLY id-bearing requests, so a send that forgot the id would hang
// until the bridge timeout rather than reporting anything.
func TestSendPaneInput_SendsAnIDBearingRequestWithTheData(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mcp-input2.sock")
	got := make(chan *ipc.Message, 4)
	srv := ipc.NewServer(sockPath, func(conn *ipc.Conn, m *ipc.Message) {
		if m.Type != ipc.MsgPaneInput {
			return
		}
		got <- m
		resp, err := ipc.NewMessage(ipc.MsgPaneInputResp,
			ipc.PaneInputRespPayload{PaneID: "pane-1", Delivered: true})
		if err != nil {
			return
		}
		resp.ID = m.ID
		conn.Send(resp)
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
	bridge := newMCPBridge(client)
	go bridge.readLoop(context.Background())

	if err := sendPaneInput(bridge, "pane-1", []byte("hello")); err != nil {
		t.Fatalf("sendPaneInput: %v", err)
	}

	select {
	case msg := <-got:
		if msg.ID == "" {
			t.Error("request carried no ID — the daemon answers only id-bearing requests, so this would time out")
		}
		var p ipc.PaneInputPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.PaneID != "pane-1" || string(p.Data) != "hello" {
			t.Errorf("payload = %+v, want the pane and bytes the caller passed", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never received a pane_input")
	}
}
