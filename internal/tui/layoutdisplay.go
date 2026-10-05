package tui

import "github.com/artyomsv/quil/internal/layouttree"

// DisplayLayout returns the tree a client should DRAW for a tab: the stored
// tree with leaves for departed panes removed and panes it lacks placed by the
// arrival rule, or the template layout for a template tab with no stored tree.
// It is layouttree.Normalize, the shared core the daemon stores by; the
// browser's TypeScript port is held to the same vectors
// (internal/layouttree/testdata/layout_vectors.json). It never mutates stored,
// and it returns nil only when panes is empty.
func DisplayLayout(stored *SerializedNode, panes []string, templateKeyword, templateMain string) *SerializedNode {
	return layouttree.Normalize(stored, panes, templateKeyword, templateMain)
}
