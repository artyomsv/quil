package tui

import (
	"encoding/json"

	"charm.land/lipgloss/v2"
)

// SplitDir determines how child nodes are arranged.
type SplitDir int

const (
	SplitHorizontal SplitDir = iota // children side-by-side (left | right)
	SplitVertical                   // children stacked (top / bottom)
)

// Minimum pane dimensions (including border).
const (
	minPaneW = 10
	minPaneH = 4
)

// LayoutNode is a binary tree node for pane layout.
// Leaf nodes hold a *PaneModel; internal nodes hold two children and a split direction.
type LayoutNode struct {
	Pane  *PaneModel // non-nil for leaf nodes
	Split SplitDir   // meaningful only for internal nodes
	Ratio float64    // fraction allocated to Left child (0.0–1.0)
	Left  *LayoutNode
	Right *LayoutNode
}

// NewLeaf creates a leaf node wrapping a pane.
func NewLeaf(pane *PaneModel) *LayoutNode {
	return &LayoutNode{Pane: pane, Ratio: 0.5}
}

// IsLeaf returns true if this node holds a pane (no children).
func (n *LayoutNode) IsLeaf() bool {
	return n.Pane != nil
}

// Leaves returns all panes via in-order traversal (left-to-right, top-to-bottom).
func (n *LayoutNode) Leaves() []*PaneModel {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return []*PaneModel{n.Pane}
	}
	return append(n.Left.Leaves(), n.Right.Leaves()...)
}

// FindLeaf returns the leaf node with the given pane ID, or nil.
func (n *LayoutNode) FindLeaf(paneID string) *LayoutNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		if n.Pane.ID == paneID {
			return n
		}
		return nil
	}
	if found := n.Left.FindLeaf(paneID); found != nil {
		return found
	}
	return n.Right.FindLeaf(paneID)
}

// PaneIDs returns the set of all pane IDs in the tree.
func (n *LayoutNode) PaneIDs() map[string]bool {
	ids := make(map[string]bool)
	for _, pane := range n.Leaves() {
		ids[pane.ID] = true
	}
	return ids
}

// findParent returns the parent of the node containing paneID, and whether
// the target is the left child (true) or right child (false).
// Returns nil if paneID is at the root or not found.
func (n *LayoutNode) findParent(paneID string) (parent *LayoutNode, isLeft bool) {
	if n == nil || n.IsLeaf() {
		return nil, false
	}
	if n.Left != nil && n.Left.IsLeaf() && n.Left.Pane.ID == paneID {
		return n, true
	}
	if n.Right != nil && n.Right.IsLeaf() && n.Right.Pane.ID == paneID {
		return n, false
	}
	if parent, isLeft = n.Left.findParent(paneID); parent != nil {
		return parent, isLeft
	}
	return n.Right.findParent(paneID)
}

// SplitLeaf replaces the leaf with paneID with an internal node.
// The existing pane becomes the Left child; a placeholder leaf (nil Pane)
// is created as the Right child and returned so the caller can fill it.
// Returns nil if paneID is not found.
func (n *LayoutNode) SplitLeaf(paneID string, dir SplitDir) *LayoutNode {
	leaf := n.FindLeaf(paneID)
	if leaf == nil {
		return nil
	}

	// Save the pane, then convert this leaf into an internal node.
	existingPane := leaf.Pane
	placeholder := &LayoutNode{Ratio: 0.5}

	leaf.Pane = nil
	leaf.Split = dir
	leaf.Ratio = 0.5
	leaf.Left = NewLeaf(existingPane)
	leaf.Right = placeholder

	return placeholder
}

// RemoveLeaf removes the leaf with paneID from the tree by promoting
// the sibling to the parent's position. Returns false if paneID is the
// sole root leaf or is not found.
func (n *LayoutNode) RemoveLeaf(paneID string) bool {
	if n == nil {
		return false
	}
	// Special case: root is a leaf — can't remove the only pane.
	if n.IsLeaf() {
		return false
	}

	// Find where the pane lives.
	parent, isLeft := n.findParentInternal(paneID)
	if parent == nil {
		return false
	}

	// Determine sibling.
	var sibling *LayoutNode
	if isLeft {
		sibling = parent.Right
	} else {
		sibling = parent.Left
	}

	// Promote sibling into parent's slot (in-place mutation).
	parent.Pane = sibling.Pane
	parent.Split = sibling.Split
	parent.Ratio = sibling.Ratio
	parent.Left = sibling.Left
	parent.Right = sibling.Right

	return true
}

// findParentInternal is like findParent but also searches internal node children.
func (n *LayoutNode) findParentInternal(paneID string) (parent *LayoutNode, isLeft bool) {
	if n == nil || n.IsLeaf() {
		return nil, false
	}
	// Check immediate children.
	if n.Left != nil {
		if n.Left.IsLeaf() && n.Left.Pane.ID == paneID {
			return n, true
		}
		if !n.Left.IsLeaf() {
			if p, l := n.Left.findParentInternal(paneID); p != nil {
				return p, l
			}
		}
	}
	if n.Right != nil {
		if n.Right.IsLeaf() && n.Right.Pane.ID == paneID {
			return n, false
		}
		if !n.Right.IsLeaf() {
			if p, l := n.Right.findParentInternal(paneID); p != nil {
				return p, l
			}
		}
	}
	return nil, false
}

