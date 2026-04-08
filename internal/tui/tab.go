package tui

// TabModel represents a single tab containing a tree of panes.
type TabModel struct {
	ID         string
	Name       string
	Color      string
	Root       *LayoutNode // binary split tree (nil = empty tab)
	ActivePane string      // pane ID of the active pane
	Width      int
	Height     int
	focusMode  bool // true = active pane fills entire tab
}

func NewTabModel(id, name string) *TabModel {
	return &TabModel{
		ID:   id,
		Name: name,
	}
}

// ActivePaneModel returns the currently active pane, or nil.
func (t *TabModel) ActivePaneModel() *PaneModel {
	if t.Root == nil {
		return nil
	}
	leaves := t.Root.Leaves()
	if len(leaves) == 0 {
		return nil
	}
	for _, p := range leaves {
		if p.ID == t.ActivePane {
			return p
		}
	}
	// Fallback: if ActivePane is stale, use first leaf.
	t.ActivePane = leaves[0].ID
	leaves[0].Active = true
	return leaves[0]
}

// Direction identifies one of the four spatial-navigation directions used by
// NavigateDirection.
type Direction int

const (
	DirLeft Direction = iota
	DirRight
	DirUp
	DirDown
)

// NavigateDirection moves focus to the closest pane in the given direction,
// if any exists. Returns true when focus changed. Semantics mirror tmux's
// `select-pane -L/R/U/D` and vim's window-motion commands: candidates must
// lie strictly in the half-plane on the target side and must overlap the
// active pane's perpendicular range. Tie-breakers are applied in order:
//
//  1. smallest gap along the direction axis,
//  2. largest perpendicular overlap,
//  3. smallest perpendicular center-to-center distance (tmux/vim parity —
//     when two equally-close candidates have the same overlap, the one whose
//     center is closer to the active pane's center on the perpendicular axis
//     wins, matching the user's muscle memory).
//
// Does nothing in focus mode or when the tab is empty.
func (t *TabModel) NavigateDirection(dir Direction) bool {
	if t.Root == nil || t.focusMode {
		return false
	}
	active := t.ActivePaneModel()
	if active == nil {
		return false
	}

	var rects []PaneRect
	t.Root.CollectRects(0, 0, t.Width, t.Height, &rects)
	if len(rects) < 2 {
		return false
	}

	var activeRect PaneRect
	found := false
	for _, r := range rects {
		if r.Pane.ID == active.ID {
			activeRect = r
			found = true
			break
		}
	}
	if !found {
		return false
	}

	bestIdx := -1
	bestGap := 1 << 30
	bestOverlap := -1
	bestPerp := 1 << 30

	for i, r := range rects {
		if r.Pane.ID == active.ID {
			continue
		}
		gap, overlap, perp, ok := directionScore(activeRect, r, dir)
		if !ok {
			continue
		}
		switch {
		case gap < bestGap:
		case gap == bestGap && overlap > bestOverlap:
		case gap == bestGap && overlap == bestOverlap && perp < bestPerp:
		default:
			continue
		}
		bestGap = gap
		bestOverlap = overlap
		bestPerp = perp
		bestIdx = i
	}

	if bestIdx < 0 {
		return false
	}
	activeRect.Pane.Active = false
	rects[bestIdx].Pane.Active = true
	t.ActivePane = rects[bestIdx].Pane.ID
	return true
}

// directionScore reports whether `cand` is a valid neighbor of `active` in
// the given direction, and if so, returns (gap, overlap, perpDist, true).
// Gap is the distance along the direction axis between the nearest edges;
// overlap is the size of the intersection on the perpendicular axis;
// perpDist is the absolute distance between the two rectangles' centers on
// the perpendicular axis (used as the third tie-breaker).
//
// >, not >=: adjacent panes share the border column, so cand.OX+cand.W ==
// active.OX is "immediately to the left" and must be accepted as DirLeft.
//
// Candidates with zero perpendicular overlap are rejected — a pane that is
// strictly above-and-to-the-right should not be reachable via "up".
func directionScore(active, cand PaneRect, dir Direction) (gap, overlap, perpDist int, ok bool) {
	switch dir {
	case DirLeft:
		if cand.OX+cand.W > active.OX {
			return 0, 0, 0, false
		}
		ov := rangeOverlap(active.OY, active.OY+active.H, cand.OY, cand.OY+cand.H)
		if ov <= 0 {
			return 0, 0, 0, false
		}
		return active.OX - (cand.OX + cand.W), ov, absInt(centerAxis(active.OY, active.H) - centerAxis(cand.OY, cand.H)), true

	case DirRight:
		if cand.OX < active.OX+active.W {
			return 0, 0, 0, false
		}
		ov := rangeOverlap(active.OY, active.OY+active.H, cand.OY, cand.OY+cand.H)
		if ov <= 0 {
			return 0, 0, 0, false
		}
		return cand.OX - (active.OX + active.W), ov, absInt(centerAxis(active.OY, active.H) - centerAxis(cand.OY, cand.H)), true

	case DirUp:
		if cand.OY+cand.H > active.OY {
			return 0, 0, 0, false
		}
		ov := rangeOverlap(active.OX, active.OX+active.W, cand.OX, cand.OX+cand.W)
		if ov <= 0 {
			return 0, 0, 0, false
		}
		return active.OY - (cand.OY + cand.H), ov, absInt(centerAxis(active.OX, active.W) - centerAxis(cand.OX, cand.W)), true

	case DirDown:
		if cand.OY < active.OY+active.H {
			return 0, 0, 0, false
		}
		ov := rangeOverlap(active.OX, active.OX+active.W, cand.OX, cand.OX+cand.W)
		if ov <= 0 {
			return 0, 0, 0, false
		}
		return cand.OY - (active.OY + active.H), ov, absInt(centerAxis(active.OX, active.W) - centerAxis(cand.OX, cand.W)), true
	}
	return 0, 0, 0, false
}

