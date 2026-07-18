package tui

import (
	"strings"
	"testing"
)

// Pinned attention must survive focusing the pane — that is the whole point
// of the pin vs the auto-clearing unseen mark.
func TestPinnedAttention_SurvivesAckFocusedPane(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	p1 := m.tabs[0].Root.Left.Pane
	p1.pinnedAttention = true
	p1.unseen = true
	m.tabs[0].ActivePane = "p1"
	m.ackFocusedPane()
	if p1.unseen {
		t.Error("unseen should be cleared on focus")
	}
	if !p1.pinnedAttention {
		t.Error("pinnedAttention must survive focus")
	}
}

// The green tab label derivation: unlike tabUnseen, a pinned pane colors the
// ACTIVE tab too — unless the pinned pane is the focused pane itself.
func TestTabPinnedAttention(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // tab 0 active, panes p1 (focused) | p2
	p2 := m.tabs[0].Root.Right.Pane

	if m.tabPinnedAttention(0) {
		t.Error("no pins: want false")
	}
	p2.pinnedAttention = true
	if !m.tabPinnedAttention(0) {
		t.Error("pinned unfocused pane on active tab: want true")
	}
	// Focus the pinned pane: the user is looking at it — no label cue.
	m.tabs[0].ActivePane = "p2"
	if m.tabPinnedAttention(0) {
		t.Error("pinned focused pane: want false")
	}
	// Out-of-range index must not panic.
	if m.tabPinnedAttention(5) {
		t.Error("out of range: want false")
	}
}

// Both new flags are View() inputs — they MUST be part of the render key or
// toggling them renders a stale cached frame.
func TestRenderKey_IncludesPinnedAndCtxHighlight(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("k", 1024)
	base := p.renderKey()
	p.pinnedAttention = true
	if p.renderKey() == base {
		t.Error("pinnedAttention must change the render key")
	}
	p.pinnedAttention = false
	p.ctxTargetHighlight = true
	if p.renderKey() == base {
		t.Error("ctxTargetHighlight must change the render key")
	}
}

// Border colors: pinned renders the same green as unseen; the menu-target
// highlight renders the split-drag blue (39). Uses the split fixture's panes
// because they have a live VT emulator and real dimensions (View() needs
// both; a bare NewPaneModel does not get them until a layout Resize).
func TestView_PinnedGreenBorder_CtxHighlightBlue(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	p := m.tabs[0].Root.Left.Pane
	p.Active = false // active purple (57) outranks green — test the idle pane look
	p.pinnedAttention = true
	if !strings.Contains(p.View(), "38;5;28") {
		t.Error("pinned pane should render green (28) border")
	}
	p.ctxTargetHighlight = true
	if !strings.Contains(p.View(), "38;5;39") {
		t.Error("ctx-target pane should render blue (39) border")
	}
}
