package tui

import (
	"testing"
)

func TestToggleFocus_SinglePane_NoOp(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	tab.Root = NewLeaf(newTestPane("p1"))
	tab.ActivePane = "p1"

	// Single-pane tab: ToggleFocus should be a no-op
	tab.ToggleFocus()
	if tab.FocusMode() {
		t.Error("focus should not activate on single-pane tab")
	}
}

func TestToggleFocus_MultiPane_TogglesOnOff(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	left := newTestPane("p1")
	right := newTestPane("p2")
	tab.Root = NewLeaf(left)
	tab.Root.SplitLeaf("p1", SplitHorizontal)
	tab.Root.Right.Pane = right
	tab.ActivePane = "p1"

	// Multi-pane: ToggleFocus should activate
	tab.ToggleFocus()
	if !tab.FocusMode() {
		t.Error("focus should be active after toggle")
	}

	// Toggle again: should deactivate
	tab.ToggleFocus()
	if tab.FocusMode() {
		t.Error("focus should be inactive after second toggle")
	}
}

func TestExitFocus_DeactivatesFocusMode(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	left := newTestPane("p1")
	right := newTestPane("p2")
	tab.Root = NewLeaf(left)
	tab.Root.SplitLeaf("p1", SplitHorizontal)
	tab.Root.Right.Pane = right
	tab.ActivePane = "p1"

	tab.ToggleFocus()
	if !tab.FocusMode() {
		t.Fatal("focus should be active")
	}

	tab.ExitFocus()
	if tab.FocusMode() {
		t.Error("focus should be inactive after ExitFocus")
	}
}

// Use NewPaneModel (not newTestPane) because Resize needs a VT emulator.
func TestResize_FocusMode_ActivePaneGetsFullDimensions(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	tab.Root = NewLeaf(p1)
	tab.Root.SplitLeaf("p1", SplitHorizontal)
	tab.Root.Right.Pane = p2
	tab.ActivePane = "p1"

	// Normal resize: both panes get partial width
	tab.Resize(100, 40)
	if p1.Width == 100 {
		t.Error("in normal mode, pane should not get full width")
	}

	// Focus resize: active pane gets full dimensions
	tab.ToggleFocus()
	tab.Resize(100, 40)
	if p1.Width != 100 {
		t.Errorf("in focus mode, active pane width: got %d, want 100", p1.Width)
	}
	if p1.Height != 40 {
		t.Errorf("in focus mode, active pane height: got %d, want 40", p1.Height)
	}
}

// Use NewPaneModel (not newTestPane) because View calls pane.View() which needs VT.
func TestView_FocusMode_RendersSinglePane(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	tab.Root = NewLeaf(p1)
	tab.Root.SplitLeaf("p1", SplitHorizontal)
	tab.Root.Right.Pane = p2
	tab.ActivePane = "p1"

	// Resize so panes can render
	tab.Resize(80, 24)

	// Normal view should contain content from both panes
	normalView := tab.View()

	// Focus view should be different (only one pane)
	tab.ToggleFocus()
	tab.Resize(80, 24)
	focusView := tab.View()

	if normalView == focusView {
		t.Error("focus view should differ from normal view (single pane vs split)")
	}
}

func TestTabModel_OverlayVisible_ActivePaneModelReturnsOverlay(t *testing.T) {
	normal := NewPaneModel("pane-n", 1024)
	overlay := NewPaneModel("pane-o", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(normal)
	tab.ActivePane = normal.ID
	tab.overlayPane = overlay

	if got := tab.ActivePaneModel(); got != normal {
		t.Fatalf("hidden overlay: ActivePaneModel = %v, want normal pane", got)
	}
	tab.overlayVisible = true
	if got := tab.ActivePaneModel(); got != overlay {
		t.Fatalf("visible overlay: ActivePaneModel = %v, want overlay pane", got)
	}
}

func TestTabModel_OverlayVisible_ResizeSizesOverlay(t *testing.T) {
	normal := NewPaneModel("pane-n", 1024)
	overlay := NewPaneModel("pane-o", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(normal)
	tab.ActivePane = normal.ID
	tab.overlayPane = overlay
	tab.overlayVisible = true
	tab.Resize(80, 24)

	if overlay.Width != 80 || overlay.Height != 24 {
		t.Errorf("overlay sized %dx%d, want 80x24", overlay.Width, overlay.Height)
	}
	// The hidden layout must ALSO stay current (overlay hides later).
	if normal.Width == 0 {
		t.Error("layout pane must still be resized while overlay is visible")
	}
}
