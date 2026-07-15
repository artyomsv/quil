# Mouse Pane Resize Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Click-and-drag a split border between panes to resize both subtrees, with per-subtree minimum-size clamping, a transient border highlight, and PTY resize deferred to mouse release.

**Architecture:** TUI-only (approach A from the spec). Drag mutates `LayoutNode.Ratio` locally; `MsgResizePane` + `MsgUpdateLayout` fire once on release via the existing `resizeAllPanes()`/`sendAllLayouts()` helpers. No daemon, IPC, or persistence changes.

**Tech Stack:** Go 1.25, Bubble Tea v2, Lipgloss v2. Tests run in Docker (no local Go).

**Spec:** `docs/superpowers/specs/2026-07-15-mouse-pane-resize-design.md`

## Global Constraints

- **Git discipline (user rule):** NO intermediate commits. One final commit at the end of the whole plan. Stage files by explicit path only — never `git add -A` (the user keeps unrelated work staged).
- **No AI attribution** anywhere in the commit message.
- **Execution isolation:** work in a git worktree branched from `master` (`superpowers:using-git-worktrees`) — the main tree has the user's in-flight work on `ci/harden-hang-timeouts`.
- **Production isolation:** never touch `~/.quil/` or the production daemon. Manual verification only via dev mode.
- **Test command** (repo root; `./scripts/dev.sh test` may fail on CRLF — use the direct form):

```bash
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine \
  go test ./internal/tui/ -run '<TestName>' -v
```

- Go files use tabs; `gofmt` formatting is mandatory.
- Existing constants reused verbatim: `minPaneW = 10`, `minPaneH = 4` (`internal/tui/layout.go:18-21`), `chromeHeight = 2` (`internal/tui/model.go:29`).

---

### Task 1: Layout primitives — subtree minimums + border collection

**Files:**
- Modify: `internal/tui/layout.go` (append after `CollectRects`, ~line 353)
- Test: `internal/tui/layout_test.go` (append)

**Interfaces:**
- Consumes: existing `LayoutNode`, `minPaneW`, `minPaneH`.
- Produces (Task 2 and 3 rely on these exact signatures):
  - `type BorderHit struct { Node *LayoutNode; OX, OY, W, H int }`
  - `func (n *LayoutNode) CollectBorders(ox, oy, w, h int, out *[]BorderHit)`
  - `func (b BorderHit) Contains(x, y int) bool`
  - `func (b BorderHit) boundary() int`
  - `func (n *LayoutNode) minWidth() int`
  - `func (n *LayoutNode) minHeight() int`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/layout_test.go`:

```go
// buildHSplitTree returns root = H-split of two leaves (a | b), ratio 0.5.
func buildHSplitTree() (*LayoutNode, *PaneModel, *PaneModel) {
	a := newTestPane("a")
	b := newTestPane("b")
	root := NewLeaf(a)
	root.SplitLeaf("a", SplitHorizontal)
	root.Right.Pane = b
	return root, a, b
}

func TestLayoutNode_MinWidth(t *testing.T) {
	t.Parallel()
	leaf := NewLeaf(newTestPane("a"))
	if got := leaf.minWidth(); got != minPaneW {
		t.Errorf("leaf minWidth = %d, want %d", got, minPaneW)
	}
	if got := leaf.minHeight(); got != minPaneH {
		t.Errorf("leaf minHeight = %d, want %d", got, minPaneH)
	}

	// H-split: widths sum, heights take the max.
	h, _, _ := buildHSplitTree()
	if got := h.minWidth(); got != 2*minPaneW {
		t.Errorf("H-split minWidth = %d, want %d", got, 2*minPaneW)
	}
	if got := h.minHeight(); got != minPaneH {
		t.Errorf("H-split minHeight = %d, want %d", got, minPaneH)
	}

	// V-split: heights sum, widths take the max.
	v := NewLeaf(newTestPane("a"))
	v.SplitLeaf("a", SplitVertical)
	v.Right.Pane = newTestPane("b")
	if got := v.minWidth(); got != minPaneW {
		t.Errorf("V-split minWidth = %d, want %d", got, minPaneW)
	}
	if got := v.minHeight(); got != 2*minPaneH {
		t.Errorf("V-split minHeight = %d, want %d", got, 2*minPaneH)
	}

	// Nested: H-split whose left child is itself an H-split of two leaves.
	n, _, _ := buildHSplitTree()
	n.Left = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left: NewLeaf(newTestPane("c")), Right: NewLeaf(newTestPane("d"))}
	if got := n.minWidth(); got != 3*minPaneW {
		t.Errorf("nested minWidth = %d, want %d", got, 3*minPaneW)
	}
}

