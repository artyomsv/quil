// Package layouttree is the id-based core of a tab's pane layout: the binary
// split tree every client draws and the daemon stores, in the JSON shape
// {"pane_id"} for a leaf and {"split","ratio","left","right"} for an inner
// node. It knows pane ids only — no pane models, no cell geometry, no
// rendering — so the daemon, the TUI and the browser's TypeScript port share
// one rule set, held together by the vectors in testdata/.
//
// Every function treats its input as read-only and returns a fresh tree.
package layouttree

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// SplitDir is how an inner node arranges its two children.
type SplitDir int

const (
	Horizontal SplitDir = iota // children side by side (left | right)
	Vertical                   // children stacked (top / bottom)
)

// Node is one node of a stored tree. A leaf has PaneID; an inner node has
// Split, Ratio (the Left child's share) and both children.
type Node struct {
	PaneID string    `json:"pane_id,omitempty"`
	Split  *SplitDir `json:"split,omitempty"`
	Ratio  float64   `json:"ratio,omitempty"`
	Left   *Node     `json:"left,omitempty"`
	Right  *Node     `json:"right,omitempty"`
}

// Parse decodes a stored tree. Empty bytes and JSON null are "no tree"
// (nil, nil); bytes that do not decode are an error, which every caller
// treats as no tree as well.
func Parse(raw json.RawMessage) (*Node, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil, nil
	}
	var n Node
	if err := json.Unmarshal(t, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Marshal encodes a tree; nil (no tree) encodes as empty bytes, which is how
// the daemon stores "no layout".
func Marshal(n *Node) (json.RawMessage, error) {
	if n == nil {
		return nil, nil
	}
	return json.Marshal(n)
}

// Equal compares two trees structurally.
func Equal(a, b *Node) bool { return reflect.DeepEqual(a, b) }

// Clone deep-copies a tree, giving an inner node with no split the
// side-by-side default.
func Clone(n *Node) *Node {
	if n == nil {
		return nil
	}
	c := &Node{PaneID: n.PaneID, Ratio: n.Ratio}
	if n.PaneID == "" {
		s := Horizontal
		if n.Split != nil {
			s = *n.Split
		}
		c.Split = &s
	} else if n.Split != nil {
		s := *n.Split
		c.Split = &s
	}
	c.Left = Clone(n.Left)
	c.Right = Clone(n.Right)
	return c
}

// PaneIDs lists the tree's pane ids in tree order (left before right).
func PaneIDs(n *Node) []string {
	var out []string
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.PaneID != "" {
			out = append(out, n.PaneID)
			return
		}
		walk(n.Left)
		walk(n.Right)
	}
	walk(n)
	return out
}

// Contains reports whether a leaf of the tree names id.
func Contains(n *Node, id string) bool {
	return findLeaf(n, id) != nil
}

func findLeaf(n *Node, id string) *Node {
	if n == nil {
		return nil
	}
	if n.PaneID != "" {
		if n.PaneID == id {
			return n
		}
		return nil
	}
	if l := findLeaf(n.Left, id); l != nil {
		return l
	}
	return findLeaf(n.Right, id)
}

func inner(dir SplitDir, ratio float64, l, r *Node) *Node {
	d := dir
	return &Node{Split: &d, Ratio: ratio, Left: l, Right: r}
}

// pruneWhere drops every leaf keep refuses (and every inner node left with
// no child), promoting the surviving sibling. Leaves are visited in tree
// order. It edits n in place; callers pass a clone.
func pruneWhere(n *Node, keep func(id string) bool) *Node {
	if n == nil {
		return nil
	}
	if n.PaneID != "" {
		if keep(n.PaneID) {
			return n
		}
		return nil
	}
	n.Left = pruneWhere(n.Left, keep)
	n.Right = pruneWhere(n.Right, keep)
	switch {
	case n.Left == nil && n.Right == nil:
		return nil
	case n.Left == nil:
		return n.Right
	case n.Right == nil:
		return n.Left
	}
	return n
}
