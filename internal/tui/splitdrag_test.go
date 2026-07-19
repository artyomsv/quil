package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// newSplitDragTestModel builds a model with one tab holding an H-split
// (p1 | p2) at ratio 0.5, window 100x40 → tab area 100x38 at origin (0,1)
// (row 0 tab bar, last row status bar). Boundary line: columns 49-50.
// NewPaneModel (not newTestPane) because Resize drives the VT emulator.
// notifications is initialized (hidden, matching NewModel's real-world
// invariant) because m.Update's MouseClickMsg/MouseWheelMsg paths call
// sidebarSwallowsMouse unconditionally — a nil *NotificationCenter panics
// on the field read before any button/x/y branching happens.
func newSplitDragTestModel(t *testing.T) *Model {
	t.Helper()
	m := newModelForTest([]string{"T"}, 0)
	m.notifications = NewNotificationCenter(30, 200)
	tab := m.tabs[0]
	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	tab.Root = NewLeaf(p1)
	tab.Root.SplitLeaf("p1", SplitHorizontal)
	tab.Root.Right.Pane = p2
	tab.ActivePane = "p1"
	m.width, m.height = 100, 40
	tab.Resize(100, 38)
	return &m
}

func TestModel_HitTestSplitBorder(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)

	for _, tc := range []struct {
		name string
		x, y int
		want bool
	}{
		{"left line cell", 49, 10, true},
		{"right line cell", 50, 10, true},
		{"pane interior", 30, 10, false},
		{"tab bar row", 50, 0, false},
	} {
		got := m.hitTestSplitBorder(tc.x, tc.y)
		if (got != nil) != tc.want {
			t.Errorf("%s: hit = %v, want %v", tc.name, got != nil, tc.want)
		}
	}

	hit := m.hitTestSplitBorder(50, 10)
	if hit == nil || hit.Node != m.tabs[0].Root {
		t.Fatal("hit should resolve to the root split node")
	}

	// Guards: focus mode and notes mode disable the hit test entirely.
	m.tabs[0].ToggleFocus()
	if m.hitTestSplitBorder(50, 10) != nil {
		t.Error("focus mode must disable border hit test")
	}
	m.tabs[0].ExitFocus()
	m.notesMode = true
	if m.hitTestSplitBorder(50, 10) != nil {
		t.Error("notes mode must disable border hit test")
	}
	m.notesMode = false

	// Single-pane tab: no internal nodes, no hit.
	single := newModelForTest([]string{"S"}, 0)
	single.tabs[0].Root = NewLeaf(NewPaneModel("only", 1024))
	single.width, single.height = 100, 40
	if single.hitTestSplitBorder(50, 10) != nil {
		t.Error("single-pane tab must produce no border hits")
	}
}

func TestModel_DragSplitBorder(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	root := m.tabs[0].Root
	p1 := root.Left.Pane
	p2 := root.Right.Pane

	hit := m.hitTestSplitBorder(50, 10)
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit

	// Drag right: boundary follows the cursor exactly (clamp in cells).
	m.dragSplitBorder(70, 10)
	if root.Ratio != 0.7 {
		t.Errorf("Ratio = %v, want 0.7", root.Ratio)
	}
	if p1.Width != 70 || p2.Width != 30 {
		t.Errorf("widths = %d/%d, want 70/30", p1.Width, p2.Width)
	}

	// Clamp left: p1 floors at minPaneW.
	m.dragSplitBorder(2, 10)
	if p1.Width != minPaneW {
		t.Errorf("clamped left width = %d, want %d", p1.Width, minPaneW)
	}

	// Clamp right: p2 floors at minPaneW.
	m.dragSplitBorder(99, 10)
	if p2.Width != minPaneW {
		t.Errorf("clamped right width = %d, want %d", p2.Width, minPaneW)
	}
}

