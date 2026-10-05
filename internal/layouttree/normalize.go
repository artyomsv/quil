package layouttree

// Normalize returns the tree a client should draw — and the daemon should
// store — for a tab: the stored tree with leaves for panes not in panes
// removed and panes it lacks placed by the arrival rule, or the template
// layout for a template tab with no stored tree. It never mutates stored and
// returns nil only when panes is empty.
//
// The arrival rule splits the FIRST leaf in tree order, stacked, with the new
// pane below. Every inner node of the result carries an explicit split.
func Normalize(stored *Node, panes []string, templateKeyword, templateMain string) *Node {
	if len(panes) == 0 {
		return nil
	}
	if stored == nil && templateKeyword != "" {
		// nil when the main pane is not listed: a template is not applied
		// until its anchor exists, and panes take the arrival rule meanwhile.
		main := -1
		for i, id := range panes {
			if id == templateMain {
				main = i
			}
		}
		if main >= 0 {
			return Template(templateKeyword, panes, main)
		}
	}
	listed := make(map[string]bool, len(panes))
	for _, id := range panes {
		listed[id] = true
	}
	tree := pruneWhere(Clone(stored), func(id string) bool { return listed[id] })
	present := make(map[string]bool)
	for _, id := range PaneIDs(tree) {
		present[id] = true
	}
	for _, id := range panes {
		if present[id] {
			continue
		}
		present[id] = true
		if tree == nil {
			tree = &Node{PaneID: id}
			continue
		}
		first := tree
		for first.PaneID == "" {
			first = first.Left
		}
		existing := &Node{PaneID: first.PaneID}
		*first = *inner(Vertical, 0.5, existing, &Node{PaneID: id})
	}
	return tree
}

// Template builds a template tab's initial tree. Pane order is kept within
// each region; main picks the anchor and is clamped to the first pane when
// out of range. nil for no panes.
func Template(keyword string, panes []string, main int) *Node {
	if len(panes) == 0 {
		return nil
	}
	if len(panes) == 1 {
		return &Node{PaneID: panes[0]}
	}
	if main < 0 || main >= len(panes) {
		main = 0
	}
	switch keyword {
	case "columns":
		return stack(panes, Horizontal)
	case "main-left", "main-top":
		rest := append([]string(nil), panes[:main]...)
		rest = append(rest, panes[main+1:]...)
		dir, other := Horizontal, Vertical
		if keyword == "main-top" {
			dir, other = Vertical, Horizontal
		}
		return inner(dir, 0.5, &Node{PaneID: panes[main]}, stack(rest, other))
	case "grid":
		// Fill the left column top-to-bottom, then the right; an odd last
		// pane spans both columns underneath them.
		pairs := len(panes) / 2
		top := inner(Horizontal, 0.5, stack(panes[:pairs], Vertical), stack(panes[pairs:2*pairs], Vertical))
		if len(panes)%2 == 0 {
			return top
		}
		return inner(Vertical, float64(pairs)/float64(pairs+1), top, &Node{PaneID: panes[len(panes)-1]})
	default:
		return stack(panes, Vertical)
	}
}

func stack(panes []string, dir SplitDir) *Node {
	if len(panes) == 1 {
		return &Node{PaneID: panes[0]}
	}
	return inner(dir, 1/float64(len(panes)), &Node{PaneID: panes[0]}, stack(panes[1:], dir))
}
