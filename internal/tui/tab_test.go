package tui

import (
	"reflect"
	"strings"
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

// TestTabModel_OverlayVisible_ViewReturnsOverlayContent: when an overlay pane
// is visible, TabModel.View() must delegate entirely to the overlay — the
// layout pane's content must not appear.
func TestTabModel_OverlayVisible_ViewReturnsOverlayContent(t *testing.T) {
	normal := NewPaneModel("pane-n", 1024)
	overlay := NewPaneModel("pane-o", 1024)
	defer normal.Dispose()
	defer overlay.Dispose()

	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(normal)
	tab.ActivePane = normal.ID
	tab.overlayPane = overlay

	// Give each pane a recognisable unique marker so the assertion is non-vacuous.
	normal.AppendOutput([]byte("NORMAL-UNIQUE-MARKER"))
	overlay.AppendOutput([]byte("OVERLAY-UNIQUE-MARKER"))

	// Resize with overlay hidden so the layout pane gets real dimensions.
	tab.Resize(80, 24)

	// Sanity: with overlay hidden the layout pane's content is visible.
	hiddenView := tab.View()
	if !strings.Contains(hiddenView, "NORMAL-UNIQUE-MARKER") {
		t.Error("hidden overlay: View() must contain the normal pane's content")
	}

	// Make the overlay visible and resize so the overlay pane gets dimensions.
	tab.overlayVisible = true
	tab.Resize(80, 24)
	visibleView := tab.View()
	if !strings.Contains(visibleView, "OVERLAY-UNIQUE-MARKER") {
		t.Error("visible overlay: View() must contain the overlay pane's content")
	}
	if strings.Contains(visibleView, "NORMAL-UNIQUE-MARKER") {
		t.Error("visible overlay: View() must NOT contain the layout pane's content")
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

func TestPlaceArrivingPane_SingleLeafGoesLeftRight(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	tab.Root = NewLeaf(newTestPane("old"))

	ok := tab.placeArrivingPane(newTestPane("new"), 100, 40)
	if !ok {
		t.Fatal("placeArrivingPane returned false")
	}
	if tab.Root.Split != SplitHorizontal {
		t.Fatalf("root split: got %v, want SplitHorizontal", tab.Root.Split)
	}
	if tab.Root.Ratio != 0.5 {
		t.Fatalf("root ratio: got %v, want 0.5", tab.Root.Ratio)
	}
	if tab.Root.Left == nil || tab.Root.Left.Pane == nil || tab.Root.Left.Pane.ID != "old" {
		t.Fatalf("left child: want the old pane, got %+v", tab.Root.Left)
	}
	if tab.Root.Right == nil || tab.Root.Right.Pane == nil || tab.Root.Right.Pane.ID != "new" {
		t.Fatalf("right child: want the new pane, got %+v", tab.Root.Right)
	}
	if leaves := tab.Leaves(); len(leaves) != 2 {
		t.Fatalf("Leaves() = %d, want 2 (cache must be invalidated)", len(leaves))
	}
}

// TestPlaceArrivingPane_NarrowLeafGoesTopBottom: `A | B` in a 30-wide tab
// makes A 15 cells wide (7|8 when split again), narrower than minPaneW, so
// the arriving pane goes top/bottom under A instead: A becomes `A / new`.
func TestPlaceArrivingPane_NarrowLeafGoesTopBottom(t *testing.T) {
	tab := NewTabModel("t1", "Test")
	tab.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left:  NewLeaf(newTestPane("a")),
		Right: NewLeaf(newTestPane("b")),
	}

	ok := tab.placeArrivingPane(newTestPane("new"), 30, 40)
	if !ok {
		t.Fatal("placeArrivingPane returned false")
	}

	aNode := tab.Root.Left
	if aNode == nil || aNode.IsLeaf() {
		t.Fatalf("leaf 'a' should have been split, got %+v", aNode)
	}
	if aNode.Split != SplitVertical {
		t.Fatalf("a's split: got %v, want SplitVertical", aNode.Split)
	}
	if aNode.Left == nil || aNode.Left.Pane == nil || aNode.Left.Pane.ID != "a" {
		t.Fatalf("a's top child: want pane 'a', got %+v", aNode.Left)
	}
	if aNode.Right == nil || aNode.Right.Pane == nil || aNode.Right.Pane.ID != "new" {
		t.Fatalf("a's bottom child: want the new pane, got %+v", aNode.Right)
	}
	if tab.Root.Right == nil || tab.Root.Right.Pane == nil || tab.Root.Right.Pane.ID != "b" {
		t.Fatalf("leaf 'b' should be untouched, got %+v", tab.Root.Right)
	}
}

// buildThreeLeafTestTree returns a fresh 3-leaf tree (a | (b / c)) each call,
// for building two independent-but-structurally-identical trees.
func buildThreeLeafTestTree() *LayoutNode {
	return &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left: NewLeaf(newTestPane("a")),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5,
			Left:  NewLeaf(newTestPane("b")),
			Right: NewLeaf(newTestPane("c")),
		},
	}
}

// TestPlaceArrivingPane_DeterministicForIdenticalTrees is the multi-client
// guarantee in miniature: two clients holding independently-built but
// structurally identical trees, given the same geometry, must place the
// arriving pane in the same spot.
func TestPlaceArrivingPane_DeterministicForIdenticalTrees(t *testing.T) {
	tabA := NewTabModel("t1", "Test")
	tabA.Root = buildThreeLeafTestTree()
	tabB := NewTabModel("t2", "Test")
	tabB.Root = buildThreeLeafTestTree()

	okA := tabA.placeArrivingPane(newTestPane("new"), 101, 41)
	okB := tabB.placeArrivingPane(newTestPane("new"), 101, 41)
	if !okA || !okB {
		t.Fatalf("placeArrivingPane: okA=%v okB=%v, want true/true", okA, okB)
	}

	if !reflect.DeepEqual(SerializeLayout(tabA.Root), SerializeLayout(tabB.Root)) {
		t.Fatalf("layouts diverged:\n a=%+v\n b=%+v", SerializeLayout(tabA.Root), SerializeLayout(tabB.Root))
	}
}

func TestPlaceArrivingPane_NoPaneLeafLeavesTreeUntouched(t *testing.T) {
	t.Run("nil tree", func(t *testing.T) {
		tab := NewTabModel("t1", "Test")
		if ok := tab.placeArrivingPane(newTestPane("new"), 100, 40); ok {
			t.Fatal("placeArrivingPane returned true for a nil tree")
		}
		if tab.Root != nil {
			t.Fatalf("tree should stay nil, got %+v", tab.Root)
		}
	})

	t.Run("only placeholders", func(t *testing.T) {
		tab := NewTabModel("t1", "Test")
		placeholder := &LayoutNode{Ratio: 0.7}
		tab.Root = placeholder

		if ok := tab.placeArrivingPane(newTestPane("new"), 100, 40); ok {
			t.Fatal("placeArrivingPane returned true for a tree with no pane leaf")
		}
		if tab.Root != placeholder || tab.Root.Ratio != 0.7 || tab.Root.Left != nil || tab.Root.Right != nil || tab.Root.Pane != nil {
			t.Fatalf("tree must be untouched, got %+v", tab.Root)
		}
	})
}