// HasPlaceholder returns true if the tree contains a leaf with nil Pane.
func (n *LayoutNode) HasPlaceholder() bool {
	if n == nil {
		return false
	}
	if n.IsLeaf() {
		return false
	}
	if n.Left != nil && n.Left.Pane == nil && n.Left.Left == nil {
		return true
	}
	if n.Right != nil && n.Right.Pane == nil && n.Right.Left == nil {
		return true
	}
	return n.Left.HasPlaceholder() || n.Right.HasPlaceholder()
}

// FillPlaceholder finds the first placeholder leaf (nil Pane) and fills it.
// Returns true if a placeholder was found and filled.
func (n *LayoutNode) FillPlaceholder(pane *PaneModel) bool {
	if n == nil {
		return false
	}
	// Check if this node's children are placeholders.
	if n.Left != nil && n.Left.Pane == nil && n.Left.Left == nil {
		n.Left.Pane = pane
		return true
	}
	if n.Right != nil && n.Right.Pane == nil && n.Right.Left == nil {
		n.Right.Pane = pane
		return true
	}
	if n.Left != nil && n.Left.FillPlaceholder(pane) {
		return true
	}
	if n.Right != nil {
		return n.Right.FillPlaceholder(pane)
	}
	return false
}

// PrunePlaceholders removes any placeholder leaves (nil Pane) by promoting
// siblings. Returns true if the tree was modified.
func (n *LayoutNode) PrunePlaceholders() bool {
	if n == nil || n.IsLeaf() {
		return false
	}
	// Recurse first so we handle nested placeholders.
	n.Left.PrunePlaceholders()
	n.Right.PrunePlaceholders()

	// Check if either child is a placeholder.
	leftIsPlaceholder := n.Left != nil && n.Left.Pane == nil && n.Left.Left == nil
	rightIsPlaceholder := n.Right != nil && n.Right.Pane == nil && n.Right.Left == nil

	if leftIsPlaceholder {
		sibling := n.Right
		n.Pane = sibling.Pane
		n.Split = sibling.Split
		n.Ratio = sibling.Ratio
		n.Left = sibling.Left
		n.Right = sibling.Right
		return true
	}
	if rightIsPlaceholder {
		sibling := n.Left
		n.Pane = sibling.Pane
		n.Split = sibling.Split
		n.Ratio = sibling.Ratio
		n.Left = sibling.Left
		n.Right = sibling.Right
		return true
	}
	return false
}

// FindPaneAt returns the pane at screen coordinates (x, y), given the node's
// origin (ox, oy) and dimensions (w, h). Mirrors resizeNode() split logic.
func (n *LayoutNode) FindPaneAt(x, y, ox, oy, w, h int) *PaneModel {
	if n == nil {
		return nil
	}
	if x < ox || x >= ox+w || y < oy || y >= oy+h {
		return nil
	}
	if n.IsLeaf() {
		return n.Pane
	}

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
		if pane := n.Left.FindPaneAt(x, y, ox, oy, leftW, h); pane != nil {
			return pane
		}
		return n.Right.FindPaneAt(x, y, ox+leftW, oy, rightW, h)

	case SplitVertical:
		topH := int(float64(h) * n.Ratio)
		if topH < minPaneH {
			topH = minPaneH
		}
		bottomH := h - topH
		if bottomH < minPaneH {
			bottomH = minPaneH
		}
		if pane := n.Left.FindPaneAt(x, y, ox, oy, w, topH); pane != nil {
			return pane
		}
		return n.Right.FindPaneAt(x, y, ox, oy+topH, w, bottomH)
	}

	return nil
}

// PaneRect holds a pane and its screen-space rectangle.
type PaneRect struct {
	Pane         *PaneModel
	OX, OY, W, H int
}

// CollectRects walks the layout tree and appends a PaneRect for every leaf.
// Used by spatial pane navigation (TabModel.NavigateDirection) to pick the
// closest neighbor in a given direction without re-implementing the layout
// arithmetic.
func (n *LayoutNode) CollectRects(ox, oy, w, h int, out *[]PaneRect) {
	if n == nil {
		return
	}
	if n.IsLeaf() {
		if n.Pane != nil {
			*out = append(*out, PaneRect{Pane: n.Pane, OX: ox, OY: oy, W: w, H: h})
		}
		return
	}
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
		n.Left.CollectRects(ox, oy, leftW, h, out)
		n.Right.CollectRects(ox+leftW, oy, rightW, h, out)
	case SplitVertical:
		topH := int(float64(h) * n.Ratio)
		if topH < minPaneH {
			topH = minPaneH
		}
		bottomH := h - topH
		if bottomH < minPaneH {
			bottomH = minPaneH
		}
		n.Left.CollectRects(ox, oy, w, topH, out)
		n.Right.CollectRects(ox, oy+topH, w, bottomH, out)
	}
}

