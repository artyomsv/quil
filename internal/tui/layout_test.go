package tui

import (
	"encoding/json"
	"testing"
)

// newTestPane creates a minimal PaneModel for testing (no VT emulator).
func newTestPane(id string) *PaneModel {
	return &PaneModel{ID: id, Name: id}
}

func TestSingleLeaf(t *testing.T) {
	p := newTestPane("a")
	root := NewLeaf(p)

	if !root.IsLeaf() {
		t.Fatal("expected leaf")
	}

	leaves := root.Leaves()
	if len(leaves) != 1 || leaves[0].ID != "a" {
		t.Fatalf("Leaves: got %v, want [a]", paneIDs(leaves))
	}
}

func TestLeavesOrdering(t *testing.T) {
	// Build a 3-leaf tree:
	//       V
	//      / \
	//     a   H
	//        / \
	//       b   c
	root := NewLeaf(newTestPane("a"))
	root.SplitLeaf("a", SplitVertical)
	root.Right.Pane = newTestPane("bc-placeholder") // fill placeholder first
	// Actually let's rebuild properly:
	root = NewLeaf(newTestPane("a"))
	placeholder := root.SplitLeaf("a", SplitVertical)
	placeholder.Pane = newTestPane("b")

	// Now split b horizontally.
	placeholder2 := root.SplitLeaf("b", SplitHorizontal)
	placeholder2.Pane = newTestPane("c")

	leaves := root.Leaves()
	got := paneIDs(leaves)
	want := []string{"a", "b", "c"}
	if !sliceEqual(got, want) {
		t.Fatalf("Leaves: got %v, want %v", got, want)
	}
}

func TestFindLeafHit(t *testing.T) {
	root := NewLeaf(newTestPane("x"))
	placeholder := root.SplitLeaf("x", SplitHorizontal)
	placeholder.Pane = newTestPane("y")

	if leaf := root.FindLeaf("y"); leaf == nil {
		t.Fatal("FindLeaf(y) returned nil")
	} else if leaf.Pane.ID != "y" {
		t.Fatalf("FindLeaf(y).Pane.ID = %q", leaf.Pane.ID)
	}
}

func TestFindLeafMiss(t *testing.T) {
	root := NewLeaf(newTestPane("x"))
	if root.FindLeaf("nope") != nil {
		t.Fatal("FindLeaf should return nil for missing ID")
	}
}

func TestPaneIDs(t *testing.T) {
	root := NewLeaf(newTestPane("a"))
	placeholder := root.SplitLeaf("a", SplitVertical)
	placeholder.Pane = newTestPane("b")

	ids := root.PaneIDs()
	if !ids["a"] || !ids["b"] || len(ids) != 2 {
		t.Fatalf("PaneIDs: got %v, want {a, b}", ids)
	}
}

func TestResizeHorizontal(t *testing.T) {
	pa := NewPaneModel("a", 1024)
	pb := NewPaneModel("b", 1024)

	root := NewLeaf(pa)
	placeholder := root.SplitLeaf("a", SplitHorizontal)
	placeholder.Pane = pb

	resizeNode(root, 100, 40)

	if pa.Width != 50 {
		t.Errorf("pane a width: got %d, want 50", pa.Width)
	}
	if pb.Width != 50 {
		t.Errorf("pane b width: got %d, want 50", pb.Width)
	}
	if pa.Height != 40 || pb.Height != 40 {
		t.Errorf("heights: a=%d, b=%d, want 40", pa.Height, pb.Height)
	}
}

func TestResizeVertical(t *testing.T) {
	pa := NewPaneModel("a", 1024)
	pb := NewPaneModel("b", 1024)

	root := NewLeaf(pa)
	placeholder := root.SplitLeaf("a", SplitVertical)
	placeholder.Pane = pb

	resizeNode(root, 80, 40)

	if pa.Height != 20 {
		t.Errorf("pane a height: got %d, want 20", pa.Height)
	}
	if pb.Height != 20 {
		t.Errorf("pane b height: got %d, want 20", pb.Height)
	}
	if pa.Width != 80 || pb.Width != 80 {
		t.Errorf("widths: a=%d, b=%d, want 80", pa.Width, pb.Width)
	}
}

