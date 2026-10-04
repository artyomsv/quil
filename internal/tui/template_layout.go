package tui

import "github.com/artyomsv/quil/internal/layouttree"

// splitForNewPane preserves ordinary tab insertion. Template tabs replace
// this temporary tree with their initial layout once the completed frame arrives.
func splitForNewPane(tab *TabModel, leaves []*PaneModel, pane *PaneModel) {
	// A tree with no leaves has nothing to split: the pane becomes the root.
	if tab.Root == nil || len(leaves) == 0 {
		tab.Root = NewLeaf(pane)
		tab.invalidateLeaves()
		return
	}
	tab.Root.SplitLeaf(leaves[0].ID, SplitVertical)
	tab.Root.FillPlaceholder(pane)
	tab.invalidateLeaves()
}

// templateLayout builds only the initial tree. Pane order is preserved within
// each region; main selects an anchor without changing creation/prompt order.
// The shape is layouttree.Template's, so the daemon and the TUI build the same
// tree; the leaves are the given pane models.
func templateLayout(keyword string, panes []*PaneModel, main int) *LayoutNode {
	ids := make([]string, len(panes))
	byID := make(map[string]*PaneModel, len(panes))
	for i, p := range panes {
		ids[i] = p.ID
		byID[p.ID] = p
	}
	return DeserializeLayout(layouttree.Template(keyword, ids, main), byID)
}

// applyTemplateLayout reports whether it built the tree — the one moment a
// template tab's layout is first worth storing.
func applyTemplateLayout(tab *TabModel, info TabInfo, paneMap map[string]*PaneInfo) bool {
	if !tab.templateLayoutPending {
		return false
	}
	panes := make([]*PaneModel, 0, len(info.Panes))
	main := -1
	for i, id := range info.Panes {
		meta := paneMap[id]
		if meta == nil || meta.TabID != tab.ID || meta.PreparingWorktree != "" || meta.Overlay {
			return false
		}
		leaf := tab.Root.FindLeaf(id)
		if leaf == nil || leaf.Pane == nil {
			return false
		}
		panes = append(panes, leaf.Pane)
		if id == info.TemplateMain {
			main = i
		}
	}
	// The preparing and swap frames lack a final anchor. Do not consume the
	// keyword or publish a layout containing their soon-to-be-replaced pane.
	if main < 0 {
		return false
	}
	tab.Root = templateLayout(info.TemplateLayout, panes, main)
	tab.invalidateLeaves()
	tab.templateLayoutApplied, tab.templateLayoutPending = true, false
	return true
}
