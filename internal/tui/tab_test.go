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

// sLeaf and sSplit build an expected SerializedNode tree, Ratio 0.5.
func sLeaf(id string) *SerializedNode { return &SerializedNode{PaneID: id} }

func sSplit(dir SplitDir, left, right *SerializedNode) *SerializedNode {
	return &SerializedNode{Split: &dir, Ratio: 0.5, Left: left, Right: right}
}

// TestPlaceArrivingPane_Spirals: the arriving pane splits the LAST pane leaf
// against its parent's direction, so successive arrivals spiral into the
// bottom-right corner instead of forming thin columns — and the min-size
// fallback flips the direction only when the other one fits.
func TestPlaceArrivingPane_Spirals(t *testing.T) {
	a, b, c := func() *LayoutNode { return NewLeaf(newTestPane("a")) },
		func() *LayoutNode { return NewLeaf(newTestPane("b")) },
		func() *LayoutNode { return NewLeaf(newTestPane("c")) }
	split := func(dir SplitDir, l, r *LayoutNode) *LayoutNode {
		return &LayoutNode{Split: dir, Ratio: 0.5, Left: l, Right: r}
	}
	H, V := SplitHorizontal, SplitVertical

	tests := []struct {
		name string
		root *LayoutNode
		w, h int
		want *SerializedNode
	}{
		{"A → A|new", a(), 100, 40,
			sSplit(H, sLeaf("a"), sLeaf("new"))},
		{"A|B → A|(B/new)", split(H, a(), b()), 100, 40,
			sSplit(H, sLeaf("a"), sSplit(V, sLeaf("b"), sLeaf("new")))},
		{"A|(B/C) → A|(B/(C|new))", split(H, a(), split(V, b(), c())), 100, 40,
			sSplit(H, sLeaf("a"), sSplit(V, sLeaf("b"), sSplit(H, sLeaf("c"), sLeaf("new"))))},
		{"(A/B) → (A/(B|new))", split(V, a(), b()), 100, 40,
			sSplit(V, sLeaf("a"), sSplit(H, sLeaf("b"), sLeaf("new")))},
		// B is 50x7: top|bottom would give 3|4 rows, left|right fits.
		{"preferred V too short → H", split(H, a(), b()), 100, 7,
			sSplit(H, sLeaf("a"), sSplit(H, sLeaf("b"), sLeaf("new")))},
		// A is 18 wide: left|right would give 9|9, top|bottom fits.
		{"preferred H too narrow → V", a(), 18, 40,
			sSplit(V, sLeaf("a"), sLeaf("new"))},
		// B is 15x6: 7|8 columns and 3|3 rows — neither fits, keep V.
		{"neither fits → preferred", split(H, a(), b()), 30, 6,
			sSplit(H, sLeaf("a"), sSplit(V, sLeaf("b"), sLeaf("new")))},
		{"unknown geometry → preferred", split(H, a(), b()), 0, 0,
			sSplit(H, sLeaf("a"), sSplit(V, sLeaf("b"), sLeaf("new")))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab := NewTabModel("t1", "Test")
			tab.Root = tt.root
			if ok := tab.placeArrivingPane(newTestPane("new"), tt.w, tt.h); !ok {
				t.Fatal("placeArrivingPane returned false")
			}
			if got := SerializeLayout(tab.Root); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tree = %s, want %s", layoutString(got), layoutString(tt.want))
			}
		})
	}
}

// TestPlaceArrivingPane_SkipsATrailingPlaceholder: the last leaf is a slot
// reserved for another create, so the last PANE leaf is split and the
// reservation is left exactly where it was.
func TestPlaceArrivingPane_SkipsATrailingPlaceholder(t *testing.T) {
	ph := &LayoutNode{Ratio: 0.5}
	tab := NewTabModel("t1", "Test")
	tab.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left: NewLeaf(newTestPane("a")),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5,
			Left: NewLeaf(newTestPane("b")), Right: ph},
	}

	if ok := tab.placeArrivingPane(newTestPane("new"), 100, 40); !ok {
		t.Fatal("placeArrivingPane returned false")
	}
	right := tab.Root.Right
	if right.Right != ph || ph.Pane != nil {
		t.Fatal("the reservation was moved or filled")
	}
	bNode := right.Left
	if bNode.Split != SplitHorizontal || bNode.Left.Pane.ID != "b" || bNode.Right.Pane.ID != "new" {
		t.Errorf("b's slot = %s, want b|new (opposite of its top|bottom parent)", layoutString(SerializeLayout(bNode)))
	}
}

// layoutString renders a SerializedNode tree as `a|(b/c)` for failure output.
func layoutString(n *SerializedNode) string {
	switch {
	case n == nil, n.Split != nil && n.Left == nil && n.Right == nil:
		return "·" // a placeholder serializes as a childless split
	case n.Split == nil:
		if n.PaneID == "" {
			return "·"
		}
		return n.PaneID
	}
	sep := "|"
	if *n.Split == SplitVertical {
		sep = "/"
	}
	return "(" + layoutString(n.Left) + sep + layoutString(n.Right) + ")"
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
