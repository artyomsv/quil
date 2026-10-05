package layouttree

// SplitLeaf replaces target's leaf with an inner node of direction dir and
// ratio 0.5: target on the left/top, newID on the right/bottom — the TUI's
// own SplitLeaf order. False (and an unchanged copy) when target is absent.
func SplitLeaf(tree *Node, target string, dir SplitDir, newID string) (*Node, bool) {
	out := Clone(tree)
	l := findLeaf(out, target)
	if l == nil {
		return out, false
	}
	*l = *inner(dir, 0.5, &Node{PaneID: target}, &Node{PaneID: newID})
	return out, true
}

// Substitute renames oldID's leaf to newID in place: position, orientation
// and every ratio stay (spec AC-13). False when oldID is absent.
func Substitute(tree *Node, oldID, newID string) (*Node, bool) {
	out := Clone(tree)
	l := findLeaf(out, oldID)
	if l == nil {
		return out, false
	}
	l.PaneID = newID
	return out, true
}

// Prune removes id's leaf, promoting its sibling. The bool reports whether
// id was in the tree; a tree that loses its only leaf becomes nil.
func Prune(tree *Node, id string) (*Node, bool) {
	if !Contains(tree, id) {
		return Clone(tree), false
	}
	return pruneWhere(Clone(tree), func(p string) bool { return p != id }), true
}

// PlaceMoved places a pane that moved in from another tab: the LAST leaf in
// tree order splits against its parent's direction (left|right for a root
// leaf), the moved pane right/below. It is the TUI's spiral rule without
// the cell-size flip (arrivalSplitDir), which needs a screen the daemon does
// not have. A pane already in the tree is not placed again.
func PlaceMoved(tree *Node, id string) *Node {
	out := Clone(tree)
	if out == nil {
		return &Node{PaneID: id}
	}
	if Contains(out, id) {
		return out
	}
	var last, lastParent *Node
	var walk func(n, parent *Node)
	walk = func(n, parent *Node) {
		if n == nil {
			return
		}
		if n.PaneID != "" {
			last, lastParent = n, parent
			return
		}
		walk(n.Left, n)
		walk(n.Right, n)
	}
	walk(out, nil)
	if last == nil {
		return &Node{PaneID: id}
	}
	dir := Horizontal
	if lastParent != nil && lastParent.Split != nil && *lastParent.Split == Horizontal {
		dir = Vertical
	}
	*last = *inner(dir, 0.5, &Node{PaneID: last.PaneID}, &Node{PaneID: id})
	return out
}

// Sanitize is the daemon's check on a tree a client wrote: a leaf repeating
// an earlier leaf's pane (tree order) is dropped, its sibling promoted, and
// an inner node whose ratio is outside (0, 1) gets 0.5. A valid tree comes
// back Equal to its input.
func Sanitize(tree *Node) *Node {
	seen := map[string]bool{}
	out := pruneWhere(Clone(tree), func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		return true
	})
	var fix func(*Node)
	fix = func(n *Node) {
		if n == nil || n.PaneID != "" {
			return
		}
		if n.Ratio <= 0 || n.Ratio >= 1 {
			n.Ratio = 0.5
		}
		fix(n.Left)
		fix(n.Right)
	}
	fix(out)
	return out
}
