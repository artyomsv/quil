package tui

import (
	"testing"
)

func TestApplyWorkspaceState_OverlayPane_NotInLayoutTree(t *testing.T) {
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
	m.applyWorkspaceState(state)

	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
	tab := m.tabs[0]
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
	m := &Model{}
	withOverlay := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(withOverlay)
	m.tabs[0].overlayVisible = true

	// Overlay exits (user pressed q in lazygit) — daemon broadcasts without it.
	without := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n"}}},
		Panes:     []PaneInfo{{ID: "pane-n", TabID: "tab-1", Type: "terminal"}},
	}
	m.applyWorkspaceState(without)

	tab := m.tabs[0]
	if tab.overlayPane != nil || tab.overlayVisible {
		t.Errorf("overlay slot must be cleared, got pane=%v visible=%v", tab.overlayPane, tab.overlayVisible)
	}

	// Regression: a third apply (still no overlay) must not panic — the
	// dropped overlay PaneModel must be disposed exactly once (by the
	// surviving sweep), never a second time.
	m.applyWorkspaceState(without)
}

// Regression: restoreTabLayout (the fast path for new tabs with saved layout)
// must not build PaneModels for overlay panes — each one starts a VT drain
// goroutine that was never adopted and never disposed (leak). The overlay must
// still be adopted into the slot via reconcileOverlayPane.
func TestApplyWorkspaceState_RestoredLayout_OverlayAdoptedNotInTree(t *testing.T) {
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
	m.applyWorkspaceState(state)

	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
	tab := m.tabs[0]
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

func TestPaneModel_Dispose_Idempotent(t *testing.T) {
	p := NewPaneModel("pane-dispose", 1024)
	p.Dispose()
	// Second Dispose must be a no-op (vt nil-guard), not a second
	// vt.Close()/drain-stop attempt.
	p.Dispose()
}

func TestApplyWorkspaceState_PendingOverlayShow_ShowsOnArrival(t *testing.T) {
	m := &Model{pendingOverlayShow: map[string]bool{"tab-1": true}}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(state)

	tab := m.tabs[0]
	if tab.overlayPane == nil || !tab.overlayVisible {
		t.Fatalf("overlay must show on arrival when this TUI requested it (pane=%v visible=%v)", tab.overlayPane, tab.overlayVisible)
	}
	if m.pendingOverlayShow["tab-1"] {
		t.Error("pendingOverlayShow must be consumed")
	}
}
