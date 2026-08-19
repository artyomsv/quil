package tui

import (
	"fmt"
	"testing"
)

// Scrollback depth is per-pane and every pane carries its own VT emulator,
// visible or not — so the figure multiplies by pane count. Measured in
// production 2026-08-18: 37 panes at the hardcoded 10 000 lines put the TUI at
// 1.13 GB resident.
//
// The default is deliberately UNCHANGED. Nobody's scrollback shrinks on
// upgrade; this exists so a user running dozens of panes on a memory-tight
// machine can trade depth for footprint, which was previously impossible at
// any price.
func TestScrollbackLines_DefaultMatchesHistoricalHardcodedDepth(t *testing.T) {
	if got := scrollbackLines(); got != defaultScrollbackLines {
		t.Errorf("scrollbackLines() = %d, want %d — the shipped default must not "+
			"change, or every existing install silently loses history on upgrade",
			got, defaultScrollbackLines)
	}
	if defaultScrollbackLines != 10000 {
		t.Errorf("defaultScrollbackLines = %d, want 10000 — this was the hardcoded "+
			"value before it became configurable", defaultScrollbackLines)
	}
}

func TestSetScrollbackLines_AppliesToNewPanes(t *testing.T) {
	orig := scrollbackLines()
	t.Cleanup(func() { SetScrollbackLines(orig) })

	SetScrollbackLines(500)
	if got := scrollbackLines(); got != 500 {
		t.Fatalf("scrollbackLines() = %d, want 500", got)
	}

	// The setting is only real if it reaches the emulator a pane is built with.
	p := NewPaneModel("pane-scrollback", 1024)
	t.Cleanup(p.Dispose)
	for i := 0; i < 700; i++ {
		p.AppendOutput([]byte("line of scrollback content\r\n"))
	}
	// A BAND, not just an upper bound. `sb > 500` alone is satisfied by zero,
	// which is precisely the "pane with no history at all" outcome the clamp
	// below exists to prevent — so the obvious assertion passes hardest on the
	// worst failure.
	sb := p.vt.ScrollbackLen()
	if sb > 500 {
		t.Errorf("pane retained %d scrollback lines with a 500 cap — the "+
			"configured depth never reached the emulator", sb)
	}
	if sb == 0 {
		t.Error("pane retained NO scrollback after 700 lines — the configured depth " +
			"reached the emulator as zero, which is worse than ignoring it")
	}
}

// Zero means "unset" in TOML, and a caller passing junk must not produce a pane
// with no history at all (or a negative allocation).
func TestSetScrollbackLines_RejectsUnsetAndNonsense(t *testing.T) {
	orig := scrollbackLines()
	t.Cleanup(func() { SetScrollbackLines(orig) })

	for _, n := range []int{0, -1, -10000} {
		SetScrollbackLines(n)
		if got := scrollbackLines(); got != defaultScrollbackLines {
			t.Errorf("SetScrollbackLines(%d) = %d, want the default %d — an unset "+
				"or nonsensical config value must fall back, not disable scrollback",
				n, got, defaultScrollbackLines)
		}
	}
}

// Adaptive default (option (a)): depth can only be chosen when a pane is
// CREATED, because x/vt's SetScrollbackSize reslices its backing array rather
// than reallocating (vt/scrollback.go: `s.lines = s.lines[len(s.lines)-max:]`)
// — the dropped prefix stays reachable, so trimming a live pane frees nothing.
// Creation is the one point the allocation is genuinely sized.

func TestAdaptiveScrollbackLines_ExplicitSettingAlwaysWins(t *testing.T) {
	// The user asked for a depth. Pane count must never override it.
	for _, panes := range []int{1, 41, 500} {
		if got := adaptiveScrollbackLines(7777, panes); got != 7777 {
			t.Errorf("panes=%d: got %d, want 7777 — an explicit setting is not advisory", panes, got)
		}
	}
}

func TestAdaptiveScrollbackLines_ExplicitStillClampedToMax(t *testing.T) {
	if got := adaptiveScrollbackLines(maxScrollbackLines+1, 1); got != maxScrollbackLines {
		t.Errorf("got %d, want %d — the typo guard must survive the adaptive path", got, maxScrollbackLines)
	}
}

func TestAdaptiveScrollbackLines_SmallWorkspacesAreUnchanged(t *testing.T) {
	// The promise: nobody with a modest workspace sees any change at all.
	for _, panes := range []int{0, 1, 2, 5, 10} {
		if got := adaptiveScrollbackLines(0, panes); got != defaultScrollbackLines {
			t.Errorf("panes=%d: got %d, want the unchanged default %d", panes, got, defaultScrollbackLines)
		}
	}
}