func TestResizeClampsMinimum(t *testing.T) {
	pa := NewPaneModel("a", 1024)

	root := NewLeaf(pa)
	resizeNode(root, 5, 2)

	if pa.Width < minPaneW {
		t.Errorf("width %d < minimum %d", pa.Width, minPaneW)
	}
	if pa.Height < minPaneH {
		t.Errorf("height %d < minimum %d", pa.Height, minPaneH)
	}
}

func TestSplitAtPane(t *testing.T) {
	root := NewLeaf(newTestPane("a"))
	placeholder := root.SplitLeaf("a", SplitHorizontal)

	if root.IsLeaf() {
		t.Fatal("root should be internal after split")
	}
	if root.Split != SplitHorizontal {
		t.Fatalf("split dir: got %d, want SplitHorizontal", root.Split)
	}
	if root.Left == nil || !root.Left.IsLeaf() || root.Left.Pane.ID != "a" {
		t.Fatal("left child should be leaf 'a'")
	}
	if placeholder == nil || placeholder.Pane != nil {
		t.Fatal("placeholder should have nil Pane")
	}
	if root.Right != placeholder {
		t.Fatal("right child should be the placeholder")
	}
}

func TestRemovePane(t *testing.T) {
	root := NewLeaf(newTestPane("a"))
	placeholder := root.SplitLeaf("a", SplitVertical)
	placeholder.Pane = newTestPane("b")

	ok := root.RemoveLeaf("b")
	if !ok {
		t.Fatal("RemoveLeaf should succeed")
	}
	if !root.IsLeaf() {
		t.Fatal("root should be leaf after removing sibling")
	}
	if root.Pane.ID != "a" {
		t.Fatalf("surviving pane: got %q, want 'a'", root.Pane.ID)
	}
}

func TestRemoveRootLeafFails(t *testing.T) {
	root := NewLeaf(newTestPane("only"))
	if root.RemoveLeaf("only") {
		t.Fatal("should not be able to remove the only leaf")
	}
}

func TestRemoveDeepPanePromotesSibling(t *testing.T) {
	// Tree: V(a, H(b, c)) — remove c, should get V(a, b)
	root := NewLeaf(newTestPane("a"))
	ph := root.SplitLeaf("a", SplitVertical)
	ph.Pane = newTestPane("b")
	ph2 := root.SplitLeaf("b", SplitHorizontal)
	ph2.Pane = newTestPane("c")

	ok := root.RemoveLeaf("c")
	if !ok {
		t.Fatal("RemoveLeaf(c) should succeed")
	}

	leaves := root.Leaves()
	got := paneIDs(leaves)
	want := []string{"a", "b"}
	if !sliceEqual(got, want) {
		t.Fatalf("after remove c: got %v, want %v", got, want)
	}
}

func TestFillPlaceholder(t *testing.T) {
	root := NewLeaf(newTestPane("a"))
	root.SplitLeaf("a", SplitHorizontal) // creates placeholder on right

	p := newTestPane("b")
	if !root.FillPlaceholder(p) {
		t.Fatal("FillPlaceholder should find the placeholder")
	}

	leaves := root.Leaves()
	got := paneIDs(leaves)
	want := []string{"a", "b"}
	if !sliceEqual(got, want) {
		t.Fatalf("after fill: got %v, want %v", got, want)
	}
}

func TestPrunePlaceholders(t *testing.T) {
	root := NewLeaf(newTestPane("a"))
	root.SplitLeaf("a", SplitHorizontal) // placeholder on right, never filled

	root.PrunePlaceholders()

	if !root.IsLeaf() {
		t.Fatal("after prune, root should be a leaf")
	}
	if root.Pane.ID != "a" {
		t.Fatalf("surviving pane: got %q, want 'a'", root.Pane.ID)
	}
}