func TestModel_DragSplitBorder_NestedMinimum(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	root := m.tabs[0].Root
	// Split p1 again horizontally: root.Left becomes (p1 | p3), so the
	// left subtree's minimum width is 2*minPaneW.
	root.SplitLeaf("p1", SplitHorizontal)
	root.Left.Right.Pane = NewPaneModel("p3", 1024)
	m.tabs[0].Resize(100, 38)

	hit := m.hitTestSplitBorder(50, 10) // root line at col 49-50
	if hit == nil || hit.Node != root {
		t.Fatal("expected root border hit at column 50")
	}
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit

	m.dragSplitBorder(2, 10)
	if got := int(float64(100) * root.Ratio); got != 2*minPaneW {
		t.Errorf("nested clamp: left share = %d, want %d", got, 2*minPaneW)
	}
}

func TestModel_DragSplitBorder_NodeVanished(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	// Arm a drag on a node that is NOT in the tree (simulates a workspace
	// reconciliation that rebuilt/pruned the layout mid-drag).
	orphan := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left: NewLeaf(newTestPane("x")), Right: NewLeaf(newTestPane("y"))}
	m.splitDragNode = orphan
	m.splitDragRect = BorderHit{Node: orphan, OX: 0, OY: 1, W: 100, H: 38}

	m.dragSplitBorder(70, 10)
	if m.splitDragNode != nil {
		t.Error("drag on a vanished node must self-clear")
	}
	if orphan.Ratio != 0.5 {
		t.Error("vanished node's ratio must not be mutated")
	}
}

func TestModel_FinishSplitDrag_CommitsToDaemon(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	fs := &fakeSender{}
	m.client = fs

	hit := m.hitTestSplitBorder(50, 10)
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit
	m.dragSplitBorder(70, 10)

	cmd := m.finishSplitDrag()
	if m.splitDragNode != nil || m.splitDragRect != (BorderHit{}) {
		t.Error("finishSplitDrag must clear the drag state")
	}
	if cmd == nil {
		t.Fatal("finishSplitDrag must return the commit command")
	}
	// Execute the batch: resizeAllPanes + sendAllLayouts.
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	} else {
		t.Fatal("expected a tea.BatchMsg from finishSplitDrag")
	}
	var resizes, layouts int
	for _, sent := range fs.sent {
		switch sent.Type {
		case ipc.MsgResizePane:
			resizes++
		case ipc.MsgUpdateLayout:
			layouts++
		}
	}
	if resizes != 2 {
		t.Errorf("MsgResizePane count = %d, want 2", resizes)
	}
	if layouts != 1 {
		t.Errorf("MsgUpdateLayout count = %d, want 1", layouts)
	}
}

func TestModel_SetSplitDragHighlight(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	root := m.tabs[0].Root
	// Make the LEFT side nested: root.Left = V-split (p1 / p3). Both p1 and
	// p3 touch the root's vertical line with their right edges; p2 touches
	// it with its left edge — all three highlight. Dragging the INNER line
	// (between p1 and p3) must highlight only p1 and p3.
	root.Left = &LayoutNode{Split: SplitVertical, Ratio: 0.5,
		Left: root.Left, Right: NewLeaf(NewPaneModel("p3", 1024))}
	m.tabs[0].Resize(100, 38)
	p1 := root.Left.Left.Pane
	p3 := root.Left.Right.Pane
	p2 := root.Right.Pane

	// Root line: all three adjacent.
	hit := m.hitTestSplitBorder(50, 10)
	if hit == nil || hit.Node != root {
		t.Fatal("expected root border hit")
	}
	m.setSplitDragHighlight(hit, true)
	if !p1.splitDragHighlight || !p3.splitDragHighlight || !p2.splitDragHighlight {
		t.Error("all panes touching the root line should highlight")
	}
	m.setSplitDragHighlight(hit, false)
	if p1.splitDragHighlight || p3.splitDragHighlight || p2.splitDragHighlight {
		t.Error("highlight must clear on all panes")
	}

	// Inner line (row boundary inside the left half): only p1 and p3.
	// Left child region: 50x38 at (0,1) → inner boundary row = 1+int(38*0.5) = 20.
	innerHit := m.hitTestSplitBorder(20, 20)
	if innerHit == nil || innerHit.Node != root.Left {
		t.Fatal("expected inner border hit at row 20")
	}
	m.setSplitDragHighlight(innerHit, true)
	if !p1.splitDragHighlight || !p3.splitDragHighlight {
		t.Error("panes adjacent to the inner line should highlight")
	}
	if p2.splitDragHighlight {
		t.Error("p2 does not touch the inner line and must not highlight")
	}
}

