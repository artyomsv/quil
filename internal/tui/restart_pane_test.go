package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestHandleConfirmKey_RestartPaneSendsRequest: confirming the restart
// dialog must fire MsgRestartPaneReq for the captured pane id — the same
// request the MCP restart_pane tool uses — and close the dialog.
func TestHandleConfirmKey_RestartPaneSendsRequest(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindRestartPane,
		confirmID:   "pane-abc123",
	}

	out, _ := m.handleConfirmKey(tea.KeyPressMsg{Text: "y"})
	got := out.(Model)
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", got.dialog)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	if fake.sent[0].Type != ipc.MsgRestartPaneReq {
		t.Errorf("sent type = %q, want %q", fake.sent[0].Type, ipc.MsgRestartPaneReq)
	}
	var payload ipc.RestartPaneReqPayload
	if err := fake.sent[0].DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.PaneID != "pane-abc123" {
		t.Errorf("payload.PaneID = %q, want %q", payload.PaneID, "pane-abc123")
	}
}

// TestHandleConfirmKey_RestartPaneEnterAlsoConfirms: unlike the stop-daemon
// confirm (y-only), restarting one pane is cheap to undo, so Enter must
// work like every other confirm dialog.
func TestHandleConfirmKey_RestartPaneEnterAlsoConfirms(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindRestartPane,
		confirmID:   "pane-1",
	}
	out, _ := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if out.(Model).dialog != dialogNone {
		t.Error("Enter did not confirm the restart dialog")
	}
	if len(fake.sent) != 1 {
		t.Errorf("sent %d messages, want 1", len(fake.sent))
	}
}

// TestHandleConfirmKey_RestartPaneEscCancels: Esc must close the dialog
// without sending anything.
func TestHandleConfirmKey_RestartPaneEscCancels(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindRestartPane,
		confirmID:   "pane-1",
	}
	out, _ := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if out.(Model).dialog != dialogNone {
		t.Error("Esc did not close the restart confirm")
	}
	if len(fake.sent) != 0 {
		t.Errorf("Esc sent %d messages, want 0", len(fake.sent))
	}
}

// TestHandleConfirmKey_RestartPaneSendErrorClosesDialog: a failed Send is
// logged, not surfaced as a stuck dialog — the user can retry with Alt+R.
func TestHandleConfirmKey_RestartPaneSendErrorClosesDialog(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{sendErr: errors.New("socket closed")}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindRestartPane,
		confirmID:   "pane-1",
	}
	out, _ := m.handleConfirmKey(tea.KeyPressMsg{Text: "y"})
	if out.(Model).dialog != dialogNone {
		t.Error("dialog must close even when Send fails")
	}
	if len(fake.sent) != 1 {
		t.Errorf("Send attempts = %d, want 1", len(fake.sent))
	}
}

// TestPaneDisplayName covers the label fallback chain used by both the
// close-pane and restart-pane confirm dialogs.
func TestPaneDisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pane PaneModel
		want string
	}{
		{"explicit name wins", PaneModel{ID: "pane-12345678", Name: "CB-13395", CWD: "/x"}, "CB-13395"},
		{"cwd fallback", PaneModel{ID: "pane-12345678", CWD: "/projects/quil"}, "/projects/quil"},
		{"id truncated", PaneModel{ID: "pane-12345678"}, "pane-123"},
		{"short id kept whole", PaneModel{ID: "p1"}, "p1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paneDisplayName(&tt.pane); got != tt.want {
				t.Errorf("paneDisplayName = %q, want %q", got, tt.want)
			}
		})
	}
}