func TestAdaptiveScrollbackLines_ShrinksAsPanesGrow(t *testing.T) {
	prev := adaptiveScrollbackLines(0, 10)
	for _, panes := range []int{20, 41, 80} {
		got := adaptiveScrollbackLines(0, panes)
		if got > prev {
			t.Errorf("panes=%d: depth %d rose above the %d at fewer panes — the budget must not grow", panes, got, prev)
		}
		prev = got
	}
}

func TestAdaptiveScrollbackLines_NeverBelowTheFloor(t *testing.T) {
	// A floor exists because a pane with a few hundred lines of history is not
	// worth having; bounding memory must not make panes useless.
	if got := adaptiveScrollbackLines(0, 100000); got != minAdaptiveScrollbackLines {
		t.Errorf("got %d, want the floor %d", got, minAdaptiveScrollbackLines)
	}
}

func TestAdaptiveScrollbackLines_MeasuredWorkspaceLandsInBand(t *testing.T) {
	// 41 panes is the workspace this work was measured against. Asserting a
	// BAND rather than a constant: the policy may be retuned, but a value at
	// either clamp would mean the formula stopped doing anything.
	got := adaptiveScrollbackLines(0, 41)
	if got <= minAdaptiveScrollbackLines || got >= defaultScrollbackLines {
		t.Errorf("41 panes gave %d, expected strictly between the floor %d and the default %d",
			got, minAdaptiveScrollbackLines, defaultScrollbackLines)
	}
}

// SetPaneCount only ever RAISES. Depth cannot be reclaimed from panes already
// built, so a count that oscillates would hand out inconsistent depths for no
// benefit — a closing pane must not deepen the next pane's allocation.
func TestSetPaneCount_OnlyRaises(t *testing.T) {
	t.Cleanup(func() { knownPaneCount = 0 })
	knownPaneCount = 0

	SetPaneCount(41)
	if knownPaneCount != 41 {
		t.Fatalf("knownPaneCount = %d, want 41", knownPaneCount)
	}
	SetPaneCount(3)
	if knownPaneCount != 41 {
		t.Errorf("knownPaneCount fell to %d — a closing pane must not deepen the next pane's allocation", knownPaneCount)
	}
	SetPaneCount(60)
	if knownPaneCount != 60 {
		t.Errorf("knownPaneCount = %d, want 60 — a growing workspace must still tighten the budget", knownPaneCount)
	}
}

// scrollbackLines() is what newVTEmulator calls, so it must route through the
// policy rather than returning the raw setting.
func TestScrollbackLines_RoutesThroughTheAdaptivePolicy(t *testing.T) {
	t.Cleanup(func() { explicitScrollback = 0; knownPaneCount = 0 })

	explicitScrollback, knownPaneCount = 0, 41
	adaptive := scrollbackLines()
	if adaptive != adaptiveScrollbackLines(0, 41) {
		t.Errorf("scrollbackLines() = %d, want the policy's %d", adaptive, adaptiveScrollbackLines(0, 41))
	}
	if adaptive >= defaultScrollbackLines {
		t.Errorf("scrollbackLines() = %d at 41 panes — the adaptive path is not wired in", adaptive)
	}

	explicitScrollback = 4321
	if got := scrollbackLines(); got != 4321 {
		t.Errorf("scrollbackLines() = %d with an explicit setting, want 4321", got)
	}
}

// The call site, not just the policy. applyWorkspaceState must publish the
// count BEFORE it creates panes, or a restored workspace sizes its first pane
// against a count of zero — which resolves to the full default and cannot be
// revised once the emulator exists.
func TestApplyWorkspaceState_PublishesPaneCountBeforeBuildingPanes(t *testing.T) {
	t.Cleanup(func() { knownPaneCount = 0; explicitScrollback = 0 })
	knownPaneCount, explicitScrollback = 0, 0

	m := coalesceModel(t)
	state := WorkspaceStateMsg{
		Tabs: []TabInfo{{ID: "t1", Name: "restored"}},
	}
	for i := 0; i < 41; i++ {
		id := fmt.Sprintf("restored-pane-%d", i)
		state.Panes = append(state.Panes, PaneInfo{ID: id, TabID: "t1", Type: "terminal"})
		state.Tabs[0].Panes = append(state.Tabs[0].Panes, id)
	}

	m.applyWorkspaceState(state, "")

	if knownPaneCount < 41 {
		t.Fatalf("knownPaneCount = %d after applying a 41-pane workspace, want >= 41", knownPaneCount)
	}
	depth := scrollbackLines()
	if depth >= defaultScrollbackLines {
		t.Errorf("scrollbackLines() = %d after a 41-pane restore — the count was not "+
			"published before panes were built, so every pane got the full default", depth)
	}
}
