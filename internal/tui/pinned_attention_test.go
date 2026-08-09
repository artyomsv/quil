package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// pinnedTabModel builds a one-tab model whose single pane carries the given
// marks. The tab it returns is index 0 and is the ACTIVE tab, which matters:
// tabUnseen self-excludes the active tab while tabPinnedAttention deliberately
// does not, so the two are only comparable on a tab both can speak about.
func pinnedTabModel(t *testing.T, pinned, unseen, blocked bool) Model {
	t.Helper()
	pane := newTestPane("pane-1")
	pane.pinnedAttention = pinned
	pane.unseen = unseen
	if blocked {
		pane.blockedSince = time.Now()
	}
	m := Model{}
	m.setTabs([]*TabModel{{ID: "tab-1", Name: "build", Root: &LayoutNode{Pane: pane}, ActivePane: "other"}})
	return m
}

// TestTabLabel_MarksAPinnedTab: the tab bar is where the user first notices a
// tab wants something, and a pin left no trace on the LABEL at all — only on
// the colour, which it shared with the unseen mark.
func TestTabLabel_MarksAPinnedTab(t *testing.T) {
	t.Parallel()
	m := pinnedTabModel(t, true, false, false)
	if got := m.tabLabel(0); !strings.Contains(got, glyphPinned) {
		t.Errorf("tabLabel = %q, want the %s pin marker", got, glyphPinned)
	}
	plain := pinnedTabModel(t, false, false, false)
	if got := plain.tabLabel(0); strings.Contains(got, glyphPinned) {
		t.Errorf("tabLabel = %q on an unpinned tab, want no pin marker", got)
	}
}

// TestTabLabel_PinnedAndBlockedShowsBoth is the reason the glyph is NOT gated
// on tabStyle's precedence. Blocked outranks pinned for the colour — a parked
// agent is a question costing you time, where a pin is a note to self — so on a
// tab that is both, the glyph is the only channel left to say the pin is there.
// Gating it would hide the mark exactly on the tabs the user is most likely to
// have marked.
func TestTabLabel_PinnedAndBlockedShowsBoth(t *testing.T) {
	t.Parallel()
	m := pinnedTabModel(t, true, false, true)
	if got := m.tabLabel(0); !strings.Contains(got, glyphPinned) {
		t.Errorf("tabLabel = %q on a pinned AND blocked tab, want the pin marker kept", got)
	}
	if got := m.tabStyle(0); got.GetBackground() != blockedTabStyle.GetBackground() {
		t.Error("a pinned and blocked tab must keep the blocked colour — blocked outranks pinned")
	}
}

// TestTabStyle_PinnedIsNotTheUnseenGreen is the headline of the tab-bar half.
// Both used to return unseenTabStyle, so a mark the user set by hand looked
// exactly like one the agent caused — and only the agent's clears itself, so
// the user was left waiting on a green that never went away.
func TestTabStyle_PinnedIsNotTheUnseenGreen(t *testing.T) {
	t.Parallel()
	pinned := pinnedTabModel(t, true, false, false).tabStyle(0)
	unseen := pinnedTabModel(t, false, true, false).tabStyle(0)

	if pinned.GetBackground() == unseen.GetBackground() {
		t.Errorf("pinned and unseen tabs share background %v — they must be distinguishable",
			pinned.GetBackground())
	}
	if pinned.GetBackground() != pinnedTabStyle.GetBackground() {
		t.Errorf("a pinned tab renders %v, want pinnedTabStyle's %v",
			pinned.GetBackground(), pinnedTabStyle.GetBackground())
	}
	// Pinned outranks unseen on a tab that is both: unseen clears itself on
	// focus, the pin needs an explicit Unmark, so showing the one that goes
	// away by itself would lose the one that does not.
	both := pinnedTabModel(t, true, true, false).tabStyle(0)
	if both.GetBackground() != pinnedTabStyle.GetBackground() {
		t.Error("a tab that is both pinned and unseen must show the pin")
	}
}

// TestTabStyle_PinnedActiveIsDistinguishable. Active and inactive tabs differ
// by BACKGROUND alone, and pinnedTabStyle replaces the background wholesale —
// the same trap blockedActiveTabStyle was added for. The variant must not
// change the RENDERED WIDTH, because renderTabBar measures style.Render(name)
// and hitTestTab repeats the measurement: extra padding would shift every tab
// after it and send clicks to the wrong one.
func TestTabStyle_PinnedActiveIsDistinguishable(t *testing.T) {
	t.Parallel()
	const name = "1:build"
	if pinnedTabStyle.Render(name) == pinnedActiveTabStyle.Render(name) {
		t.Error("the active pinned tab renders identically to the inactive one")
	}
	if w, aw := lipgloss.Width(pinnedTabStyle.Render(name)), lipgloss.Width(pinnedActiveTabStyle.Render(name)); w != aw {
		t.Errorf("pinned active variant measures %d cells against the base's %d — "+
			"renderTabBar and hitTestTab both measure this, so a difference desyncs clicks", aw, w)
	}
}

