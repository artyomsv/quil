package ipc_test

import (
	"bytes"
	"testing"

	"github.com/stukans/aethel/internal/ipc"
)

func TestWriteReadMessage(t *testing.T) {
	var buf bytes.Buffer

	msg := &ipc.Message{
		Type:    "create_pane",
		Payload: []byte(`{"cwd":"/home/user"}`),
	}

	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	got, err := ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if got.Type != msg.Type {
		t.Errorf("Type: got %q, want %q", got.Type, msg.Type)
	}
	if string(got.Payload) != string(msg.Payload) {
		t.Errorf("Payload: got %q, want %q", got.Payload, msg.Payload)
	}
}

func TestWriteReadMultipleMessages(t *testing.T) {
	var buf bytes.Buffer

	messages := []*ipc.Message{
		{Type: "attach", Payload: []byte(`{}`)},
		{Type: "pane_input", Payload: []byte(`{"pane_id":"p1","data":"ls\n"}`)},
		{Type: "pane_output", Payload: []byte(`{"pane_id":"p1","data":"file1 file2"}`)},
	}

	for _, m := range messages {
		if err := ipc.WriteMessage(&buf, m); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}

	for i, want := range messages {
		got, err := ipc.ReadMessage(&buf)
		if err != nil {
			t.Fatalf("ReadMessage %d failed: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("msg %d Type: got %q, want %q", i, got.Type, want.Type)
		}
	}
}

func TestMessageTypes(t *testing.T) {
	types := []string{
		ipc.MsgAttach,
		ipc.MsgDetach,
		ipc.MsgShutdown,
		ipc.MsgHeartbeat,
		ipc.MsgCreatePane,
		ipc.MsgDestroyPane,
		ipc.MsgResizePane,
		ipc.MsgPaneInput,
		ipc.MsgPaneOutput,
		ipc.MsgCreateTab,
		ipc.MsgDestroyTab,
		ipc.MsgSwitchTab,
		ipc.MsgWorkspaceState,
		ipc.MsgStateUpdate,
	}
	for _, typ := range types {
		if typ == "" {
			t.Error("found empty message type constant")
		}
	}
}

func TestNewMessageAndDecode(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
		TabID: "tab-1",
		CWD:   "/home/user",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if msg.Type != ipc.MsgCreatePane {
		t.Errorf("Type: got %q, want %q", msg.Type, ipc.MsgCreatePane)
	}

	var payload ipc.CreatePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.TabID != "tab-1" {
		t.Errorf("TabID: got %q, want %q", payload.TabID, "tab-1")
	}
	if payload.CWD != "/home/user" {
		t.Errorf("CWD: got %q, want %q", payload.CWD, "/home/user")
	}
}
