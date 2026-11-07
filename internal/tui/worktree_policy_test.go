package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// sentWorktreePolicy returns the last worktree_policy payload the fake
// connection carried, and whether there was one.
func sentWorktreePolicy(t *testing.T, conn *fakeConn) (ipc.WorktreePolicyPayload, bool) {
	t.Helper()
	var p ipc.WorktreePolicyPayload
	found := false
	for _, sent := range conn.sent {
		if sent.Type != ipc.MsgWorktreePolicy {
			continue
		}
		if err := sent.DecodePayload(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		found = true
	}
	return p, found
}

// Toggling the F1 row must reach the daemon at once: the daemon enforces the
// switch, and the config file is written only when the TUI exits.
//
// Fails if the isBool arm returns without worktreePolicyCmd, or if the row is
// missing its worktreePolicy flag.
func TestSettingsDialog_RemoteBranchDeleteRowSendsPolicy(t *testing.T) {
	idx := -1
	for i, f := range settingsFields() {
		if f.label == "Allow remote branch delete" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no \"Allow remote branch delete\" row in settingsFields()")
	}
	conn := newFakeConn()
	m := Model{cfg: config.Default(), client: conn, dialog: dialogSettings, dialogCursor: idx}

	out, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if !got.cfg.Worktree.RemoteBranchDelete {
		t.Fatal("Enter on the row did not switch it on")
	}
	if !got.configChanged {
		t.Error("toggle did not flag configChanged; it would be lost on exit")
	}
	if cmd == nil {
		t.Fatal("toggle produced no command — the daemon would keep refusing")
	}
	cmd()
	if p, ok := sentWorktreePolicy(t, conn); !ok || !p.RemoteBranchDelete {
		t.Errorf("worktree_policy sent = %v with %+v, want sent with true", ok, p)
	}
}

// The attach push is what tells a freshly attached daemon the switch at all.
// Admin class: sent only where it is accepted.
func TestAttach_SendsWorktreePolicyOnlyWhereAdmin(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsStandard, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			m, conn := readOnlyModel(t, rights)
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			if sent := sentType(conn, ipc.MsgWorktreePolicy); sent != (rights == ipc.RightsFull) {
				t.Fatalf("worktree_policy sent = %v on rights %q", sent, rights)
			}
		})
	}
}
