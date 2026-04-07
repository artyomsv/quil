package ipc_test

import (
	"bytes"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
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
		ipc.MsgListPanesReq,
		ipc.MsgListPanesResp,
		ipc.MsgReadPaneOutputReq,
		ipc.MsgReadPaneOutputResp,
		ipc.MsgPaneStatusReq,
		ipc.MsgPaneStatusResp,
		ipc.MsgCreatePaneReq,
		ipc.MsgCreatePaneResp,
		ipc.MsgRestartPaneReq,
		ipc.MsgRestartPaneResp,
		ipc.MsgScreenshotPaneReq,
		ipc.MsgScreenshotPaneResp,
		ipc.MsgSwitchTabReq,
		ipc.MsgSwitchTabResp,
		ipc.MsgListTabsReq,
		ipc.MsgListTabsResp,
		ipc.MsgDestroyPaneReq,
		ipc.MsgDestroyPaneResp,
		ipc.MsgSetActivePane,
		ipc.MsgCloseTUI,
		ipc.MsgHighlightPane,
		ipc.MsgPaneEvent,
		ipc.MsgDismissEvent,
		ipc.MsgGetNotificationsReq,
		ipc.MsgGetNotificationsResp,
		ipc.MsgWatchNotificationsReq,
		ipc.MsgWatchNotificationsResp,
	}
	for _, typ := range types {
		if typ == "" {
			t.Error("found empty message type constant")
		}
	}
}

func TestMessageIDBackwardCompat(t *testing.T) {
	// Messages without ID should round-trip with empty ID (omitempty)
	var buf bytes.Buffer
	msg := &ipc.Message{Type: "attach", Payload: []byte(`{}`)}
	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	// Verify the wire format does NOT contain "id" when empty
	if bytes.Contains(buf.Bytes()[4:], []byte(`"id"`)) {
		t.Error("empty ID should be omitted from wire format")
	}
	got, err := ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.ID != "" {
		t.Errorf("ID should be empty, got %q", got.ID)
	}

	// Messages with ID should preserve it
	buf.Reset()
	msg = &ipc.Message{Type: "list_panes_req", ID: "req-123"}
	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	got, err = ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.ID != "req-123" {
		t.Errorf("ID: got %q, want %q", got.ID, "req-123")
	}
}

func TestMCPPayloadRoundTrip(t *testing.T) {
	// ListPanesResp
	resp, err := ipc.NewMessage(ipc.MsgListPanesResp, ipc.ListPanesRespPayload{
		Panes: []ipc.PaneInfo{
			{ID: "pane-1", TabID: "tab-1", TabName: "Shell", Type: "terminal", CWD: "/home", Running: true},
		},
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var payload ipc.ListPanesRespPayload
	if err := resp.DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if len(payload.Panes) != 1 {
		t.Fatalf("Panes: got %d, want 1", len(payload.Panes))
	}
	if payload.Panes[0].ID != "pane-1" {
		t.Errorf("Pane ID: got %q, want %q", payload.Panes[0].ID, "pane-1")
	}
	if !payload.Panes[0].Running {
		t.Error("Pane should be running")
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
