package daemon

import (
	"encoding/json"
	"log"

	"github.com/artyomsv/quil/internal/layouttree"
)

// Stored layout trees follow pane membership (spec 5b §3.2). Every helper here
// runs inside a SessionManager method under sm.mu (write), in the same hold as
// the membership change, so a snapshot or broadcast never sees a tab whose
// tree and pane list disagree. None of them takes PluginMu.

// layoutWrite is what SetTabLayout did with a client's tree.
type layoutWrite int

const (
	layoutStored layoutWrite = iota
	layoutNoTab
	layoutStale
)

// treePanesLocked lists tab's live panes that belong in its tree — every pane
// but an overlay — in tab order.
func (sm *SessionManager) treePanesLocked(tab *Tab) []string {
	out := make([]string, 0, len(tab.Panes))
	for _, id := range tab.Panes {
		if p, ok := sm.panes[id]; ok && !p.treeless {
			out = append(out, id)
		}
	}
	return out
}

// treePanesExceptLocked is treePanesLocked without one id.
func (sm *SessionManager) treePanesExceptLocked(tab *Tab, skip string) []string {
	all := sm.treePanesLocked(tab)
	out := all[:0]
	for _, id := range all {
		if id != skip {
			out = append(out, id)
		}
	}
	return out
}

// wellFormed copies a tree into the shape every tree operation assumes: a
// leaf carries its pane id only, an inner node has both children and a split
// of Horizontal or Vertical. An inner node missing a child is replaced by the
// other, one missing both — or a leaf with no id — disappears. A client wrote
// these bytes, so nothing about their shape is taken on trust; Normalize's
// first-leaf walk, for one, follows Left without a nil check.
func wellFormed(n *layouttree.Node) *layouttree.Node {
	if n == nil {
		return nil
	}
	if n.PaneID != "" {
		return &layouttree.Node{PaneID: n.PaneID}
	}
	l, r := wellFormed(n.Left), wellFormed(n.Right)
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	dir := layouttree.Horizontal
	if n.Split != nil && *n.Split == layouttree.Vertical {
		dir = layouttree.Vertical
	}
	return &layouttree.Node{Split: &dir, Ratio: n.Ratio, Left: l, Right: r}
}

// storedTreeLocked parses tab's stored tree; false for no tree or bytes that
// do not parse (which every reader treats as no tree).
func storedTreeLocked(tab *Tab) (*layouttree.Node, bool) {
	tree, err := layouttree.Parse(tab.Layout)
	if err != nil {
		return nil, false
	}
	tree = wellFormed(tree)
	return tree, tree != nil
}

// storeTreeLocked stores tree (nil = no tree) and bumps LayoutRev.
func storeTreeLocked(tab *Tab, tree *layouttree.Node) {
	raw, err := layouttree.Marshal(tree)
	if err != nil {
		log.Printf("layout: marshal tab %s: %v", tab.ID, err)
		return
	}
	tab.Layout = raw
	tab.LayoutRev++
}

// pruneTreeLocked removes paneID from tab's stored tree; LayoutRev moves only
// when the tree held it (an overlay's destroy must not make clients re-adopt).
func pruneTreeLocked(tab *Tab, paneID string) {
	tree, ok := storedTreeLocked(tab)
	if !ok {
		return
	}
	if out, changed := layouttree.Prune(tree, paneID); changed {
		storeTreeLocked(tab, out)
	}
}

// substituteTreeLocked gives newID oldID's leaf.
func substituteTreeLocked(tab *Tab, oldID, newID string) {
	tree, ok := storedTreeLocked(tab)
	if !ok {
		return
	}
	if out, changed := layouttree.Substitute(tree, oldID, newID); changed {
		storeTreeLocked(tab, out)
	}
}

// placeMovedLocked places a pane that just moved into tab by the spiral rule.
// A tab with no stored tree keeps none: clients place and store it as before.
func (sm *SessionManager) placeMovedLocked(tab *Tab, paneID string) {
	tree, ok := storedTreeLocked(tab)
	if !ok {
		return
	}
	base := layouttree.Normalize(tree, sm.treePanesExceptLocked(tab, paneID), tab.TemplateLayout, tab.TemplateMain)
	storeTreeLocked(tab, layouttree.PlaceMoved(base, paneID))
}

// insertPaneLocked places paneID (already a member of tab) by splitting
// target in tab's normalized tree; target "" is that tree's first leaf. False
// — and nothing stored — when target is not in the tree. Used by
// split_pane_req (spec §3.3).
func (sm *SessionManager) insertPaneLocked(tab *Tab, paneID, target string, dir layouttree.SplitDir) bool {
	stored, _ := storedTreeLocked(tab)
	base := layouttree.Normalize(stored, sm.treePanesExceptLocked(tab, paneID), tab.TemplateLayout, tab.TemplateMain)
	if base == nil {
		if target != "" {
			return false
		}
		storeTreeLocked(tab, &layouttree.Node{PaneID: paneID})
		return true
	}
	if target == "" {
		target = layouttree.PaneIDs(base)[0]
	}
	out, ok := layouttree.SplitLeaf(base, target, dir, paneID)
	if !ok {
		return false
	}
	storeTreeLocked(tab, out)
	return true
}

// revalidateRestoredLayout runs a restored tab's stored tree through the same
// validation as SetTabLayout, once its panes are in the session. A
// workspace.json written by an older daemon stored whatever a client sent, and
// restore itself skips a pane id it cannot accept, so the file can name a pane
// that is not live; without this the first broadcast would carry it (AC-7).
// A valid tree keeps its bytes. LayoutRev is left as restored: every client
// adopts the first broadcast after a reattach whatever its revision.
func (sm *SessionManager) revalidateRestoredLayout(tabID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	tab, ok := sm.tabs[tabID]
	if !ok || len(tab.Layout) == 0 {
		// No tree: a template tab still waiting for its first one must stay
		// without, and an ordinary tab gets its tree from the next write.
		return
	}
	tab.Layout = sm.validLayoutLocked(tab, tab.Layout)
}

// validLayoutLocked is what SetTabLayout stores for a client's tree: the
// client's own bytes when the tree is already valid — so the TUI's echo
// detection, which compares against what it sent, still recognises its write
// — otherwise the corrected tree, or empty when nothing is left.
//
// Valid means: it is well formed (wellFormed), names only live non-overlay
// panes of this tab, each once, every ratio inside (0, 1), and lacks none of
// them. Malformed bytes are "no tree" and get the arrival-rule (or template)
// tree.
//
// "Unchanged" compares against Clone of the parsed input, not the parse
// itself: Clone, like every step of the correction, gives an inner node with
// no split the side-by-side default every reader already applies, so a write
// that omits it is not mistaken for one that needed fixing.
func (sm *SessionManager) validLayoutLocked(tab *Tab, in json.RawMessage) json.RawMessage {
	parsed, err := layouttree.Parse(in)
	if err != nil {
		parsed = nil
	}
	out := layouttree.Normalize(layouttree.Sanitize(wellFormed(parsed)), sm.treePanesLocked(tab), tab.TemplateLayout, tab.TemplateMain)
	if out == nil {
		return nil
	}
	if err == nil && layouttree.Equal(out, layouttree.Clone(parsed)) {
		return in
	}
	raw, mErr := layouttree.Marshal(out)
	if mErr != nil {
		log.Printf("layout: marshal tab %s: %v", tab.ID, mErr)
		return nil
	}
	return raw
}
