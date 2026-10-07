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

// The greyed rows are inert, and the keys for the same actions refuse with
// the reason. Nothing reaches the parked link. (A menu that was open when the
// link dropped is closed by the loss itself; see handleLinkLost.)
func TestLinkDown_RowsAndKeysSendNothing(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m = parkRevoked(t, m)
	clearSent(conn)

	for _, act := range []ctxMenuAction{ctxActAttention, ctxActMute, ctxActMarkDeletion} {
		m = roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
		m.ctxMenu.cursor = ctxItemIndex(t, m.ctxMenu.items, act)
		m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.ctxMenu.open() {
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
		}
	}
	if n := countSent(conn, ipc.MsgUpdatePane); n != 0 {
		t.Errorf("%d update_pane sent from greyed menu rows", n)
	}

	// Alt+M, the mute key.
	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt})
	if n := countSent(conn, ipc.MsgUpdatePane); n != 0 {
		t.Errorf("%d update_pane sent by the mute key", n)
	}
	assertLinkDownFlash(t, m, "mute key")
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

// Dragging a parked host's project onto a group header in the sidebar moved
// it locally and sent the change into the dead conn. The press no longer arms
// the drag, so the release changes nothing and sends nothing.
func TestLinkDown_SidebarGroupDragRefused(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m.groups = projectGroups{Groups: []projectGroup{{Name: "H"}}}
	m.sidebarOpen = true
	m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
	m = parkRevoked(t, m)
	clearSent(conn)

	hy := -1
	for y, r := range m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight()) {
		if r.kind == sidebarRowGroup && r.inGroup && r.group == 0 {
			hy = y
		}
	}
	if hy < 0 {
		t.Fatal("setup: no header row for group H")
	}
	py := sidebarRowY(t, m, sidebarRowProject)
	m = roUpdate(t, m,
		tea.MouseClickMsg{X: 1, Y: py, Button: tea.MouseLeft},
		tea.MouseMotionMsg{X: 1, Y: hy, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: 1, Y: hy, Button: tea.MouseLeft},
	)

	if g := m.groups.groupOf(roDest, "proj-1"); g >= 0 {
		t.Errorf("the parked host's project joined group %d", g)
	}
	if n := countSent(conn, ipc.MsgSetProjectGroup); n != 0 {
		t.Errorf("%d set_project_group sent into the parked link", n)
	}
}
