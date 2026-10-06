package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// viewerMenuModel is a read-only viewer of roDest with the sidebar open,
// started against home ("" is a local start, roDest a --connect one).
func viewerMenuModel(t *testing.T, home string) Model {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir()) // a disconnect rewrites config
	m, _ := readOnlyModel(t, ipc.RightsReadOnly)
	m.SetHomeDest(home)
	m.sidebarOpen = true
	return roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
}

// confirmDisconnect answers yes to the disconnect confirm for dest.
func confirmDisconnect(t *testing.T, m Model, dest string) Model {
	t.Helper()
	m.confirmKind, m.confirmID, m.dialog = confirmKindDisconnectHost, dest, dialogConfirm
	return roUpdate(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
}

// A viewer started with --connect could "Disconnect host…" the only host it
// had: every project went, and the window showed "Connecting to quild…" for
// good (manual test, PR #256). The start host is not offered, and the
// confirm refuses it too, leaving the session as it was.
func TestDisconnectHost_StartHostIsNotDisconnected(t *testing.T) {
	m := viewerMenuModel(t, roDest)

	m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseRight})
	assertCtxRows(t, m.ctxMenu.items, false, ctxActDisconnectHost)
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	m = confirmDisconnect(t, m, roDest)
	if r := m.client.(*Router); r.Conn(roDest) == nil {
		t.Fatal("the start host's connection was dropped")
	}
	if len(m.projects) != 1 || m.activeTabModel() == nil {
		t.Fatalf("the start host's projects went: %d projects", len(m.projects))
	}
	if m.dialog != dialogNone {
		t.Errorf("the confirm stayed open (dialog %v)", m.dialog)
	}
	if !m.destReadOnly(roDest) {
		t.Error("the refused disconnect forgot the host's rights")
	}
}

// Disconnect is client-local, so a viewer may still disconnect a host that is
// NOT the one it started on.
func TestDisconnectHost_ViewerDisconnectsASecondHost(t *testing.T) {
	m := viewerMenuModel(t, "")

	m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseRight})
	assertCtxRows(t, m.ctxMenu.items, true, ctxActDisconnectHost)
	m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	m = confirmDisconnect(t, m, roDest)
	if r := m.client.(*Router); r.Conn(roDest) != nil {
		t.Error("the second host's connection survived the disconnect")
	}
	for _, p := range m.projects {
		if p.Dest == roDest {
			t.Errorf("project %s of the disconnected host is still listed", p.ID)
		}
	}
}

// The palette greys its Disconnect row for the start host, like the menu, and
// keeps it for any other host.
func TestDisconnectHost_PaletteGreysTheStartHost(t *testing.T) {
	for _, home := range []string{roDest, ""} {
		m := viewerMenuModel(t, home)
		found := false
		for _, c := range m.buildPaletteCommands() {
			if c.action != palActRemoveProject {
				continue
			}
			found = true
			if want := home == ""; c.enabled != want {
				t.Errorf("home %q: palette %q enabled = %v, want %v", home, c.label, c.enabled, want)
			}
		}
		if !found {
			t.Errorf("home %q: no Disconnect row in the palette", home)
		}
	}
}

// The palette's "remove project" reaches the same confirm by another way. On
// the start host it says why it did nothing rather than opening the confirm.
func TestDisconnectHost_StartHostConfirmFlashes(t *testing.T) {
	m := viewerMenuModel(t, roDest)
	cmd := m.confirmDisconnectHost(m.cur().ID)
	runCmdNoWait(cmd)
	if m.dialog == dialogConfirm {
		t.Fatal("the confirm opened for the start host")
	}
	if !strings.Contains(m.flashText, "cannot be disconnected") {
		t.Errorf("flash = %q, want why", m.flashText)
	}
}