func TestTabNextPrevPane(t *testing.T) {
	tab := NewTabModel("t1", "test")
	tab.Root = NewLeaf(newTestPane("a"))
	ph := tab.Root.SplitLeaf("a", SplitVertical)
	ph.Pane = newTestPane("b")
	tab.ActivePane = "a"

	tab.NextPane()
	if tab.ActivePane != "b" {
		t.Fatalf("after NextPane: got %q, want b", tab.ActivePane)
	}

	tab.NextPane()
	if tab.ActivePane != "a" {
		t.Fatalf("after wrapping NextPane: got %q, want a", tab.ActivePane)
	}

	tab.PrevPane()
	if tab.ActivePane != "b" {
		t.Fatalf("after PrevPane: got %q, want b", tab.ActivePane)
	}
}

func TestTabRemovePaneUpdatesActive(t *testing.T) {
	tab := NewTabModel("t1", "test")
	tab.Root = NewLeaf(newTestPane("a"))
	ph := tab.Root.SplitLeaf("a", SplitVertical)
	ph.Pane = newTestPane("b")
	tab.ActivePane = "b"

	tab.RemovePane("b")
	if tab.ActivePane != "a" {
		t.Fatalf("after removing active: got %q, want a", tab.ActivePane)
	}
}

func TestSerializeLayoutSingleLeaf(t *testing.T) {
	root := NewLeaf(newTestPane("a"))
	s := SerializeLayout(root)
	if s.PaneID != "a" {
		t.Fatalf("SerializeLayout leaf: got pane_id=%q, want 'a'", s.PaneID)
	}

	// Round-trip through JSON
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var s2 SerializedNode
	if err := json.Unmarshal(data, &s2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	panes := map[string]*PaneModel{"a": newTestPane("a")}
	rebuilt := DeserializeLayout(&s2, panes)
	if !rebuilt.IsLeaf() || rebuilt.Pane.ID != "a" {
		t.Fatal("round-trip failed: expected leaf 'a'")
	}
}

func TestSerializeLayoutTree(t *testing.T) {
	// Build: V(a, H(b, c))
	root := NewLeaf(newTestPane("a"))
	ph := root.SplitLeaf("a", SplitVertical)
	ph.Pane = newTestPane("b")
	ph2 := root.SplitLeaf("b", SplitHorizontal)
	ph2.Pane = newTestPane("c")

	data, err := MarshalLayout(root)
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}

	s, err := UnmarshalLayout(data)
	if err != nil {
		t.Fatalf("UnmarshalLayout: %v", err)
	}

	panes := map[string]*PaneModel{
		"a": newTestPane("a"),
		"b": newTestPane("b"),
		"c": newTestPane("c"),
	}
	rebuilt := DeserializeLayout(s, panes)
	leaves := rebuilt.Leaves()
	got := paneIDs(leaves)
	want := []string{"a", "b", "c"}
	if !sliceEqual(got, want) {
		t.Fatalf("round-trip tree: got %v, want %v", got, want)
	}

	// Verify split directions preserved.
	if rebuilt.Split != SplitVertical {
		t.Fatalf("root split: got %d, want SplitVertical", rebuilt.Split)
	}
	if rebuilt.Right.Split != SplitHorizontal {
		t.Fatalf("right child split: got %d, want SplitHorizontal", rebuilt.Right.Split)
	}
}

func TestDeserializeLayoutMissingPane(t *testing.T) {
	// Serialize a 2-pane tree, then deserialize with only 1 pane available.
	root := NewLeaf(newTestPane("a"))
	ph := root.SplitLeaf("a", SplitHorizontal)
	ph.Pane = newTestPane("b")

	data, err := MarshalLayout(root)
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}
	s, _ := UnmarshalLayout(data)

	// Only pane "a" exists.
	panes := map[string]*PaneModel{"a": newTestPane("a")}
	rebuilt := DeserializeLayout(s, panes)
	rebuilt.PrunePlaceholders()

	if !rebuilt.IsLeaf() {
		t.Fatal("expected single leaf after pruning missing pane")
	}
	if rebuilt.Pane.ID != "a" {
		t.Fatalf("surviving pane: got %q, want 'a'", rebuilt.Pane.ID)
	}
}

