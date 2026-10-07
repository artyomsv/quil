package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// assertLinkDownFlash checks the flash names the parked host and why.
func assertLinkDownFlash(t *testing.T, m Model, what string) {
	t.Helper()
	if !strings.Contains(m.flashText, roDest+" is disconnected") || !strings.Contains(m.flashText, "token refused") {
		t.Errorf("%s: flash = %q, want the host named as disconnected, with the reason", what, m.flashText)
	}
}

// After a token revoke the --connect window's link is parked, and its dead
// conn stays in the router. The pane, tab and project menus kept every row
// enabled, and a rename, a mute, an attention mark or a group move was sent
// into that conn and lost while the local copy showed it done (manual
// retest, PR #256). The rows that need the daemon are greyed now; the local
// view rows stay.
func TestLinkDown_MenusGreyDaemonActions(t *testing.T) {
	m, _ := rawArgsModel(t, ipc.RightsFull)
	m.sidebarOpen = true
	m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
	m = parkRevoked(t, m)

	// The pane menu, by its key (quick actions).
	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
	if !m.ctxMenu.open() {
		t.Fatal("setup: the pane menu did not open")
	}
	assertCtxRows(t, m.ctxMenu.items, false, ctxActRename, ctxActClose,
		ctxActMute, ctxActAttention, ctxActMarkDeletion, ctxActRestart, ctxActMovePane)
	assertCtxRows(t, m.ctxMenu.items, true, ctxActFocus)
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// The tab menu, by a right-click on the tab bar.
	spans := m.tabSpans()
	m = roUpdate(t, m, tea.MouseClickMsg{X: m.projectSidebarWidth() + spans[0].start + 1, Y: 0, Button: tea.MouseRight})
	assertCtxRows(t, m.ctxMenu.items, false, ctxActRenameTab, ctxActTabColorList)
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// The project menu, by a right-click on its sidebar row.
	m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseRight})
	assertCtxRows(t, m.ctxMenu.items, false, ctxActRenameProject, ctxActGroupList)
	// Client-side only: still offered.
	assertCtxRows(t, m.ctxMenu.items, true, ctxActDisconnectHost)
}

// A menu opened while the link was up keeps its rows; the executor refuses
// a row chosen after the link went down, and nothing is sent.
func TestLinkDown_MenuOpenedBeforeTheParkRefuses(t *testing.T) {
	for _, act := range []ctxMenuAction{ctxActAttention, ctxActMute, ctxActMarkDeletion} {
		m, conn := rawArgsModel(t, ipc.RightsFull)
		m = roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
		m.ctxMenu.cursor = ctxItemIndex(t, m.ctxMenu.items, act)
		m = parkRevoked(t, m)
		clearSent(conn)

		m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

		if n := countSent(conn, ipc.MsgUpdatePane); n != 0 {
			t.Errorf("row %d: %d update_pane sent into the parked link", act, n)
		}
		assertLinkDownFlash(t, m, "menu row")
	}
}

// Alt+F2 on a parked host opens no rename; a rename typed while the link was
// up and committed after it went down is refused, sends nothing, and leaves
// the pane's name as the daemon has it.
func TestLinkDown_RenameRefused(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	altF2 := tea.KeyPressMsg{Code: tea.KeyF2, Mod: tea.ModAlt}

	// Typed while the link was up.
	m = roUpdate(t, m, altF2)
	if !m.renamingPane {
		t.Fatal("setup: Alt+F2 did not start a pane rename")
	}
	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = parkRevoked(t, m)
	clearSent(conn)
	pane := m.projects[0].tabs[0].Root.Pane
	before := pane.Name

	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if n := countSent(conn, ipc.MsgUpdatePane); n != 0 {
		t.Errorf("%d update_pane sent into the parked link", n)
	}
	if pane.Name != before {
		t.Errorf("pane name = %q, want %q: a rename no daemon made", pane.Name, before)
	}
	assertLinkDownFlash(t, m, "rename commit")

	// Started after the park: refused at the key.
	m.flashText = ""
	m = roUpdate(t, m, altF2)
	if m.renamingPane {
		t.Error("Alt+F2 started a rename on a parked host")
	}
	assertLinkDownFlash(t, m, "Alt+F2")
}

// The sidebar's "Move to group…" moved a parked host's project locally and
// sent the change into the dead conn. The list opened before the park, so
// its row is still enabled: the move itself refuses.
func TestLinkDown_GroupMoveRefused(t *testing.T) {
	m, _ := rawArgsModel(t, ipc.RightsFull)
	m.groups = projectGroups{Groups: []projectGroup{
		{Name: "G", Members: []groupMember{{Dest: roDest, ID: "proj-1"}}},
		{Name: "H"},
	}}
	m.sidebarOpen = true
	m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})

	m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseRight})
	m.ctxMenu.cursor = ctxItemIndex(t, m.ctxMenu.items, ctxActGroupList)
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	found := false
	for i, it := range m.ctxMenu.items {
		if it.id == ctxActSetGroup && it.groupName == "H" {
			m.ctxMenu.cursor, found = i, true
		}
	}
	if !found {
		t.Fatal("setup: the group list did not open")
	}
	m = parkRevoked(t, m)

	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if g := m.groups.groupOf(roDest, "proj-1"); g != 0 {
		t.Errorf("the parked host's project moved to group %d, want it left in G", g)
	}
	assertLinkDownFlash(t, m, "group move")
}