// TestPaneRow_PinSuffixKeepsThePinColour. When a live state outranks the pin,
// the ◆ survives as a suffix — and it used to be painted in the OUTRANKING
// state's colour, because the whole row went through one Render. An amber ◆
// reads as part of the blocked state rather than as the user's own mark, which
// is the distinction this change exists to make.
func TestPaneRow_PinSuffixKeepsThePinColour(t *testing.T) {
	t.Parallel()
	pinSGR := styleSGR(t, sidebarPinnedStyle)
	if pinSGR == "" {
		t.Skip("lipgloss renders without colour here — this assertion cannot discriminate")
	}
	// Only the two states that OUTRANK the pin in paneRow's switch put it in a
	// suffix. Unseen does not — the pin wins there and becomes the row's
	// primary glyph, which the last block below covers instead.
	for _, tt := range []struct {
		name  string
		setup func(p *PaneModel)
	}{
		{"blocked", func(p *PaneModel) { p.blockedSince = time.Now() }},
		{"working", func(p *PaneModel) { p.working = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pane := &PaneModel{ID: "p1", Name: "agent"}
			pane.pinnedAttention = true
			tt.setup(pane)
			row := paneRow(pane, false, defaultSidebarWidth)
			if !strings.Contains(row, pinSGR+" "+glyphPinned) {
				t.Errorf("paneRow = %q, want the ◆ suffix painted in the pin colour, not the %s state's", row, tt.name)
			}
		})
	}

	// Unseen is ranked BELOW the pin, so a pane that is both shows ◆ as its
	// glyph and the whole row takes the pin colour. Asserted so the ranking is
	// pinned rather than inferred: promoting unseen above the pin would hide a
	// mark that needs an explicit Unmark behind one that clears itself.
	t.Run("unseen ranks below the pin", func(t *testing.T) {
		t.Parallel()
		pane := &PaneModel{ID: "p1", Name: "agent"}
		pane.pinnedAttention = true
		pane.unseen = true
		row := paneRow(pane, false, defaultSidebarWidth)
		if !strings.Contains(row, pinSGR) || !strings.Contains(row, glyphPinned) {
			t.Errorf("paneRow = %q, want the pin glyph in the pin colour", row)
		}
		if strings.Contains(row, glyphDone) {
			t.Errorf("paneRow = %q shows the unseen ✓ over the pin", row)
		}
	})
}

// TestPaneRow_PinSurvivesALongBlockedReason. The pin's width is RESERVED before
// the label floor applies, so a long tool name truncates into the label's
// budget instead of eating the mark. Appending it to the suffix left it last in
// one string cut from the end, so the pin vanished exactly when the pane was
// busiest — which is when the user most needs to find it again.
func TestPaneRow_PinSurvivesALongBlockedReason(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "pane-b16e3850", Name: "agent"}
	pane.pinnedAttention = true
	pane.blockedSince = time.Now()
	pane.blockedReason = "AskUserQuestion"

	row := paneRow(pane, false, defaultSidebarWidth)
	if !strings.Contains(row, glyphPinned) {
		t.Errorf("paneRow = %q dropped the pin to fit a long blocked reason", row)
	}
	if n := lipgloss.Width(row); n != defaultSidebarWidth {
		t.Errorf("row measures %d cells, want exactly %d", n, defaultSidebarWidth)
	}
}

// TestParseWorkspaceState_ReadsThePinWireKey pins the CLIENT half of the wire
// contract, and it is the one link nothing else covers: the daemon's own
// round-trip test proves the writer and reader agree with EACH OTHER, because
// both live in that package. A key the daemon writes and this parser looks for
// under a different name would pass every test on both sides and simply never
// show the mark.
//
// The literal "pinned_attention" is deliberately spelled out here rather than
// referenced through a shared constant — the constant would make the two sides
// agree by construction and stop testing anything. Its twin is
// TestSnapshot_PinnedAttentionUsesTheWireKey in internal/daemon.
func TestParseWorkspaceState_ReadsThePinWireKey(t *testing.T) {
	t.Parallel()
	got := parseWorkspaceState(map[string]any{
		"panes": []any{
			map[string]any{"id": "p1", "tab_id": "t1", "pinned_attention": true},
			map[string]any{"id": "p2", "tab_id": "t1"},
		},
	})
	if len(got.Panes) != 2 {
		t.Fatalf("parsed %d panes, want 2", len(got.Panes))
	}
	if !got.Panes[0].PinnedAttention {
		t.Error(`the "pinned_attention" key did not reach PaneInfo — the daemon writes it under exactly that name`)
	}
	// Absent means false: the daemon omits the key when the pane is not pinned
	// (the omit-if-false idiom every flag in that block uses), so a parser that
	// defaulted to true would mark every pane in the workspace.
	if got.Panes[1].PinnedAttention {
		t.Error("a pane with no pinned_attention key parsed as pinned")
	}
}

// TestSyncPaneMeta_AdoptsTheDaemonsPin. The daemon is the sole author of this
// mark now, which is what makes it survive a restart and read the same on every
// client. A copy that only ever SET it — the shape a naive `if info.X { pane.X
// = true }` takes — would make an Unmark performed in another client, or a
// Clear attention that reached the daemon, invisible here forever.
func TestSyncPaneMeta_AdoptsTheDaemonsPin(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", PinnedAttention: true}, false, 0)
	if !pane.pinnedAttention {
		t.Fatal("a daemon-reported pin was not adopted")
	}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", PinnedAttention: false}, false, 0)
	if pane.pinnedAttention {
		t.Error("a daemon-reported CLEAR was not adopted — the copy must be unconditional, " +
			"or an unmark from another client can never reach this one")
	}
}