func TestDeserializeLayoutNil(t *testing.T) {
	s, err := UnmarshalLayout(nil)
	if err != nil {
		t.Fatalf("UnmarshalLayout(nil) error: %v", err)
	}
	if s != nil {
		t.Fatal("expected nil for empty input")
	}

	rebuilt := DeserializeLayout(nil, nil)
	if rebuilt != nil {
		t.Fatal("expected nil from DeserializeLayout(nil)")
	}
}

func TestFindPaneAtSingle(t *testing.T) {
	p := newTestPane("a")
	p.Width = 80
	p.Height = 24
	root := NewLeaf(p)

	// Click anywhere in bounds returns the pane.
	if got := root.FindPaneAt(0, 0, 0, 0, 80, 24); got == nil || got.ID != "a" {
		t.Fatal("expected pane 'a' at (0,0)")
	}
	if got := root.FindPaneAt(79, 23, 0, 0, 80, 24); got == nil || got.ID != "a" {
		t.Fatal("expected pane 'a' at (79,23)")
	}
	// Out of bounds returns nil.
	if got := root.FindPaneAt(80, 0, 0, 0, 80, 24); got != nil {
		t.Fatal("expected nil at x=80 (out of bounds)")
	}
	if got := root.FindPaneAt(0, 24, 0, 0, 80, 24); got != nil {
		t.Fatal("expected nil at y=24 (out of bounds)")
	}
}

func TestFindPaneAtHorizontalSplit(t *testing.T) {
	pa := newTestPane("a")
	pb := newTestPane("b")
	root := NewLeaf(pa)
	placeholder := root.SplitLeaf("a", SplitHorizontal)
	placeholder.Pane = pb
	root.Ratio = 0.5

	w, h := 100, 40

	// Click left half → pane a (leftW = 50)
	if got := root.FindPaneAt(0, 0, 0, 0, w, h); got == nil || got.ID != "a" {
		t.Fatalf("expected 'a' at x=0, got %v", got)
	}
	if got := root.FindPaneAt(49, 20, 0, 0, w, h); got == nil || got.ID != "a" {
		t.Fatalf("expected 'a' at x=49, got %v", got)
	}
	// Click right half → pane b
	if got := root.FindPaneAt(50, 0, 0, 0, w, h); got == nil || got.ID != "b" {
		t.Fatalf("expected 'b' at x=50, got %v", got)
	}
	if got := root.FindPaneAt(99, 39, 0, 0, w, h); got == nil || got.ID != "b" {
		t.Fatalf("expected 'b' at x=99, got %v", got)
	}
}

func TestFindPaneAtVerticalSplit(t *testing.T) {
	pa := newTestPane("a")
	pb := newTestPane("b")
	root := NewLeaf(pa)
	placeholder := root.SplitLeaf("a", SplitVertical)
	placeholder.Pane = pb
	root.Ratio = 0.5

	w, h := 80, 40

	// Click top half → pane a (topH = 20)
	if got := root.FindPaneAt(40, 0, 0, 0, w, h); got == nil || got.ID != "a" {
		t.Fatalf("expected 'a' at y=0, got %v", got)
	}
	if got := root.FindPaneAt(40, 19, 0, 0, w, h); got == nil || got.ID != "a" {
		t.Fatalf("expected 'a' at y=19, got %v", got)
	}
	// Click bottom half → pane b
	if got := root.FindPaneAt(40, 20, 0, 0, w, h); got == nil || got.ID != "b" {
		t.Fatalf("expected 'b' at y=20, got %v", got)
	}
	if got := root.FindPaneAt(40, 39, 0, 0, w, h); got == nil || got.ID != "b" {
		t.Fatalf("expected 'b' at y=39, got %v", got)
	}
}

func TestFindPaneAtWithOffset(t *testing.T) {
	// Simulate tab bar offset: pane area starts at y=1
	p := newTestPane("a")
	root := NewLeaf(p)

	// Click within offset bounds
	if got := root.FindPaneAt(10, 5, 0, 1, 80, 24); got == nil || got.ID != "a" {
		t.Fatal("expected pane 'a' within offset area")
	}
	// Click above the offset (tab bar area) → nil
	if got := root.FindPaneAt(10, 0, 0, 1, 80, 24); got != nil {
		t.Fatal("expected nil for click above offset (tab bar)")
	}
}

