package tui

// Pure half of tab layout arrangement: "Layout…" in the tab menu, the
// palette's Layout rows, the tab.layout_* actions, and the two tree edits a
// pane drag drops into (panedrag.go).
//
// Every function returns a NEW tree built from the input's *PaneModel leaves
// and never mutates the input: applyTabArrangement may still refuse the result
// (a busy tab, not enough room), and a refused arrangement must leave the tab
// exactly as it was. Nothing here creates or disposes a PaneModel.

// layoutKind names one of the six arrangements.
type layoutKind int

const (
	layoutNone layoutKind = iota
	layoutEven
	layoutColumns
	layoutRows
	layoutGrid
	layoutMain
	layoutSpiral
)

// dropZone is where a dragged pane lands on the pane under the pointer:
// beside it on one side, or swapped with it (zoneCenter).
type dropZone int

const (
	zoneNone dropZone = iota
	zoneLeft
	zoneRight
	zoneTop
	zoneBottom
	zoneCenter
)

// cloneLayout deep-copies the tree's nodes. Leaves keep pointing at the SAME
// PaneModels — they are live panes and must never be duplicated — and a
// placeholder keeps its recorded rect and label.
func cloneLayout(n *LayoutNode) *LayoutNode {
	if n == nil {
		return nil
	}
	c := *n
	c.Left = cloneLayout(n.Left)
	c.Right = cloneLayout(n.Right)
	return &c
}

// evenOut returns a copy of root with the same shape and every split ratio
// set so each pane covers the same share of the tab: a split's Left gets
// paneCount(Left)/paneCount(node). Placeholder leaves count as none, and a
// split with no pane on one side keeps its ratio.
func evenOut(root *LayoutNode) *LayoutNode {
	out := cloneLayout(root)
	setEvenRatios(out)
	return out
}

// setEvenRatios applies evenOut's rule in place and returns the subtree's
// pane count.
func setEvenRatios(n *LayoutNode) int {
	switch {
	case n == nil:
		return 0
	case n.IsLeaf():
		return 1
	case n.Left == nil && n.Right == nil:
		return 0 // placeholder
	}
	l, r := setEvenRatios(n.Left), setEvenRatios(n.Right)
	if l > 0 && r > 0 {
		n.Ratio = float64(l) / float64(l+r)
	}
	return l + r
}

// paneLeaves wraps each pane in a fresh leaf node.
func paneLeaves(panes []*PaneModel) []*LayoutNode {
	out := make([]*LayoutNode, len(panes))
	for i, p := range panes {
		out[i] = NewLeaf(p)
	}
	return out
}

// chainNodes links nodes into a right-leaning chain split in dir. The node
// holding element i of n remaining gets Ratio 1/(n-i), so every element gets
// an equal share.
func chainNodes(nodes []*LayoutNode, dir SplitDir) *LayoutNode {
	switch len(nodes) {
	case 0:
		return nil
	case 1:
		return nodes[0]
	}
	return &LayoutNode{
		Split: dir,
		Ratio: 1 / float64(len(nodes)),
		Left:  nodes[0],
		Right: chainNodes(nodes[1:], dir),
	}
}

// columnsLayout puts every pane side by side (left|right), equal widths.
func columnsLayout(panes []*PaneModel) *LayoutNode {
	return chainNodes(paneLeaves(panes), SplitHorizontal)
}

// rowsLayout stacks every pane (top/bottom), equal heights.
func rowsLayout(panes []*PaneModel) *LayoutNode {
	return chainNodes(paneLeaves(panes), SplitVertical)
}

// gridColumns is ceil(sqrt(n)), in integers so a perfect square can never
// land one column off through float rounding.
func gridColumns(n int) int {
	c := 1
	for c*c < n {
		c++
	}
	return c
}

// gridLayout fills rows of gridColumns(n) panes in order — the last row takes
// the remainder — as a rows chain of columns chains: equal row heights, equal
// widths within a row.
func gridLayout(panes []*PaneModel) *LayoutNode {
	c := gridColumns(len(panes))
	var rows []*LayoutNode
	for i := 0; i < len(panes); i += c {
		rows = append(rows, columnsLayout(panes[i:min(i+c, len(panes))]))
	}
	return chainNodes(rows, SplitVertical)
}

// mainStackLayout puts main on the left at 0.5 and stacks the rest, in their
// order, on the right with equal heights. A main that is not in panes falls
// back to the first pane.
func mainStackLayout(panes []*PaneModel, main *PaneModel) *LayoutNode {
	if len(panes) == 0 {
		return nil
	}
	if main == nil {
		main = panes[0]
	}
	rest := make([]*PaneModel, 0, len(panes))
	found := false
	for _, p := range panes {
		if p.ID == main.ID {
			found = true
			continue
		}
		rest = append(rest, p)
	}
	if !found {
		main, rest = panes[0], panes[1:]
	}
	if len(rest) == 0 {
		return NewLeaf(main)
	}
	return &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(main), Right: rowsLayout(rest)}
}

