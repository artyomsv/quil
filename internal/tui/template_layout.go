package tui

// templateLayout builds only the initial tree. Pane order is preserved within
// each region; main selects an anchor without changing creation/prompt order.
func templateLayout(keyword string, panes []*PaneModel, main int) *LayoutNode {
	if len(panes) == 0 {
		return nil
	}
	if len(panes) == 1 {
		return NewLeaf(panes[0])
	}
	if main < 0 || main >= len(panes) {
		main = 0
	}
	switch keyword {
	case "columns":
		return templateStack(panes, SplitHorizontal)
	case "main-left", "main-top":
		rest := append([]*PaneModel(nil), panes[:main]...)
		rest = append(rest, panes[main+1:]...)
		dir, other := SplitHorizontal, SplitVertical
		if keyword == "main-top" {
			dir, other = SplitVertical, SplitHorizontal
		}
		return &LayoutNode{Split: dir, Ratio: 0.5, Left: NewLeaf(panes[main]), Right: templateStack(rest, other)}
	case "grid":
		// Fill the left column top-to-bottom, then the right; an odd last
		// pane spans both columns underneath them.
		pairs := len(panes) / 2
		top := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
			Left: templateStack(panes[:pairs], SplitVertical), Right: templateStack(panes[pairs:2*pairs], SplitVertical)}
		if len(panes)%2 == 0 {
			return top
		}
		return &LayoutNode{Split: SplitVertical, Ratio: float64(pairs) / float64(pairs+1), Left: top, Right: NewLeaf(panes[len(panes)-1])}
	default:
		return templateStack(panes, SplitVertical)
	}
}

func templateStack(panes []*PaneModel, dir SplitDir) *LayoutNode {
	if len(panes) == 1 {
		return NewLeaf(panes[0])
	}
	return &LayoutNode{Split: dir, Ratio: 1 / float64(len(panes)), Left: NewLeaf(panes[0]), Right: templateStack(panes[1:], dir)}
}

func applyTemplateLayout(tab *TabModel, info TabInfo, paneMap map[string]*PaneInfo) {
	if !tab.templateLayoutPending {
		return
	}
	panes := make([]*PaneModel, 0, len(info.Panes))
	main := -1
	for i, id := range info.Panes {
		meta := paneMap[id]
		if meta == nil || meta.TabID != tab.ID || meta.PreparingWorktree != "" || meta.Overlay {
			return
		}
		leaf := tab.Root.FindLeaf(id)
		if leaf == nil || leaf.Pane == nil {
			return
		}
		panes = append(panes, leaf.Pane)
		if id == info.TemplateMain {
			main = i
		}
	}
	// The preparing and swap frames lack a final anchor. Do not consume the
	// keyword or publish a layout containing their soon-to-be-replaced pane.
	if main < 0 {
		return
	}
	tab.Root = templateLayout(info.TemplateLayout, panes, main)
	tab.invalidateLeaves()
	tab.templateLayoutApplied, tab.templateLayoutPending = true, false
}