func TestFindPaneAtNilNode(t *testing.T) {
	var root *LayoutNode
	if got := root.FindPaneAt(0, 0, 0, 0, 80, 24); got != nil {
		t.Fatal("expected nil for nil node")
	}
}

// helpers

func paneIDs(panes []*PaneModel) []string {
	ids := make([]string, len(panes))
	for i, p := range panes {
		ids[i] = p.ID
	}
	return ids
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Spatial navigation tests ---

// buildTab creates a TabModel with a specific layout and sets an active pane.
// The caller provides a factory func that builds the layout tree.
//
// Note: we do NOT call resizeNode here because that triggers PaneModel.ResizeVT,
// which assumes the pane has an initialized VT emulator. Test panes don't have
// one (newTestPane keeps them bare). NavigateDirection works fine without it —
// CollectRects computes geometry from the tab's Width/Height top-down.
func buildTab(t *testing.T, w, h int, active string, makeRoot func() *LayoutNode) *TabModel {
	t.Helper()
	tab := NewTabModel("t1", "Test")
	tab.Root = makeRoot()
	tab.Width = w
	tab.Height = h
	tab.ActivePane = active
	return tab
}

func TestNavigateDirection_SinglePane_NoOp(t *testing.T) {
	tab := buildTab(t, 80, 24, "a", func() *LayoutNode {
		return NewLeaf(newTestPane("a"))
	})
	if tab.NavigateDirection(DirLeft) {
		t.Error("single pane: DirLeft should return false")
	}
	if tab.NavigateDirection(DirRight) {
		t.Error("single pane: DirRight should return false")
	}
	if tab.ActivePane != "a" {
		t.Errorf("single pane: ActivePane changed to %q", tab.ActivePane)
	}
}

func TestNavigateDirection_HorizontalSplit_LeftRight(t *testing.T) {
	// Layout: [a | b]  (side by side)
	tab := buildTab(t, 80, 24, "a", func() *LayoutNode {
		return &LayoutNode{
			Split: SplitHorizontal,
			Ratio: 0.5,
			Left:  NewLeaf(newTestPane("a")),
			Right: NewLeaf(newTestPane("b")),
		}
	})

	// From a, go right → b
	if !tab.NavigateDirection(DirRight) {
		t.Fatal("right from a should succeed")
	}
	if tab.ActivePane != "b" {
		t.Errorf("expected active = b after right, got %q", tab.ActivePane)
	}

	// From b, go left → a
	if !tab.NavigateDirection(DirLeft) {
		t.Fatal("left from b should succeed")
	}
	if tab.ActivePane != "a" {
		t.Errorf("expected active = a after left, got %q", tab.ActivePane)
	}

	// From a, up/down have no neighbor
	if tab.NavigateDirection(DirUp) {
		t.Error("up from a (no neighbor above) should return false")
	}
	if tab.NavigateDirection(DirDown) {
		t.Error("down from a (no neighbor below) should return false")
	}
}

func TestNavigateDirection_VerticalSplit_UpDown(t *testing.T) {
	// Layout: [a]  (stacked)
	//         [b]
	tab := buildTab(t, 80, 24, "a", func() *LayoutNode {
		return &LayoutNode{
			Split: SplitVertical,
			Ratio: 0.5,
			Left:  NewLeaf(newTestPane("a")),
			Right: NewLeaf(newTestPane("b")),
		}
	})

	if !tab.NavigateDirection(DirDown) {
		t.Fatal("down from top pane should succeed")
	}
	if tab.ActivePane != "b" {
		t.Errorf("expected active = b after down, got %q", tab.ActivePane)
	}

	if !tab.NavigateDirection(DirUp) {
		t.Fatal("up from bottom pane should succeed")
	}
	if tab.ActivePane != "a" {
		t.Errorf("expected active = a after up, got %q", tab.ActivePane)
	}

	if tab.NavigateDirection(DirLeft) {
		t.Error("left (no horizontal neighbor) should return false")
	}
}

func TestNavigateDirection_MixedGrid(t *testing.T) {
	// Layout:  +---+---+
	//          | a | b |
	//          +---+---+
	//          |   c   |
	//          +-------+
	//
	// Built as vertical split (top row / bottom c),
	// where the top row is a horizontal split of a | b.
	tab := buildTab(t, 80, 24, "a", func() *LayoutNode {
		topRow := &LayoutNode{
			Split: SplitHorizontal,
			Ratio: 0.5,
			Left:  NewLeaf(newTestPane("a")),
			Right: NewLeaf(newTestPane("b")),
		}
		return &LayoutNode{
			Split: SplitVertical,
			Ratio: 0.5,
			Left:  topRow,
			Right: NewLeaf(newTestPane("c")),
		}
	})

	// a → right → b
	if !tab.NavigateDirection(DirRight) || tab.ActivePane != "b" {
		t.Errorf("a → right expected b, got %q", tab.ActivePane)
	}
	// b → down → c (c spans the full bottom, so b's bottom edge sees c)
	if !tab.NavigateDirection(DirDown) || tab.ActivePane != "c" {
		t.Errorf("b → down expected c, got %q", tab.ActivePane)
	}
	// c → up → either a or b depending on which has more horizontal overlap
	// with c's position. c spans full width, so either is valid. Just check
	// that we moved somewhere upward.
	if !tab.NavigateDirection(DirUp) {
		t.Fatal("c → up should succeed")
	}
	if tab.ActivePane != "a" && tab.ActivePane != "b" {
		t.Errorf("c → up expected a or b, got %q", tab.ActivePane)
	}
}

func TestNavigateDirection_NoOverlap_Rejected(t *testing.T) {
	// Layout: [a]
	//         [b]
	// With a thin b offset on the right side (via nested split):
	//   +-----------+-----+
	//   |     a     |  c  |
	//   +-----------+-----+
	//   |         b       |
	//   +-----------------+
	//
	// From c, going down should hit b (b spans full width, so c's vertical
	// column does overlap b horizontally). Going up from c has no neighbor
	// because nothing is above c.
	tab := buildTab(t, 80, 24, "c", func() *LayoutNode {
		topRow := &LayoutNode{
			Split: SplitHorizontal,
			Ratio: 0.7,
			Left:  NewLeaf(newTestPane("a")),
			Right: NewLeaf(newTestPane("c")),
		}
		return &LayoutNode{
			Split: SplitVertical,
			Ratio: 0.5,
			Left:  topRow,
			Right: NewLeaf(newTestPane("b")),
		}
	})

	if tab.NavigateDirection(DirUp) {
		t.Error("c → up (nothing above) should return false")
	}
	// c → down → b (b is directly below c in the column)
	if !tab.NavigateDirection(DirDown) || tab.ActivePane != "b" {
		t.Errorf("c → down expected b, got %q", tab.ActivePane)
	}
}

func TestNavigateDirection_FocusMode_IsNoOp(t *testing.T) {
	tab := buildTab(t, 80, 24, "a", func() *LayoutNode {
		return &LayoutNode{
			Split: SplitHorizontal,
			Ratio: 0.5,
			Left:  NewLeaf(newTestPane("a")),
			Right: NewLeaf(newTestPane("b")),
		}
	})
	tab.ToggleFocus() // enters focus mode

	if tab.NavigateDirection(DirRight) {
		t.Error("NavigateDirection in focus mode should return false")
	}
	if tab.ActivePane != "a" {
		t.Errorf("ActivePane changed during focus-mode nav: %q", tab.ActivePane)
	}
}

func TestNavigateDirection_AllCandidatesFailOverlap_Returns_False(t *testing.T) {
	// Layout:
	//   +-------+
	//   |   a   |   ← active, top half, full width
	//   +---+---+
	//   | b |   |   ← b is bottom-left only
	//   +---+ c +
	//   |     |   ← c spans bottom-right
	//   +-----+
	//
	// Construct it as: vertical(a, horizontal(b, c)). Then from the
	// PERSPECTIVE OF b, going "up" lands on a (full width, fully overlaps).
	// What we actually want to exercise is the "no candidate qualifies"
	// branch — the simplest way is a 2-pane vertical layout where active
	// is the bottom pane and we go "right": no candidate has horizontal
	// overlap because the only candidate IS above, not to the right.
	tab := buildTab(t, 80, 24, "b", func() *LayoutNode {
		return &LayoutNode{
			Split: SplitVertical,
			Ratio: 0.5,
			Left:  NewLeaf(newTestPane("a")),
			Right: NewLeaf(newTestPane("b")),
		}
	})
	// b is below a. Going right or left from b: no candidate qualifies
	// because a is not on the right or left half-plane.
	if tab.NavigateDirection(DirRight) {
		t.Error("DirRight from b: expected no candidate, got navigation")
	}
	if tab.NavigateDirection(DirLeft) {
		t.Error("DirLeft from b: expected no candidate, got navigation")
	}
	if tab.ActivePane != "b" {
		t.Errorf("ActivePane changed to %q on failed nav", tab.ActivePane)
	}
}

func TestNavigateDirection_TieBreaksByCenterDistance(t *testing.T) {
	// Layout (heights in rows):
	//   +-----+-----+
	//   |     |  b  |   b: top-right
	//   |  a  +-----+
	//   |     |  c  |   c: bottom-right
	//   +-----+-----+
	//
	// From a, going right: both b and c are valid candidates with the same
	// horizontal gap (zero — they share the border). With identical overlap
	// scores against a's full vertical extent, the third tie-breaker
	// (perpendicular center distance) decides. Since a's vertical center
	// equals the boundary between b and c, both are tied on that axis too —
	// in that case, deterministic order falls back to first-seen, which is
	// b (left subtree first in CollectRects).
	//
	// To make the tie-breaker actually MATTER, we set a's height to be the
	// FULL height and place it in a vertical sandwich where one candidate's
	// center is closer than the other's. Easiest version: make a span the
	// top 80% so its center sits on row ~10; b on top-right occupies rows
	// 0..3 (center 1.5) and c bottom-right rows 19..23 (center 21). a's
	// center is 12. abs(12-1.5) = 10.5 vs abs(12-21) = 9 — c wins.
	tab := buildTab(t, 100, 24, "a", func() *LayoutNode {
		// Right column split into b (top, ~16%) + c (bottom, ~84%) so c's
		// center is closer to a's center.
		rightCol := &LayoutNode{
			Split: SplitVertical,
			Ratio: 0.16,
			Left:  NewLeaf(newTestPane("b")),
			Right: NewLeaf(newTestPane("c")),
		}
		return &LayoutNode{
			Split: SplitHorizontal,
			Ratio: 0.5,
			Left:  NewLeaf(newTestPane("a")),
			Right: rightCol,
		}
	})

	if !tab.NavigateDirection(DirRight) {
		t.Fatal("DirRight should succeed")
	}
	if tab.ActivePane != "c" {
		t.Errorf("expected c (closer center) to win tie, got %q", tab.ActivePane)
	}
}

func TestRangeOverlap(t *testing.T) {
	cases := []struct {
		name       string
		a1, a2, b1, b2, want int
	}{
		{"identical", 0, 10, 0, 10, 10},
		{"partial overlap", 0, 10, 5, 15, 5},
		{"b contains a", 2, 8, 0, 10, 6},
		{"a contains b", 0, 10, 3, 7, 4},
		{"touching (no overlap)", 0, 10, 10, 20, 0},
		{"disjoint", 0, 10, 20, 30, 0},
		{"disjoint reverse", 20, 30, 0, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rangeOverlap(tc.a1, tc.a2, tc.b1, tc.b2); got != tc.want {
				t.Errorf("rangeOverlap(%d,%d,%d,%d) = %d, want %d",
					tc.a1, tc.a2, tc.b1, tc.b2, got, tc.want)
			}
		})
	}
}