func TestModel_ClearDragState_ClearsHighlight(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	hit := m.hitTestSplitBorder(50, 10)
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit
	m.setSplitDragHighlight(hit, true)

	m.clearDragState()
	for _, p := range m.tabs[0].Leaves() {
		if p.splitDragHighlight {
			t.Errorf("pane %s highlight must clear with the drag state", p.ID)
		}
	}
}

func TestPaneModel_RenderKey_SplitDragHighlight(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p", 1024)
	before := p.renderKey()
	p.splitDragHighlight = true
	after := p.renderKey()
	if before == after {
		t.Error("renderKey must change when splitDragHighlight flips (stale-frame guard)")
	}
}

// TestModel_SplitDrag_UpdateLifecycle drives the real Update() dispatch:
// click on the border arms the drag, motion moves the ratio, release
// clears state and returns the commit command.
func TestModel_SplitDrag_UpdateLifecycle(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	// Update() paths touch these; newModelForTest leaves them nil.
	m.notifications = NewNotificationCenter(30, 200)
	m.perfStats = newEventLoopStats()
	m.client = &fakeSender{}
	root := m.tabs[0].Root

	// Click on the split line (columns 49-50).
	next, _ := m.Update(tea.MouseClickMsg{X: 50, Y: 10, Button: tea.MouseLeft})
	nm := next.(Model)
	if nm.splitDragNode != root {
		t.Fatal("click on the split line must arm the border drag")
	}
	if !root.Left.Pane.splitDragHighlight || !root.Right.Pane.splitDragHighlight {
		t.Error("arming the drag must highlight the adjacent panes")
	}

	// Motion: boundary follows to column 70.
	next, _ = nm.Update(tea.MouseMotionMsg{X: 70, Y: 10, Button: tea.MouseLeft})
	nm = next.(Model)
	if root.Ratio != 0.7 {
		t.Errorf("Ratio after motion = %v, want 0.7", root.Ratio)
	}

	// Release: state cleared, commit command returned, highlight off.
	next, cmd := nm.Update(tea.MouseReleaseMsg{X: 70, Y: 10, Button: tea.MouseLeft})
	nm = next.(Model)
	if nm.splitDragNode != nil {
		t.Error("release must clear the drag state")
	}
	if cmd == nil {
		t.Error("release must return the daemon commit command")
	}
	if root.Left.Pane.splitDragHighlight || root.Right.Pane.splitDragHighlight {
		t.Error("release must clear the border highlight")
	}
}

// TestModel_DragSplitBorder_Vertical exercises the SplitVertical arm of
// dragSplitBorder (topH clamp + ratio derivation) — the vertical
// counterpart of TestModel_DragSplitBorder. Tab area height 32 keeps every
// expected ratio exactly representable (x/32), so int(float64(h)*Ratio)
// cannot lose a row to float truncation in the assertions.
func TestModel_DragSplitBorder_Vertical(t *testing.T) {
	t.Parallel()
	m := newModelForTest([]string{"T"}, 0)
	tab := m.tabs[0]
	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	tab.Root = NewLeaf(p1)
	tab.Root.SplitLeaf("p1", SplitVertical)
	tab.Root.Right.Pane = p2
	tab.ActivePane = "p1"
	m.width, m.height = 100, 34 // tab area 100x32 at (0,1); boundary row = 1+16 = 17
	tab.Resize(100, 32)
	root := tab.Root

	hit := m.hitTestSplitBorder(50, 17)
	if hit == nil || hit.Node != root {
		t.Fatal("expected root V-split border hit at row 17")
	}
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit

	// Drag down: boundary follows the cursor (topH = y - OY = 24).
	m.dragSplitBorder(50, 25)
	if root.Ratio != 0.75 {
		t.Errorf("Ratio = %v, want 0.75", root.Ratio)
	}
	if p1.Height != 24 || p2.Height != 8 {
		t.Errorf("heights = %d/%d, want 24/8", p1.Height, p2.Height)
	}

	// Clamp up: p1 floors at minPaneH.
	m.dragSplitBorder(50, 2)
	if p1.Height != minPaneH {
		t.Errorf("clamped top height = %d, want %d", p1.Height, minPaneH)
	}

	// Clamp down: p2 floors at minPaneH.
	m.dragSplitBorder(50, 40)
	if p2.Height != minPaneH {
		t.Errorf("clamped bottom height = %d, want %d", p2.Height, minPaneH)
	}
}