// centerAxis returns the integer center of the interval [origin, origin+size).
func centerAxis(origin, size int) int {
	return origin + size/2
}

// absInt is the obvious helper — math.Abs is float64 and pulls in math.
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// rangeOverlap returns the length of the intersection of two closed intervals
// [a1, a2) and [b1, b2) on the same axis. Zero when the intervals don't touch.
func rangeOverlap(a1, a2, b1, b2 int) int {
	lo := a1
	if b1 > lo {
		lo = b1
	}
	hi := a2
	if b2 < hi {
		hi = b2
	}
	if hi > lo {
		return hi - lo
	}
	return 0
}

// NextPane advances focus to the next pane (in-order traversal order).
func (t *TabModel) NextPane() {
	leaves := t.Root.Leaves()
	if len(leaves) == 0 {
		return
	}
	idx := t.activeIndex(leaves)
	leaves[idx].Active = false
	next := (idx + 1) % len(leaves)
	leaves[next].Active = true
	t.ActivePane = leaves[next].ID
}

// PrevPane moves focus to the previous pane.
func (t *TabModel) PrevPane() {
	leaves := t.Root.Leaves()
	if len(leaves) == 0 {
		return
	}
	idx := t.activeIndex(leaves)
	leaves[idx].Active = false
	prev := (idx - 1 + len(leaves)) % len(leaves)
	leaves[prev].Active = true
	t.ActivePane = leaves[prev].ID
}

// activeIndex finds the index of the active pane in leaves. Defaults to 0.
func (t *TabModel) activeIndex(leaves []*PaneModel) int {
	for i, p := range leaves {
		if p.ID == t.ActivePane {
			return i
		}
	}
	return 0
}

// Resize recomputes dimensions for the entire layout tree.
func (t *TabModel) Resize(w, h int) {
	t.Width = w
	t.Height = h
	if t.focusMode {
		if pane := t.ActivePaneModel(); pane != nil {
			pane.Width = w
			pane.Height = h
			cols := w - 2
			rows := h - 2
			if cols < 1 {
				cols = 1
			}
			if rows < 1 {
				rows = 1
			}
			pane.ResizeVT(cols, rows)
		}
		return
	}
	if t.Root != nil {
		resizeNode(t.Root, w, h)
	}
}

// View renders the entire pane layout.
func (t *TabModel) View() string {
	if t.Root == nil {
		return ""
	}
	if t.focusMode {
		if pane := t.ActivePaneModel(); pane != nil {
			return pane.View()
		}
	}
	return renderNode(t.Root)
}

// ToggleFocus toggles pane focus mode on/off.
// No-op on single-pane tabs (already fills the tab).
func (t *TabModel) ToggleFocus() {
	if t.Root != nil && t.Root.IsLeaf() {
		return
	}
	t.focusMode = !t.focusMode
}

// ExitFocus exits focus mode if active.
func (t *TabModel) ExitFocus() {
	t.focusMode = false
}

// FocusMode returns whether focus mode is active.
func (t *TabModel) FocusMode() bool {
	return t.focusMode
}

// SplitAtPane splits the pane with the given ID, inserting a placeholder
// for the new pane. Returns the placeholder node (caller fills Pane later).
func (t *TabModel) SplitAtPane(paneID string, dir SplitDir) *LayoutNode {
	if t.Root == nil {
		return nil
	}
	return t.Root.SplitLeaf(paneID, dir)
}

// RemovePane removes the pane with the given ID, promoting its sibling.
// If the removed pane was active, focus moves to the first leaf.
func (t *TabModel) RemovePane(paneID string) {
	if t.Root == nil {
		return
	}
	// If the root is a single leaf with this ID, clear the tree.
	if t.Root.IsLeaf() && t.Root.Pane.ID == paneID {
		t.Root = nil
		t.ActivePane = ""
		return
	}
	if !t.Root.RemoveLeaf(paneID) {
		return
	}
	// If we removed the active pane, pick the first leaf.
	if t.ActivePane == paneID {
		leaves := t.Root.Leaves()
		if len(leaves) > 0 {
			t.ActivePane = leaves[0].ID
			leaves[0].Active = true
		}
	}
}
