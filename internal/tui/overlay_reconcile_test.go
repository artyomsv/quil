package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// The three flip sites below set tab.overlayVisible AND return a command that
// reports it. Every test in this file used to assert only the local flag and
// discard applyWorkspaceState's []tea.Cmd, so the report could be deleted at any
// of them with the package green (mutation-verified in the QA review). The
// consequence of losing one is a silent leak: an overlay that ends or is
// replaced without telling the daemon keeps OverlayHiddenAt zero and is exempt
// from the sweep forever — exactly what this feature exists to close.

// overlayStateWith builds a one-tab broadcast, optionally carrying an overlay.
func overlayStateWith(overlay bool) WorkspaceStateMsg {
	tab := TabInfo{ID: "tab-1", Name: "t", Panes: []string{"pane-n"}}
	panes := []PaneInfo{{ID: "pane-n", TabID: "tab-1", Type: "terminal"}}
	if overlay {
		tab.Panes = append(tab.Panes, "pane-o")
		panes = append(panes, PaneInfo{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true})
	}
	return WorkspaceStateMsg{ActiveTab: "tab-1", Tabs: []TabInfo{tab}, Panes: panes}
}

// An overlay ending (lazygit's `q`, a reattach that no longer has it) must tell
// the daemon it went hidden. Nothing else will: the pane is about to be gone
// from the client's model entirely.
func TestApplyWorkspaceState_OverlayGone_ReportsHiddenOnTheWire(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn}

	m.applyWorkspaceState(overlayStateWith(true), "")
	m.curTabs()[0].overlayVisible = true
	conn.sent = nil

	_, cmds := m.applyWorkspaceState(overlayStateWith(false), "")
	runCmds(cmds)

	got := overlayReports(t, conn)
	if v, ok := got["pane-o"]; !ok || v {
		t.Errorf("an overlay that disappeared reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
}

// The arrival half: this TUI's own Alt+G created the overlay, so it comes on
// screen the moment the daemon reports it — and the daemon must be told, or the
// pane it just created still carries whatever hidden stamp it had.
//
// The overlay-less apply first is deliberate: it settles the project and its
// active tab, so the only thing that can produce a report on the second apply is
// this flip site (a project's FIRST broadcast reports its own truth too).
func TestApplyWorkspaceState_PendingOverlayShow_ReportsVisibleOnTheWire(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn}

	m.applyWorkspaceState(overlayStateWith(false), "")
	m.pendingOverlayShow = map[string]bool{"tab-1": true}
	conn.sent = nil

	_, cmds := m.applyWorkspaceState(overlayStateWith(true), "")
	runCmds(cmds)

	got := overlayReports(t, conn)
	if v, ok := got["pane-o"]; !ok || !v {
		t.Errorf("an overlay shown on arrival reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

func TestApplyWorkspaceState_OverlayPane_NotInLayoutTree(t *testing.T) {
	t.Parallel()
	m := &Model{}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs: []TabInfo{{
			ID: "tab-1", Name: "t",
			Panes: []string{"pane-n", "pane-o"},
		}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", CWD: "/repo", Overlay: true},
		},
	}
	m.applyWorkspaceState(state, "")

	if len(m.curTabs()) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.curTabs()))
	}
	tab := m.curTabs()[0]
	if tab.Root == nil || len(tab.Leaves()) != 1 || tab.Leaves()[0].ID != "pane-n" {
		t.Errorf("layout tree must hold only the normal pane, got %v", tab.Leaves())
	}
	if tab.overlayPane == nil || tab.overlayPane.ID != "pane-o" {
		t.Fatalf("overlayPane = %v, want pane-o", tab.overlayPane)
	}
	if tab.overlayVisible {
		t.Error("overlay must default to hidden on reattach")
	}
}

func TestApplyWorkspaceState_OverlayGone_ClearsSlot(t *testing.T) {
	t.Parallel()
	m := &Model{}
	withOverlay := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(withOverlay, "")
	m.curTabs()[0].overlayVisible = true

	// Overlay exits (user pressed q in lazygit) — daemon broadcasts without it.
	without := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n"}}},
		Panes:     []PaneInfo{{ID: "pane-n", TabID: "tab-1", Type: "terminal"}},
	}
	m.applyWorkspaceState(without, "")

	tab := m.curTabs()[0]
	if tab.overlayPane != nil || tab.overlayVisible {
		t.Errorf("overlay slot must be cleared, got pane=%v visible=%v", tab.overlayPane, tab.overlayVisible)
	}

	// Regression: a third apply (still no overlay) must not panic — the
	// dropped overlay PaneModel must be disposed exactly once (by the
	// surviving sweep), never a second time.
	m.applyWorkspaceState(without, "")
}

// Regression: restoreTabLayout (the fast path for new tabs with saved layout)
// must not build PaneModels for overlay panes — each one starts a VT drain
// goroutine that was never adopted and never disposed (leak). The overlay must
// still be adopted into the slot via reconcileOverlayPane.
func TestApplyWorkspaceState_RestoredLayout_OverlayAdoptedNotInTree(t *testing.T) {
	t.Parallel()
	m := &Model{}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs: []TabInfo{{
			ID: "tab-1", Name: "t",
			Panes:  []string{"pane-n", "pane-o"},
			Layout: []byte(`{"pane_id":"pane-n"}`),
		}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(state, "")

	if len(m.curTabs()) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.curTabs()))
	}
	tab := m.curTabs()[0]
	if tab.Root == nil || len(tab.Leaves()) != 1 || tab.Leaves()[0].ID != "pane-n" {
		t.Errorf("restored layout tree must hold only the normal pane, got %v", tab.Leaves())
	}
	if tab.overlayPane == nil || tab.overlayPane.ID != "pane-o" {
		t.Fatalf("overlayPane = %v, want pane-o", tab.overlayPane)
	}
	if tab.overlayVisible {
		t.Error("overlay must default to hidden on restore")
	}
}

func TestApplyWorkspaceState_PendingOverlayShow_ShowsOnArrival(t *testing.T) {
	t.Parallel()
	m := &Model{pendingOverlayShow: map[string]bool{"tab-1": true}}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(state, "")

	tab := m.curTabs()[0]
	if tab.overlayPane == nil || !tab.overlayVisible {
		t.Fatalf("overlay must show on arrival when this TUI requested it (pane=%v visible=%v)", tab.overlayPane, tab.overlayVisible)
	}
	if m.pendingOverlayShow["tab-1"] {
		t.Error("pendingOverlayShow must be consumed")
	}
}