// TestModel_SplitDrag_ScrollbarKeepsPriority pins the border/scrollbar
// zone split in Update(): the DRAWN split line (both border glyphs) always
// arms the border drag — even the left glyph, which sits inside the left
// pane's widened scrollbar hit zone and used to be swallowed silently —
// while the scrollbar's own drawn column (scrollbarX) still wins for thumb
// clicks because the border zone never extends left of the drawn line.
func TestModel_SplitDrag_ScrollbarKeepsPriority(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.notifications = NewNotificationCenter(30, 200)
	m.perfStats = newEventLoopStats()
	m.client = &fakeSender{}

	// Left pane rect is (0,1,50,38): scrollbarX = 48, scrollbar hit zone
	// 47-49. x=49 is the split line's LEFT drawn glyph — border must win.
	next, _ := m.Update(tea.MouseClickMsg{X: 49, Y: 10, Button: tea.MouseLeft})
	nm := next.(Model)
	if nm.splitDragNode == nil {
		t.Error("the drawn split line's left glyph must arm the border drag")
	}
	if nm.scrollDragPaneID != "" {
		t.Errorf("scrollDragPaneID = %q, want empty (border wins its drawn line)", nm.scrollDragPaneID)
	}

	// x=48 is the scrollbar's drawn column — outside the border zone, the
	// scrollbar keeps it (thumb clicks still work at a split edge).
	next, _ = nm.Update(tea.MouseReleaseMsg{X: 49, Y: 10, Button: tea.MouseLeft})
	nm = next.(Model)
	next, _ = nm.Update(tea.MouseClickMsg{X: 48, Y: 10, Button: tea.MouseLeft})
	nm = next.(Model)
	if nm.splitDragNode != nil {
		t.Error("split drag must not arm on the scrollbar's drawn column")
	}
	if nm.scrollDragPaneID != "p1" {
		t.Errorf("scrollDragPaneID = %q, want %q (scrollbar owns its column)", nm.scrollDragPaneID, "p1")
	}
}

// TestModel_DragSplitBorder_VTResizeDeferredToRelease pins the fix for
// mid-drag content corruption: ResizeVT's contract (pane.go) pairs every
// emulator resize with a PTY redraw, but mid-drag the PTY resize is
// deferred — so the emulator must NOT be resized through intermediate
// widths (each rewrap garbles the grid with no child repaint to correct
// it, permanently at the narrowest width crossed). Mid-drag only the
// rects move; the single VT resize happens in finishSplitDrag, paired
// with the PTY resize.
func TestModel_DragSplitBorder_VTResizeDeferredToRelease(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.client = &fakeSender{}
	root := m.tabs[0].Root
	p1 := root.Left.Pane
	vtW, vtH := p1.vt.Width(), p1.vt.Height()

	hit := m.hitTestSplitBorder(50, 10)
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit

	m.dragSplitBorder(70, 10)
	if p1.Width != 70 {
		t.Errorf("mid-drag rect width = %d, want 70 (borders must move live)", p1.Width)
	}
	if p1.vt.Width() != vtW || p1.vt.Height() != vtH {
		t.Errorf("mid-drag VT = %dx%d, want unchanged %dx%d (emulator resize must defer to release)",
			p1.vt.Width(), p1.vt.Height(), vtW, vtH)
	}

	m.finishSplitDrag()
	if p1.vt.Width() == vtW {
		t.Errorf("post-release VT width = %d, want resized away from %d", p1.vt.Width(), vtW)
	}
}