// FindPaneRectAt returns the pane and its screen rectangle at coordinates (x, y).
func (n *LayoutNode) FindPaneRectAt(x, y, ox, oy, w, h int) *PaneRect {
	if n == nil {
		return nil
	}
	if x < ox || x >= ox+w || y < oy || y >= oy+h {
		return nil
	}
	if n.IsLeaf() {
		if n.Pane == nil {
			return nil
		}
		return &PaneRect{Pane: n.Pane, OX: ox, OY: oy, W: w, H: h}
	}

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
		if r := n.Left.FindPaneRectAt(x, y, ox, oy, leftW, h); r != nil {
			return r
		}
		return n.Right.FindPaneRectAt(x, y, ox+leftW, oy, rightW, h)

	case SplitVertical:
		topH := int(float64(h) * n.Ratio)
		if topH < minPaneH {
			topH = minPaneH
		}
		bottomH := h - topH
		if bottomH < minPaneH {
			bottomH = minPaneH
		}
		if r := n.Left.FindPaneRectAt(x, y, ox, oy, w, topH); r != nil {
			return r
		}
		return n.Right.FindPaneRectAt(x, y, ox, oy+topH, w, bottomH)
	}

	return nil
}

// resizeNode recursively assigns dimensions to each node.
func resizeNode(n *LayoutNode, w, h int) {
	if n == nil {
		return
	}
	// Placeholder node (nil Pane, no children) — skip until filled.
	if n.Pane == nil && n.Left == nil && n.Right == nil {
		return
	}
	if n.IsLeaf() {
		if w < minPaneW {
			w = minPaneW
		}
		if h < minPaneH {
			h = minPaneH
		}
		n.Pane.Width = w
		n.Pane.Height = h
		n.Pane.ResizeVT(w-2, h-2)
		return
	}

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
		resizeNode(n.Left, leftW, h)
		resizeNode(n.Right, rightW, h)

	case SplitVertical:
		topH := int(float64(h) * n.Ratio)
		if topH < minPaneH {
			topH = minPaneH
		}
		bottomH := h - topH
		if bottomH < minPaneH {
			bottomH = minPaneH
		}
		resizeNode(n.Left, w, topH)
		resizeNode(n.Right, w, bottomH)
	}
}

// SerializedNode is a JSON-friendly representation of a LayoutNode tree.
// Leaf nodes have PaneID set; internal nodes have Split, Ratio, Left, Right.
type SerializedNode struct {
	PaneID string          `json:"pane_id,omitempty"`
	Split  *SplitDir       `json:"split,omitempty"`
	Ratio  float64         `json:"ratio,omitempty"`
	Left   *SerializedNode `json:"left,omitempty"`
	Right  *SerializedNode `json:"right,omitempty"`
}

// SerializeLayout converts a LayoutNode tree into a SerializedNode tree.
func SerializeLayout(n *LayoutNode) *SerializedNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return &SerializedNode{PaneID: n.Pane.ID}
	}
	split := n.Split
	return &SerializedNode{
		Split: &split,
		Ratio: n.Ratio,
		Left:  SerializeLayout(n.Left),
		Right: SerializeLayout(n.Right),
	}
}

// DeserializeLayout reconstructs a LayoutNode tree from a SerializedNode tree.
// Panes are looked up by ID from the provided map. Missing panes become
// placeholder nodes (nil Pane) that should be pruned by the caller.
func DeserializeLayout(s *SerializedNode, panes map[string]*PaneModel) *LayoutNode {
	if s == nil {
		return nil
	}
	if s.PaneID != "" {
		pane, ok := panes[s.PaneID]
		if !ok {
			// Missing pane → placeholder (will be pruned)
			return &LayoutNode{Ratio: 0.5}
		}
		return NewLeaf(pane)
	}
	split := SplitHorizontal
	if s.Split != nil {
		split = *s.Split
	}
	return &LayoutNode{
		Split: split,
		Ratio: s.Ratio,
		Left:  DeserializeLayout(s.Left, panes),
		Right: DeserializeLayout(s.Right, panes),
	}
}

// MarshalLayout serializes a LayoutNode tree to JSON.
func MarshalLayout(n *LayoutNode) (json.RawMessage, error) {
	s := SerializeLayout(n)
	return json.Marshal(s)
}

// UnmarshalLayout deserializes JSON into a SerializedNode tree.
func UnmarshalLayout(data json.RawMessage) (*SerializedNode, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var s SerializedNode
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// renderNode recursively produces the visual output for the layout tree.
func renderNode(n *LayoutNode) string {
	if n == nil {
		return ""
	}
	if n.IsLeaf() {
		return n.Pane.View()
	}

	leftView := renderNode(n.Left)
	rightView := renderNode(n.Right)

	switch n.Split {
	case SplitVertical:
		return lipgloss.JoinVertical(lipgloss.Left, leftView, rightView)
	default: // SplitHorizontal
		return lipgloss.JoinHorizontal(lipgloss.Top, leftView, rightView)
	}
}
