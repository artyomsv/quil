package tui

// DisplayLayout returns the tree a client should DRAW for a tab: the stored
// tree with leaves for departed panes removed and panes it lacks placed by the
// arrival rule, or the template layout for a template tab with no stored tree.
// It is the TUI's own placement for a pane nobody on this client asked for,
// extracted so a second client (the browser) can be held to the same result
// by shared test vectors. It never mutates stored, and it returns nil only
// when panes is empty.
//
// The arrival rule splits the FIRST leaf in tree order, stacked, with the new
// pane below — splitForNewPane's historical top|bottom split. Every inner node
// of the result carries an explicit split, as SerializeLayout always writes it.
func DisplayLayout(stored *SerializedNode, panes []string, templateKeyword, templateMain string) *SerializedNode {
	if len(panes) == 0 {
		return nil
	}
	if stored == nil && templateKeyword != "" {
		if t := templateDisplay(templateKeyword, panes, templateMain); t != nil {
			return t
		}
	}
	listed := make(map[string]bool, len(panes))
	for _, id := range panes {
		listed[id] = true
	}
	tree := pruneDisplay(cloneSerialized(stored), listed)
	present := make(map[string]bool)
	collectDisplayPanes(tree, present)
	for _, id := range panes {
		if present[id] {
			continue
		}
		present[id] = true
		if tree == nil {
			tree = &SerializedNode{PaneID: id}
			continue
		}
		first := firstDisplayLeaf(tree)
		stacked := SplitVertical
		existing := &SerializedNode{PaneID: first.PaneID}
		*first = SerializedNode{Split: &stacked, Ratio: 0.5, Left: existing, Right: &SerializedNode{PaneID: id}}
	}
	return tree
}

// cloneSerialized deep-copies a tree, giving an inner node with no split the
// side-by-side default.
func cloneSerialized(n *SerializedNode) *SerializedNode {
	if n == nil {
		return nil
	}
	c := &SerializedNode{PaneID: n.PaneID, Ratio: n.Ratio}
	if n.PaneID == "" {
		s := SplitHorizontal
		if n.Split != nil {
			s = *n.Split
		}
		c.Split = &s
	} else if n.Split != nil {
		s := *n.Split
		c.Split = &s
	}
	c.Left = cloneSerialized(n.Left)
	c.Right = cloneSerialized(n.Right)
	return c
}

// pruneDisplay drops leaves whose pane is not listed (and malformed internal
// nodes with a missing child), promoting the surviving sibling.
func pruneDisplay(n *SerializedNode, listed map[string]bool) *SerializedNode {
	if n == nil {
		return nil
	}
	if n.PaneID != "" {
		if listed[n.PaneID] {
			return n
		}
		return nil
	}
	n.Left = pruneDisplay(n.Left, listed)
	n.Right = pruneDisplay(n.Right, listed)
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

func collectDisplayPanes(n *SerializedNode, into map[string]bool) {
	if n == nil {
		return
	}
	if n.PaneID != "" {
		into[n.PaneID] = true
		return
	}
	collectDisplayPanes(n.Left, into)
	collectDisplayPanes(n.Right, into)
}

func firstDisplayLeaf(n *SerializedNode) *SerializedNode {
	for n.PaneID == "" {
		n = n.Left
	}
	return n
}

// templateDisplay mirrors templateLayout over SerializedNode. nil when the
// main pane is not listed: the TUI does not apply a template until its anchor
// exists, and places panes by the arrival rule meanwhile.
func templateDisplay(keyword string, panes []string, main string) *SerializedNode {
	mainIdx := -1
	for i, id := range panes {
		if id == main {
			mainIdx = i
		}
	}
	if mainIdx < 0 {
		return nil
	}
	models := make([]*PaneModel, len(panes))
	for i, id := range panes {
		models[i] = &PaneModel{ID: id}
	}
	return SerializeLayout(templateLayout(keyword, models, mainIdx))
}