func TestLayoutNode_CollectBorders(t *testing.T) {
	t.Parallel()

	// Single leaf: no borders.
	leaf := NewLeaf(newTestPane("a"))
	var none []BorderHit
	leaf.CollectBorders(0, 1, 100, 38, &none)
	if len(none) != 0 {
		t.Fatalf("leaf borders = %d, want 0", len(none))
	}

	// H-split at ratio 0.5 in a 100x38 region at origin (0,1):
	// boundary column = 0 + int(100*0.5) = 50; line cells are x=49 and x=50.
	h, _, _ := buildHSplitTree()
	var hits []BorderHit
	h.CollectBorders(0, 1, 100, 38, &hits)
	if len(hits) != 1 {
		t.Fatalf("H-split borders = %d, want 1", len(hits))
	}
	if hits[0].Node != h {
		t.Error("BorderHit.Node should be the internal node")
	}
	if got := hits[0].boundary(); got != 50 {
		t.Errorf("boundary = %d, want 50", got)
	}
	for _, tc := range []struct {
		x, y int
		want bool
	}{
		{49, 10, true},  // left cell of the 2-cell line
		{50, 10, true},  // right cell
		{48, 10, false}, // one column too far left
		{51, 10, false}, // one column too far right
		{50, 0, false},  // above the region (tab bar row)
		{50, 39, false}, // below the region
	} {
		if got := hits[0].Contains(tc.x, tc.y); got != tc.want {
			t.Errorf("Contains(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}

	// Nested mixed splits: root V-split; top child is an H-split.
	// Region 100x38 at (0,1): root boundary row = 1 + int(38*0.5) = 20.
	// Top child region is 100x19 at (0,1): inner boundary col = int(100*0.5) = 50.
	root := &LayoutNode{Split: SplitVertical, Ratio: 0.5}
	inner := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left: NewLeaf(newTestPane("a")), Right: NewLeaf(newTestPane("b"))}
	root.Left = inner
	root.Right = NewLeaf(newTestPane("c"))
	var nested []BorderHit
	root.CollectBorders(0, 1, 100, 38, &nested)
	if len(nested) != 2 {
		t.Fatalf("nested borders = %d, want 2", len(nested))
	}
	// Parent emitted before child (reverse scan = deepest wins).
	if nested[0].Node != root || nested[1].Node != inner {
		t.Error("emission order must be parent then child")
	}
	if got := nested[0].boundary(); got != 20 {
		t.Errorf("root boundary row = %d, want 20", got)
	}
	if got := nested[1].boundary(); got != 50 {
		t.Errorf("inner boundary col = %d, want 50", got)
	}
	// Inner line only spans the top child's rows (1..19 inclusive).
	if nested[1].Contains(50, 25) {
		t.Error("inner H line must not extend below the top child's region")
	}
	if !nested[1].Contains(50, 10) {
		t.Error("inner H line should contain (50,10)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine \
  go test ./internal/tui/ -run 'TestLayoutNode_MinWidth|TestLayoutNode_CollectBorders' -v
```

Expected: compile error — `undefined: BorderHit`, `n.minWidth undefined`.

- [ ] **Step 3: Implement in `internal/tui/layout.go`**

Append after `CollectRects` (before `FindPaneRectAt`):

```go
// BorderHit describes one draggable split line: the internal node whose
// Ratio a drag mutates, plus the node's region captured at collection time
// so ratio math survives mid-drag layout changes.
type BorderHit struct {
	Node         *LayoutNode
	OX, OY, W, H int
}

// boundary returns the first cell of the Right child along the split axis
// (column for H-splits, row for V-splits). Mirrors resizeNode's clamp of
// the Left child's share — the Right child is always placed at this offset.
func (b BorderHit) boundary() int {
	switch b.Node.Split {
	case SplitVertical:
		topH := int(float64(b.H) * b.Node.Ratio)
		if topH < minPaneH {
			topH = minPaneH
		}
		return b.OY + topH
	default: // SplitHorizontal
		leftW := int(float64(b.W) * b.Node.Ratio)
		if leftW < minPaneW {
			leftW = minPaneW
		}
		return b.OX + leftW
	}
}

// Contains reports whether (x, y) lies on this node's split line. The line
// is 2 cells thick — the Left child's closing border cell and the Right
// child's opening border cell — across the node's full perpendicular extent.
func (b BorderHit) Contains(x, y int) bool {
	bd := b.boundary()
	switch b.Node.Split {
	case SplitVertical:
		return (y == bd-1 || y == bd) && x >= b.OX && x < b.OX+b.W
	default: // SplitHorizontal
		return (x == bd-1 || x == bd) && y >= b.OY && y < b.OY+b.H
	}
}

// CollectBorders walks the layout tree with the same arithmetic as
// CollectRects and appends one BorderHit per internal node. Parents are
// emitted before children, so a reverse scan finds the deepest split line
// under a point (T-junction resolution).
func (n *LayoutNode) CollectBorders(ox, oy, w, h int, out *[]BorderHit) {
	if n == nil || n.IsLeaf() {
		return
	}
	// Placeholder node (nil Pane, no children) — no split line.
	if n.Left == nil && n.Right == nil {
		return
	}
	*out = append(*out, BorderHit{Node: n, OX: ox, OY: oy, W: w, H: h})
	switch n.Split {
	case SplitHorizontal:
		leftW := int(float64(w) * n.Ratio)
		if leftW < minPaneW {
			leftW = minPaneW
		}
		rightW := w - leftW
		if rightW < minPaneW {
			rightW = minPaneW
		}
		n.Left.CollectBorders(ox, oy, leftW, h, out)
		n.Right.CollectBorders(ox+leftW, oy, rightW, h, out)
	case SplitVertical:
		topH := int(float64(h) * n.Ratio)
		if topH < minPaneH {
			topH = minPaneH
		}
		bottomH := h - topH
		if bottomH < minPaneH {
			bottomH = minPaneH
		}
		n.Left.CollectBorders(ox, oy, w, topH, out)
		n.Right.CollectBorders(ox, oy+topH, w, bottomH, out)
	}
}

// minWidth returns the smallest width this subtree can occupy without any
// leaf dropping below minPaneW. Placeholder leaves count as a full pane so
// a mid-fill tree never reports an impossible zero minimum.
func (n *LayoutNode) minWidth() int {
	if n == nil {
		return 0
	}
	if n.Left == nil && n.Right == nil { // leaf or placeholder
		return minPaneW
	}
	if n.Split == SplitHorizontal {
		return n.Left.minWidth() + n.Right.minWidth()
	}
	return max(n.Left.minWidth(), n.Right.minWidth())
}

// minHeight is the vertical counterpart of minWidth.
func (n *LayoutNode) minHeight() int {
	if n == nil {
		return 0
	}
	if n.Left == nil && n.Right == nil { // leaf or placeholder
		return minPaneH
	}
	if n.Split == SplitVertical {
		return n.Left.minHeight() + n.Right.minHeight()
	}
	return max(n.Left.minHeight(), n.Right.minHeight())
}
```

- [ ] **Step 4: Run tests to verify they pass**

Same command as Step 2. Expected: PASS (both tests, all subcases). Do NOT commit (single final commit at plan end).

---

### Task 2: Drag state machine — hit test, ratio drag, release commit

**Files:**
- Modify: `internal/tui/model.go` — new fields after `tabDragFromIdx` (~line 305), `clearDragState()` (~line 1303), new helpers after `hitTestScrollbar` (~line 1292)
- Test: Create `internal/tui/splitdrag_test.go`

**Interfaces:**
- Consumes (Task 1): `BorderHit`, `CollectBorders`, `Contains`, `minWidth()`, `minHeight()`.
- Consumes (existing): `m.activeTabModel()`, `chromeHeight`, `m.paneAreaWidth()`, `tab.FocusMode()`, `tab.Resize(w, h)`, `m.resizeAllPanes()`, `m.sendAllLayouts()`, `newModelForTest` + `fakeSender` + `newTestPane` + `NewPaneModel` test helpers.
- Produces (Task 3 and 4 rely on):
  - Model fields `splitDragNode *LayoutNode`, `splitDragRect BorderHit`
  - `func (m *Model) hitTestSplitBorder(x, y int) *BorderHit`
  - `func (m *Model) dragSplitBorder(x, y int)`
  - `func (m *Model) finishSplitDrag() tea.Cmd`
  - `func treeContains(n, target *LayoutNode) bool`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/splitdrag_test.go`:

```go
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
func newSplitDragTestModel(t *testing.T) *Model {
	t.Helper()
	m := newModelForTest([]string{"T"}, 0)
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
```

Also extend `TestModel_ClearDragState` in `internal/tui/tab_reorder_test.go`: add to the struct literal

```go
		splitDragNode: &LayoutNode{},
		splitDragRect: BorderHit{OX: 1, OY: 2, W: 3, H: 4},
```

and after the existing assertions:

```go
	if m.splitDragNode != nil {
		t.Error("splitDragNode should be nil after clearDragState")
	}
	if m.splitDragRect != (BorderHit{}) {
		t.Errorf("splitDragRect = %+v, want zero value", m.splitDragRect)
	}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine \
  go test ./internal/tui/ -run 'TestModel_HitTestSplitBorder|TestModel_DragSplitBorder|TestModel_FinishSplitDrag|TestModel_ClearDragState' -v
```

Expected: compile error — `m.splitDragNode undefined`, `m.hitTestSplitBorder undefined`.

- [ ] **Step 3: Implement in `internal/tui/model.go`**

3a. Fields — after the `tabDragFromIdx int` field (~line 305):

```go
	// Split-border drag-resize. splitDragNode is non-nil while a border
	// drag is in progress; splitDragRect captures the owning node's region
	// at click time so mid-drag layout changes can't drift the ratio math.
	// PTY resize + layout persistence are deferred to release
	// (finishSplitDrag) — mid-drag only the local tree and VT change.
	splitDragNode *LayoutNode
	splitDragRect BorderHit
```

3b. `clearDragState()` — add the two zeroings (the highlight-off call is added in Task 3):

```go
	m.splitDragNode = nil
	m.splitDragRect = BorderHit{}
```

3c. Helpers — append after `hitTestScrollbar` (~line 1292):

```go
// hitTestSplitBorder returns the deepest split line containing (x, y), or
// nil. Disabled in focus mode (one full-area pane — no inner borders),
// notes mode (implies focus mode plus a squeezed layout), and on tabs
// without splits (a leaf root emits no borders). Runs AFTER
// hitTestScrollbar at the call site so the scrollbar keeps priority where
// its widened hit zone overlaps a pane's right border.
func (m *Model) hitTestSplitBorder(x, y int) *BorderHit {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || tab.FocusMode() || m.notesMode {
		return nil
	}
	tabH := m.height - chromeHeight
	var borders []BorderHit
	tab.Root.CollectBorders(0, 1, m.paneAreaWidth(), tabH, &borders)
	for i := len(borders) - 1; i >= 0; i-- {
		if borders[i].Contains(x, y) {
			return &borders[i]
		}
	}
	return nil
}

// treeContains reports whether target is reachable from n (pointer
// identity). Guards a drag whose node was pruned or replaced by a
// workspace_state reconciliation mid-drag.
func treeContains(n, target *LayoutNode) bool {
	if n == nil {
		return false
	}
	if n == target {
		return true
	}
	return treeContains(n.Left, target) || treeContains(n.Right, target)
}

// dragSplitBorder maps the cursor to a new Ratio on the dragged node,
// clamped so every leaf in BOTH subtrees keeps its minimum size (nested
// splits included, via minWidth/minHeight). Clamping happens in cells and
// the ratio is derived from the clamped cell count — exact at the
// extremes, no float-truncation flicker. Local-only: the PTY resize and
// layout persistence fire on release (finishSplitDrag).
func (m *Model) dragSplitBorder(x, y int) {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || m.splitDragNode == nil ||
		!treeContains(tab.Root, m.splitDragNode) {
		m.clearDragState()
		return
	}
	node, rect := m.splitDragNode, m.splitDragRect
	switch node.Split {
	case SplitHorizontal:
		if rect.W <= 0 {
			return
		}
		leftW := min(max(x-rect.OX, node.Left.minWidth()), rect.W-node.Right.minWidth())
		node.Ratio = float64(leftW) / float64(rect.W)
	case SplitVertical:
		if rect.H <= 0 {
			return
		}
		topH := min(max(y-rect.OY, node.Left.minHeight()), rect.H-node.Right.minHeight())
		node.Ratio = float64(topH) / float64(rect.H)
	}
	tab.Resize(tab.Width, tab.Height)
}

// finishSplitDrag commits an in-progress border drag: the daemon gets the
// final pane sizes (one PTY resize per pane — children reflow once, per
// the on-release-only design) and every tab's layout blob (persists the
// new Ratio). resizeAllPanes/sendAllLayouts cover all panes/tabs; the
// daemon's same-size guard drops the untouched panes' resizes, and layout
// updates are stored opaquely without broadcast, so the extra breadth is
// harmless and reuses tested plumbing.
func (m *Model) finishSplitDrag() tea.Cmd {
	m.clearDragState()
	return tea.Batch(m.resizeAllPanes(), m.sendAllLayouts())
}
```

- [ ] **Step 4: Run tests to verify they pass**

Same command as Step 2. Expected: PASS (5 new tests + extended ClearDragState). Do NOT commit.

---

### Task 3: Drag highlight — transient border color on adjacent panes

**Files:**
- Modify: `internal/tui/pane.go` — new field on `PaneModel` (near `mcpHighlight` usage; add after `previewWrap` field, ~line 121), `paneRenderKey` struct + `renderKey()` (~lines 143-200), `View()` color chain (~line 738-750)
- Modify: `internal/tui/model.go` — `setSplitDragHighlight` helper next to the Task 2 helpers; hook into `clearDragState()`
- Test: append to `internal/tui/splitdrag_test.go`

**Interfaces:**
- Consumes (Task 1): `BorderHit.boundary()`, `CollectRects`.
- Consumes (Task 2): `m.splitDragNode`, `m.splitDragRect`, `clearDragState()`.
- Produces (Task 4 relies on): `func (m *Model) setSplitDragHighlight(hit *BorderHit, on bool)`; `PaneModel.splitDragHighlight bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/splitdrag_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine \
  go test ./internal/tui/ -run 'TestModel_SetSplitDragHighlight|TestModel_ClearDragState_ClearsHighlight|TestPaneModel_RenderKey_SplitDragHighlight' -v
```

Expected: compile error — `p.splitDragHighlight undefined`, `m.setSplitDragHighlight undefined`.

- [ ] **Step 3: Implement**

3a. `internal/tui/pane.go` — add the field to `PaneModel` after `previewWrap` (~line 121):

```go
	// splitDragHighlight marks this pane's border while a split-border
	// drag-resize touching this pane is in progress. Transient TUI state,
	// never persisted; set/cleared by Model.setSplitDragHighlight.
	splitDragHighlight bool
```

3b. `paneRenderKey` — add a field (grouped with the other bools):

```go
	splitDragHighlight             bool
```

and in `renderKey()` add:

```go
		splitDragHighlight: p.splitDragHighlight,
```

3c. `View()` color chain — insert between the ghost/resuming/preparing branch and the `mcpHighlight` branch (~line 747):

```go
	if p.splitDragHighlight {
		borderColor = lipgloss.Color("39") // bright blue — split drag in progress
	}
```

3d. `internal/tui/model.go` — helper next to `hitTestSplitBorder`, plus the `clearDragState` hook. In `clearDragState`, BEFORE the field zeroings added in Task 2:

```go
	if m.splitDragNode != nil {
		m.setSplitDragHighlight(&m.splitDragRect, false)
	}
```

New helper:

```go
// setSplitDragHighlight toggles the transient drag highlight on every leaf
// whose rect touches the dragged split line, on both sides of it.
// Adjacency is topological (which leaves border the line never changes as
// the ratio moves), so the same recomputation clears exactly the set it
// set — even after the drag moved the boundary.
func (m *Model) setSplitDragHighlight(hit *BorderHit, on bool) {
	if hit == nil || hit.Node == nil || hit.Node.IsLeaf() {
		return
	}
	bd := hit.boundary()
	var rects []PaneRect
	hit.Node.CollectRects(hit.OX, hit.OY, hit.W, hit.H, &rects)
	for i := range rects {
		if rects[i].Pane == nil {
			continue
		}
		var touches bool
		if hit.Node.Split == SplitHorizontal {
			touches = rects[i].OX == bd || rects[i].OX+rects[i].W == bd
		} else {
			touches = rects[i].OY == bd || rects[i].OY+rects[i].H == bd
		}
		if touches {
			rects[i].Pane.splitDragHighlight = on
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Same command as Step 2. Expected: PASS (3 new tests). Also re-run Task 2's command — `TestModel_FinishSplitDrag_CommitsToDaemon` still passes with the highlight hook in `clearDragState`. Do NOT commit.

---

### Task 4: Update() wiring, docs, full verification

**Files:**
- Modify: `internal/tui/model.go` — `MouseClickMsg` (~line 601-620), `MouseMotionMsg` (~line 644-650), `MouseReleaseMsg` (~line 677-687) branches
- Modify: `docs/features.md` (mouse feature list), `docs/keybindings.md` (mouse section), `.claude/CLAUDE.md` (Key Conventions bullet)
- Test: append to `internal/tui/splitdrag_test.go`

**Interfaces:**
- Consumes (Tasks 1-3): `hitTestSplitBorder`, `dragSplitBorder`, `finishSplitDrag`, `setSplitDragHighlight`, `splitDragNode`/`splitDragRect`, `clearDragState`.
- Produces: end-to-end behavior; nothing downstream.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/splitdrag_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine \
  go test ./internal/tui/ -run 'TestModel_SplitDrag_UpdateLifecycle' -v
```

Expected: FAIL — "click on the split line must arm the border drag" (hit test exists but nothing in Update calls it; the click falls through to text-selection arming).

- [ ] **Step 3: Wire into Update()**

3a. `MouseClickMsg` — in the `msg.Y < m.height-1` block, AFTER the scrollbar branch (`if rect := m.hitTestScrollbar(...) {...}`, ~line 601-608) and BEFORE the "Pane area — start tracking for drag selection" block:

```go
				// Split-border click arms a drag-resize. After the
				// scrollbar check so the scrollbar keeps priority where
				// its widened hit zone overlaps a pane's right border.
				if hit := m.hitTestSplitBorder(msg.X, msg.Y); hit != nil {
					m.clearDragState()
					m.splitDragNode = hit.Node
					m.splitDragRect = *hit
					m.selection = nil
					m.setSplitDragHighlight(hit, true)
					return m, nil
				}
```

3b. `MouseMotionMsg` — AFTER the `if m.scrollDragPaneID != "" {...}` branch (~line 644-650) and BEFORE the `notesMouseDown` branch:

```go
		if m.splitDragNode != nil {
			m.dragSplitBorder(msg.X, msg.Y)
			return m, nil
		}
```

3c. `MouseReleaseMsg` — BEFORE the `if m.tabDragFromIdx >= 0 || m.scrollDragPaneID != ""` branch (~line 680):

```go
		if m.splitDragNode != nil {
			return m, m.finishSplitDrag()
		}
```

- [ ] **Step 4: Run test to verify it passes**

Same command as Step 2. Expected: PASS.

- [ ] **Step 5: Docs**

- `docs/features.md` — add to the mouse/pane management feature area: "**Drag-resize splits** — click and drag any border between panes to resize; every pane keeps a 10×4 minimum, affected panes highlight while dragging, and child processes see one resize on release."
- `docs/keybindings.md` — mouse section: add a row "Click + drag split border | Resize adjacent panes (PTY resize applied on release)".
- `.claude/CLAUDE.md` — append one bullet to Key Conventions, after the "Mouse drag invariant" bullet: "Split-border drag-resize: `CollectBorders`/`BorderHit` (`internal/tui/layout.go`) enumerate split lines (2-cell hit zone per line, reverse scan = deepest wins); `hitTestSplitBorder`/`dragSplitBorder`/`finishSplitDrag` (`model.go`) arm/move/commit the drag — ratio clamped in cells against subtree minimums (`minWidth`/`minHeight`), PTY resize + layout persistence deferred to release (`resizeAllPanes` + `sendAllLayouts`), adjacent panes get a transient `splitDragHighlight` border (color 39, in `renderKey`). Disabled in focus/notes mode. Drag state rides `clearDragState()`."

- [ ] **Step 6: Full verification**

```bash
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine go vet ./...
docker run --rm -v "$(pwd):/src" -w /src -v quil-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false golang:1.25-alpine go test ./...
```

Expected: vet clean; ALL packages pass (not just internal/tui).

- [ ] **Step 7: Single final commit (stage by path only)**

```bash
git add internal/tui/layout.go internal/tui/layout_test.go \
        internal/tui/model.go internal/tui/pane.go \
        internal/tui/splitdrag_test.go internal/tui/tab_reorder_test.go \
        docs/features.md docs/keybindings.md .claude/CLAUDE.md \
        docs/superpowers/specs/2026-07-15-mouse-pane-resize-design.md \
        docs/superpowers/plans/2026-07-15-mouse-pane-resize.md
git commit -m "feat(tui): drag pane split borders with the mouse to resize

Click a border between panes and drag to move the split. The ratio is
clamped in cells against subtree minimums (10x4 per pane, nested splits
included), affected panes show a transient border highlight, and the PTY
resize + layout persistence fire once on mouse release to avoid resize
churn in child TUIs. Disabled in focus and notes modes; scrollbar clicks
keep priority where the hit zones overlap."
```

Verify with `git status` that no unrelated files are staged.

---

## Self-Review Notes

- **Spec coverage:** hit-testing (Task 1), minimums + clamp (Tasks 1-2), drag state machine + release commit (Task 2), highlight (Task 3), Update wiring + guards (Task 4), tests throughout, manual verification deferred to the finishing step. Release commit uses `resizeAllPanes`+`sendAllLayouts` (all panes/tabs) instead of the spec's "dragged node's leaves + one tab" — deliberate DRY deviation, noted in `finishSplitDrag`'s comment; the daemon's same-size guard makes it equivalent.
- **Type consistency:** `BorderHit` fields/methods, `splitDragNode/splitDragRect`, `setSplitDragHighlight(hit *BorderHit, on bool)` used identically in Tasks 2-4.
- **Boundary arithmetic cross-check:** `boundary()` mirrors `resizeNode`'s Left-share clamp only (the Right-share clamp never moves the child origin), matching `CollectRects` placement.