// spiralLayout places the panes in order with the dwindle rule a moved pane
// uses (spiralLeaf + spiralSplitDir: split the last leaf against its parent,
// a root leaf left|right), then evens the result out. No narrow-leaf
// fallback: the apply step's fitsMinSize check covers a tab that is too small.
func spiralLayout(panes []*PaneModel) *LayoutNode {
	if len(panes) == 0 {
		return nil
	}
	root := NewLeaf(panes[0])
	for _, p := range panes[1:] {
		leaf, parentSplit, hasParent := root.spiralLeaf()
		root.SplitLeaf(leaf.Pane.ID, spiralSplitDir(parentSplit, hasParent)).fill(p)
	}
	return evenOut(root)
}

// arrangeLayout builds the tree for kind from root's panes in reading order;
// main + stack uses active as its main pane. nil for layoutNone or a nil root.
func arrangeLayout(kind layoutKind, root *LayoutNode, active *PaneModel) *LayoutNode {
	if root == nil {
		return nil
	}
	panes := root.Leaves()
	switch kind {
	case layoutEven:
		return evenOut(root)
	case layoutColumns:
		return columnsLayout(panes)
	case layoutRows:
		return rowsLayout(panes)
	case layoutGrid:
		return gridLayout(panes)
	case layoutMain:
		return mainStackLayout(panes, active)
	case layoutSpiral:
		return spiralLayout(panes)
	}
	return nil
}

// moveLeafBeside returns a copy of root with pane x taken out (RemoveLeaf
// promotes its sibling) and pane y split in zone's direction, x on the zone's
// side at 0.5. nil when zone is not an edge, x == y, either pane is absent, or
// x is the only pane.
func moveLeafBeside(root *LayoutNode, x, y *PaneModel, zone dropZone) *LayoutNode {
	var dir SplitDir
	switch zone {
	case zoneLeft, zoneRight:
		dir = SplitHorizontal
	case zoneTop, zoneBottom:
		dir = SplitVertical
	default:
		return nil
	}
	if root == nil || x == nil || y == nil || x.ID == y.ID {
		return nil
	}
	out := cloneLayout(root)
	if out.FindLeaf(y.ID) == nil || !out.RemoveLeaf(x.ID) {
		return nil
	}
	target := out.FindLeaf(y.ID)
	moved, stays := NewLeaf(x), NewLeaf(target.Pane)
	target.Pane = nil
	target.phType = ""
	target.Split = dir
	target.Ratio = 0.5
	if zone == zoneLeft || zone == zoneTop {
		target.Left, target.Right = moved, stays
	} else {
		target.Left, target.Right = stays, moved
	}
	return out
}

// swapLeaves returns a copy of root with the leaves of x and y exchanged —
// same shape, same ratios. nil when x == y or either pane is absent.
func swapLeaves(root *LayoutNode, x, y *PaneModel) *LayoutNode {
	if root == nil || x == nil || y == nil || x.ID == y.ID {
		return nil
	}
	out := cloneLayout(root)
	a, b := out.FindLeaf(x.ID), out.FindLeaf(y.ID)
	if a == nil || b == nil {
		return nil
	}
	a.Pane, b.Pane = b.Pane, a.Pane
	return out
}

// fitsMinSize reports whether every leaf of root — pane or placeholder — gets
// at least minPaneW x minPaneH cells in a w x h tab.
//
// It repeats CollectRects' arithmetic WITHOUT its clamps. CollectRects widens
// any child below the minimum back up to it, so every rect it returns reads as
// large enough while the rects together overflow the tab: five columns in 40
// cells come back as five 10-wide rects. Where no clamp would fire, the two
// walks agree cell for cell. Unknown geometry (w or h <= 0, before the first
// WindowSizeMsg) fits.
func fitsMinSize(root *LayoutNode, w, h int) bool {
	if w <= 0 || h <= 0 {
		return true
	}
	return fitsWalk(root, w, h)
}

func fitsWalk(n *LayoutNode, w, h int) bool {
	if n == nil {
		return true
	}
	if n.Left == nil && n.Right == nil {
		return w >= minPaneW && h >= minPaneH
	}
	switch n.Split {
	case SplitHorizontal:
		leftW := int(float64(w) * n.Ratio)
		return fitsWalk(n.Left, leftW, h) && fitsWalk(n.Right, w-leftW, h)
	case SplitVertical:
		topH := int(float64(h) * n.Ratio)
		return fitsWalk(n.Left, w, topH) && fitsWalk(n.Right, w, h-topH)
	}
	return true
}
