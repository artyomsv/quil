package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
)

// newTabBarScrollModel builds a model the same way newModelForTest does, but
// gives every tab a real PaneModel with real SCROLLBACK — the "no pane
// scrolled" claim in these tests needs a pane whose scroll offset could
// actually move to be worth anything. A pane with no history has
// maxScroll()==0 (pane.go), so ScrollUp clamps right back to 0 and
// ScrollDown floors at 0 regardless of whether the wheel reached it — a
// leaked event would be silently indistinguishable from a correctly-swallowed
// one. Feeding each pane enough lines to overflow its 24-row VT gives
// ScrollUp real room to move, which is what makes wantScrollableHistory (and
// every "the wheel must not reach the pane" assertion below) a real check
// rather than a vacuous one. Sized at 60x40 with no project sidebar, which
// the fixture below (8 ten-character tab names) reliably overflows.
func newTabBarScrollModel(names []string, activeIdx int) Model {
	m := newModelForTest(names, activeIdx)
	var scrollback strings.Builder
	for line := 0; line < 80; line++ {
		fmt.Fprintf(&scrollback, "line %d\r\n", line)
	}
	for i, tab := range m.curTabs() {
		pane := NewPaneModel(fmt.Sprintf("pane-%d", i), 4096)
		pane.Active = true
		pane.AppendOutput([]byte(scrollback.String()))
		tab.Root = NewLeaf(pane)
		tab.ActivePane = pane.ID
	}
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 60, 40
	return m
}

// wantScrollableHistory fails the test unless the active pane actually has
// scrollback to move through. Without this control, a pane's scrollBack
// starting at (and clamping back to) 0 on both ends makes a wheel event that
// LEAKS through to PaneModel.ScrollUp/ScrollDown indistinguishable from one
// that correctly never reached the pane at all.
func wantScrollableHistory(t *testing.T, m Model) {
	t.Helper()
	tab := m.activeTabModel()
	if tab == nil {
		t.Fatal("no active tab")
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		t.Fatal("no active pane")
	}
	if pane.maxScroll() <= 0 {
		t.Fatal("fixture pane has no scrollback — a wheel event leaking through to it " +
			"would be invisible; feed it more output")
	}
}

// eightOverflowingTabNames is wide enough, at 60 columns, that only a few of
// the eight tabs fit at once — the fixture every test below shares.
func eightOverflowingTabNames() []string {
	names := make([]string, 8)
	for i := range names {
		names[i] = strings.Repeat(string(rune('A'+i)), 10)
	}
	return names
}

// wantOverflow fails the test if the fixture does not actually overflow the
// bar — every case here depends on scrolling being possible at all.
func wantOverflow(t *testing.T, m Model) {
	t.Helper()
	if len(m.tabSpans()) == len(m.curTabs()) {
		t.Fatal("fixture does not overflow — this test cannot discriminate, " +
			"widen the tabs or narrow the terminal")
	}
}

func wheelAtTabBar(t *testing.T, m Model, btn tea.MouseButton) Model {
	t.Helper()
	updated, cmd := m.Update(tea.MouseWheelMsg{X: m.projectSidebarWidth() + 1, Y: 0, Button: btn})
	if cmd != nil {
		t.Fatal("wheel over the tab bar must never produce a command " +
			"(no pane forward, no skip-render opt-out)")
	}
	return updated.(Model)
}

func activePaneScrollBack(t *testing.T, m Model) int {
	t.Helper()
	tab := m.activeTabModel()
	if tab == nil {
		t.Fatal("no active tab")
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		t.Fatal("no active pane")
	}
	return pane.scrollBack
}

// Wheel down over the tab bar advances the visible window by one tab, leaves
// the active tab untouched, and never reaches the pane beneath the bar.
func TestWheelDownOverTabBar_ScrollsWithoutSwitchingOrPaneScroll(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)
	wantScrollableHistory(t, m)

	beforeSpans := m.tabSpans()
	beforeFirst := beforeSpans[0].index
	beforeActiveID := m.curTabs()[m.activeTabIdx()].ID

	got := wheelAtTabBar(t, m, tea.MouseWheelDown)

	afterSpans := got.tabSpans()
	if len(afterSpans) == 0 {
		t.Fatal("no tabs visible after scrolling")
	}
	if afterSpans[0].index != beforeFirst+1 {
		t.Fatalf("first visible tab = %d after one wheel-down notch, want %d",
			afterSpans[0].index, beforeFirst+1)
	}
	if got.curTabs()[got.activeTabIdx()].ID != beforeActiveID {
		t.Fatalf("active tab changed from %q to %q — the wheel must never switch tabs",
			beforeActiveID, got.curTabs()[got.activeTabIdx()].ID)
	}

	// The wheel must never reach the pane. Wheel-UP is the discriminating
	// probe: scrollBack starts at 0, and ScrollDown floors there too (so a
	// down-notch leak would be invisible), but ScrollUp on a pane with real
	// history (wantScrollableHistory) would move scrollBack off 0 the moment
	// the event reached PaneModel.ScrollUp.
	upped := wheelAtTabBar(t, got, tea.MouseWheelUp)
	if s := activePaneScrollBack(t, upped); s != 0 {
		t.Fatalf("active pane scrollBack = %d after wheel-up over the tab bar, want 0 — "+
			"the wheel must not reach the pane", s)
	}
}

// Wheel right is wheel down; wheel left is wheel up — the four buttons a
// tea.MouseWheelMsg can carry are matched explicitly, not collapsed to
// up/down.
func TestWheelLeftRightOverTabBar_MatchUpDown(t *testing.T) {
	t.Parallel()

	down := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, down)
	down = wheelAtTabBar(t, down, tea.MouseWheelDown)

	right := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	right = wheelAtTabBar(t, right, tea.MouseWheelRight)

	if down.tabScrollFirst != right.tabScrollFirst {
		t.Fatalf("wheel-right first=%d, want the same as wheel-down first=%d",
			right.tabScrollFirst, down.tabScrollFirst)
	}

	// Now walk both back with up/left from the same scrolled position.
	up := wheelAtTabBar(t, down, tea.MouseWheelUp)
	left := wheelAtTabBar(t, down, tea.MouseWheelLeft)
	if up.tabScrollFirst != left.tabScrollFirst {
		t.Fatalf("wheel-left first=%d, want the same as wheel-up first=%d",
			left.tabScrollFirst, up.tabScrollFirst)
	}
}

// A regression fixture found by exhaustive search over (tab count, name
// length, bar width, active index): n=4 one-character tab names, barW=22,
// active tab 3 (the LAST tab). Auto mode's expansion there is left-only —
// there is nothing to the right of the last tab — but it still reserves
// space for a right marker it will never need on every step of that
// expansion, the same conservative approximation auto mode has always used
// (see tabBarLayout). That makes its window start (tab 2) land PAST
// maxFirst (1, computed with only the left marker reserved, since the tail
// is already fully shown). A wheel-DOWN notch that just clamps the result —
// clamp(2+1, 0, 1) = 1 — moves the visible window LEFT, backward from the
// direction the wheel was actually turned.
func TestWheelDown_NeverMovesTheWindowOppositeToItsDirection(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel([]string{"A", "B", "C", "D"}, 3)
	m.width = 22
	wantOverflow(t, m)

	before := m.tabSpans()
	if len(before) == 0 {
		t.Fatal("fixture precondition: nothing visible")
	}
	beforeFirst := before[0].index
	_, widths, barW, leftMarkerW, _ := m.tabBarWidths()
	maxFirst := maxFirstIndex(widths, barW, leftMarkerW)
	if beforeFirst <= maxFirst {
		t.Fatalf("fixture precondition: auto-mode first=%d must EXCEED maxFirst=%d, "+
			"or this fixture no longer reproduces the mismatch a plain clamp gets wrong",
			beforeFirst, maxFirst)
	}

	got := wheelAtTabBar(t, m, tea.MouseWheelDown)
	after := got.tabSpans()
	if len(after) == 0 {
		t.Fatal("no tabs visible after wheel-down")
	}
	if after[0].index < beforeFirst {
		t.Fatalf("first visible tab moved from %d to %d on a wheel-DOWN notch — "+
			"the window must never move opposite to the direction it was turned",
			beforeFirst, after[0].index)
	}
	// The refusal must be a genuine no-op: no state change, still auto mode.
	if got.tabScrollFirst != 0 || got.tabScrollAnchor != "" {
		t.Fatalf("a refused notch still wrote scroll state: first=%d anchor=%q",
			got.tabScrollFirst, got.tabScrollAnchor)
	}
	if got.tabBarManualMode() {
		t.Fatal("a refused notch entered manual mode instead of leaving auto mode untouched")
	}
}

// Wheel up at the leftmost position and wheel down at maxFirst are both
// no-ops: no change, and — the property a bad clamp would violate — no
// panic.
func TestWheelAtEdges_NoChangeNoPanic(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)

	// Auto mode starts the window at tab 0 for an active tab at index 0, so
	// wheel-up here is already at the left edge.
	beforeFirst := m.tabSpans()[0].index
	if beforeFirst != 0 {
		t.Fatalf("fixture precondition: auto-mode first = %d, want 0", beforeFirst)
	}
	up := wheelAtTabBar(t, m, tea.MouseWheelUp)
	if got := up.tabSpans()[0].index; got != 0 {
		t.Fatalf("wheel-up at the leftmost position moved to %d, want 0 (no-op)", got)
	}

	_, widths, barW, leftMarkerW, _ := m.tabBarWidths()
	maxFirst := maxFirstIndex(widths, barW, leftMarkerW)

	// Drive to maxFirst, then one more down must not move past it or panic.
	cur := m
	for i := 0; i < len(m.curTabs())+2; i++ {
		cur = wheelAtTabBar(t, cur, tea.MouseWheelDown)
	}
	if cur.tabScrollFirst != maxFirst {
		t.Fatalf("after overscrolling down, tabScrollFirst = %d, want clamped to maxFirst = %d",
			cur.tabScrollFirst, maxFirst)
	}
	past := wheelAtTabBar(t, cur, tea.MouseWheelDown)
	if past.tabScrollFirst != maxFirst {
		t.Fatalf("wheel-down at maxFirst moved to %d, want unchanged %d", past.tabScrollFirst, maxFirst)
	}
}

// Repeated wheel-down stops exactly at maxFirst: the last tab is visible and
// no wider tail than necessary is left off-screen.
func TestRepeatedWheelDown_StopsAtMaxFirst(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)
	_, widths, barW, leftMarkerW, _ := m.tabBarWidths()
	maxFirst := maxFirstIndex(widths, barW, leftMarkerW)
	if maxFirst == 0 {
		t.Fatal("fixture's maxFirst = 0 — this test cannot discriminate, widen the fixture")
	}

	cur := m
	for i := 0; i < len(m.curTabs())*2; i++ {
		cur = wheelAtTabBar(t, cur, tea.MouseWheelDown)
	}
	if cur.tabScrollFirst != maxFirst {
		t.Fatalf("tabScrollFirst = %d after over-scrolling, want maxFirst = %d", cur.tabScrollFirst, maxFirst)
	}
	spans := cur.tabSpans()
	last := spans[len(spans)-1]
	if last.index != len(cur.curTabs())-1 {
		t.Fatalf("last visible tab index = %d, want the final tab (%d)", last.index, len(cur.curTabs())-1)
	}
}

// The left marker appears once tabs are hidden to the left, the right marker
// once tabs remain to the right, both counts agree with tabBarLayout, and the
// bar stays one line within paneAreaWidth() throughout.
func TestTabBarMarkers_TwoSidedAndBounded(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)

	assertBounded := func(mm Model) {
		t.Helper()
		bar := mm.renderTabBar()
		if strings.Contains(bar, "\n") {
			t.Fatal("tab bar wrapped onto a second line")
		}
		if w := lipgloss.Width(bar); w != mm.paneAreaWidth() {
			t.Fatalf("tab bar width = %d, want paneAreaWidth() = %d", w, mm.paneAreaWidth())
		}
	}

	// At the start (auto mode, active tab 0) nothing is hidden on the left.
	_, hiddenLeft, hiddenRight := m.tabBarLayout()
	assertBounded(m)
	if hiddenLeft != 0 {
		t.Fatalf("hiddenLeft = %d at the leftmost position, want 0", hiddenLeft)
	}
	if hiddenRight == 0 {
		t.Fatal("fixture does not hide any tab on the right — cannot exercise the right marker")
	}
	row := stripANSI(m.renderTabBar())
	if !strings.Contains(row, fmt.Sprintf(" %d»", hiddenRight)) {
		t.Errorf("bar does not show the right marker for %d hidden tabs: %q", hiddenRight, row)
	}
	if strings.Contains(row, "«") {
		t.Errorf("bar shows a left marker with nothing hidden on the left: %q", row)
	}

	// Scroll until something is hidden on the left too.
	cur := m
	for i := 0; i < len(m.curTabs()); i++ {
		cur = wheelAtTabBar(t, cur, tea.MouseWheelDown)
		_, hl, _ := cur.tabBarLayout()
		if hl > 0 {
			break
		}
	}
	_, hiddenLeft, hiddenRight = cur.tabBarLayout()
	if hiddenLeft == 0 {
		t.Fatal("could not reach a scroll position that hides a tab on the left")
	}
	assertBounded(cur)
	row = stripANSI(cur.renderTabBar())
	if !strings.Contains(row, fmt.Sprintf("«%d ", hiddenLeft)) {
		t.Errorf("bar does not show the left marker for %d hidden tabs: %q", hiddenLeft, row)
	}
	if hiddenRight > 0 && !strings.Contains(row, fmt.Sprintf(" %d»", hiddenRight)) {
		t.Errorf("bar does not show the right marker for %d hidden tabs: %q", hiddenRight, row)
	}
}

// Sibling of TestTabSpansMatchThePaintedBarWhenTheBarOverflows: this fixture
// is scrolled so the LEFT marker is visible, and every span's start column
// (which must include the left marker's width) still has to match where its
// tab is actually painted.
func TestTabSpansMatchThePaintedBar_WithLeftMarkerVisible(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)

	cur := m
	var hiddenLeft int
	for i := 0; i < len(m.curTabs()); i++ {
		cur = wheelAtTabBar(t, cur, tea.MouseWheelDown)
		spans, hl, _ := cur.tabBarLayout()
		if hl > 0 && len(spans) >= 2 {
			hiddenLeft = hl
			break
		}
	}
	if hiddenLeft == 0 {
		t.Fatal("could not reach a scroll position with the left marker visible and 2+ tabs painted")
	}

	spans := cur.tabSpans()
	row := stripANSI(cur.renderTabBar())
	for _, s := range spans {
		want := stripANSI(s.text)
		got := cellIndexOf(row, want)
		if got < 0 {
			t.Errorf("tab %d is in tabSpans but not painted in the bar: %q", s.index, row)
			continue
		}
		if got != s.start {
			t.Errorf("tab %d is painted at cell column %d but tabSpans puts it at %d",
				s.index, got, s.start)
		}
	}
}

// Clicking a visible tab while scrolled switches to it but keeps the current
// scroll window — the bar must not jump back to auto-centering on the click.
func TestClickWhileScrolled_KeepsTheScrollWindow(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	m.client = fake
	wantOverflow(t, m)

	scrolled := wheelAtTabBar(t, m, tea.MouseWheelDown)
	beforeFirst := scrolled.tabSpans()[0].index

	spans := scrolled.tabSpans()
	// Click a visible tab that is not already active.
	var target tabSpan
	found := false
	for _, s := range spans {
		if s.index != scrolled.activeTabIdx() {
			target = s
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no non-active visible tab to click")
	}
	x := scrolled.projectSidebarWidth() + target.start
	updated, cmd := scrolled.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
	got := updated.(Model)
	if cmd != nil {
		cmd() // drain the switch-tab IPC send against the fake conn
	}

	if got.activeTabIdx() != target.index {
		t.Fatalf("active tab = %d after clicking tab %d, want %d", got.activeTabIdx(), target.index, target.index)
	}
	if got.tabSpans()[0].index != beforeFirst {
		t.Fatalf("first visible tab jumped from %d to %d on click — the scroll window must not move",
			beforeFirst, got.tabSpans()[0].index)
	}
}

// Clicking the RIGHTMOST visible tab while scrolled must not hide it. The
// active tab's "* " prefix costs two extra cells the instant it becomes
// active, so a click on a tab sitting right at the end of the window can
// grow it straight off the bar — the same window that painted it a moment
// ago, before the click. Geometry found by exhaustive search over (tab
// count, name length, bar width, active index): 5 one-character-named tabs,
// barW=15, scrolled so tabs D and E (indices 3, 4) are the only two visible
// — clicking E leaves less than 2 cells of spare budget once it grows.
func TestClickRightmostVisibleTabWhileScrolled_StaysVisible(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"A", "B", "C", "D", "E"}, 0)
	m.client = fake
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 15, 40

	m.tabScrollFirst = 3
	m.tabScrollAnchor = m.curTabs()[0].ID
	if !m.tabBarManualMode() {
		t.Fatal("fixture precondition: expected manual mode")
	}
	spansBefore := m.tabSpans()
	if len(spansBefore) == 0 {
		t.Fatal("fixture precondition: nothing visible")
	}
	target := spansBefore[len(spansBefore)-1]
	if target.index != 4 {
		t.Fatalf("fixture precondition: rightmost visible tab = %d, want 4 (E) — "+
			"this fixture no longer reproduces the geometry the search found", target.index)
	}

	x := m.projectSidebarWidth() + target.start
	updated, cmd := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
	got := updated.(Model)
	if cmd != nil {
		cmd()
	}

	if got.activeTabIdx() != target.index {
		t.Fatalf("active tab = %d after clicking tab %d, want %d", got.activeTabIdx(), target.index, target.index)
	}
	visible := false
	for _, s := range got.tabSpans() {
		if s.index == target.index {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatalf("clicked tab %d is not visible after the click — its own active-marker "+
			"growth pushed it off the bar", target.index)
	}
}

// A keyboard (or any non-click) tab switch returns the bar to auto mode: the
// anchor no longer names the active tab, so the newly active tab becomes
// visible again even if it was scrolled out of view.
func TestKeyboardSwitchWhileScrolled_ReturnsToAutoMode(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	m.client = fake
	wantOverflow(t, m)

	scrolled := wheelAtTabBar(t, m, tea.MouseWheelDown)
	if !scrolled.tabBarManualMode() {
		t.Fatal("fixture precondition: expected manual mode after scrolling")
	}

	// Switch to a tab that is currently scrolled out of view.
	target := len(scrolled.curTabs()) - 1
	if cmd := scrolled.switchTab(target); cmd != nil {
		cmd()
	}
	if scrolled.tabBarManualMode() {
		t.Fatal("manual mode still in effect after switchTab — the anchor must not follow the switch")
	}
	visible := false
	for _, s := range scrolled.tabSpans() {
		if s.index == target {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatalf("active tab %d is not visible after switching to it in auto mode", target)
	}
}

// Closing tabs so the stored tabScrollFirst exceeds the new maxFirst must not
// paint past the end or leave the wheel stuck: the layout clamps to a local
// copy, and the NEXT notch moves from that clamped value, not the stale one.
func TestScrollFirstPastMaxFirstAfterTabsClose_ClampsAndRecovers(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)

	cur := m
	for i := 0; i < len(m.curTabs()); i++ {
		cur = wheelAtTabBar(t, cur, tea.MouseWheelDown)
	}
	_, widths, barW, leftMarkerW, _ := cur.tabBarWidths()
	oldMaxFirst := maxFirstIndex(widths, barW, leftMarkerW)
	if cur.tabScrollFirst != oldMaxFirst || oldMaxFirst == 0 {
		t.Fatalf("fixture precondition: tabScrollFirst=%d, oldMaxFirst=%d", cur.tabScrollFirst, oldMaxFirst)
	}

	// Close tabs from the tail — down to 5, which still overflows the bar —
	// narrowing what maxFirst can be while leaving the stored tabScrollFirst
	// (and its anchor, still the active tab) alone.
	proj := cur.projects[cur.activeProject]
	proj.tabs = proj.tabs[:5]

	spans, _, _ := cur.tabBarLayout()
	if len(spans) == 0 {
		t.Fatal("layout painted nothing after closing tabs")
	}
	if len(spans) == len(proj.tabs) {
		t.Fatal("fixture precondition: the 5 remaining tabs must still overflow the bar")
	}
	_, widths, barW, leftMarkerW, _ = cur.tabBarWidths()
	newMaxFirst := maxFirstIndex(widths, barW, leftMarkerW)
	// The fixture must overshoot by at least 2, or a broken implementation
	// that clamps only the RESULT (clamp(oldMaxFirst+delta, 0, newMaxFirst))
	// instead of the START produces the same value as the correct one and
	// the exact-value assertion below cannot tell them apart: with
	// oldMaxFirst=newMaxFirst+1, clamp(oldMaxFirst-1, 0, newMaxFirst) is
	// already newMaxFirst-1, same as clamping the start first.
	if newMaxFirst > oldMaxFirst-2 {
		t.Fatalf("fixture precondition: newMaxFirst=%d must be at most oldMaxFirst-2=%d "+
			"or a missing pre-clamp cannot be distinguished from a correct one", newMaxFirst, oldMaxFirst-2)
	}
	if spans[0].index > newMaxFirst {
		t.Fatalf("first visible tab = %d after closing tabs, want clamped to <= %d", spans[0].index, newMaxFirst)
	}

	// The next notch must move from the CLAMPED position, not the stale one.
	// Asserting only membership in [0,newMaxFirst] cannot catch a missing
	// pre-clamp: clamping just the result — clamp(oldMaxFirst-1, 0,
	// newMaxFirst) — also lands in that range (at newMaxFirst itself, per the
	// precondition above), so the exact expected value is asserted instead:
	// newMaxFirst-1, which only clamping the START to newMaxFirst BEFORE
	// applying the notch can produce.
	next := wheelAtTabBar(t, cur, tea.MouseWheelUp)
	want := newMaxFirst - 1
	if want < 0 {
		want = 0
	}
	if next.tabScrollFirst != want {
		t.Fatalf("tabScrollFirst = %d after a notch past a closed-tab clamp, want exactly %d "+
			"(newMaxFirst-1, reached only by clamping the stale start BEFORE the notch)",
			next.tabScrollFirst, want)
	}
}

// When every tab already fits, the wheel over the tab bar is a pure no-op:
// no state change, and the event is swallowed rather than reaching the pane.
func TestWheelOverTabBar_AllTabsFit_NoOp(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel([]string{"A", "B", "C"}, 0)
	if len(m.tabSpans()) != len(m.curTabs()) {
		t.Fatal("fixture overflows — this test needs every tab to fit")
	}
	wantScrollableHistory(t, m)

	for _, btn := range []tea.MouseButton{tea.MouseWheelDown, tea.MouseWheelUp} {
		got := wheelAtTabBar(t, m, btn)
		if got.tabScrollFirst != 0 || got.tabScrollAnchor != "" {
			t.Fatalf("wheel(%v) over a fully-fitting bar changed state: first=%d anchor=%q",
				btn, got.tabScrollFirst, got.tabScrollAnchor)
		}
	}

	// Wheel-UP is the discriminating probe for "never reaches the pane":
	// scrollBack starts at 0, and ScrollDown floors there too (so a
	// down-notch leak is invisible), but ScrollUp on a pane with real
	// history (wantScrollableHistory) would move scrollBack off 0 the moment
	// the event reached PaneModel.ScrollUp.
	upped := wheelAtTabBar(t, m, tea.MouseWheelUp)
	if s := activePaneScrollBack(t, upped); s != 0 {
		t.Fatalf("active pane scrollBack = %d after wheel-up over a fully-fitting bar, want 0 — "+
			"the wheel must not reach the pane", s)
	}
}

// A wheel at Y==0 but X inside the project sidebar's column is the sidebar's
// event, not the tab bar's — existing sidebar wheel behaviour must be
// unaffected by the new tab-bar branch.
func TestWheelAtRow0InsideProjectSidebar_NotTreatedAsTabBar(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	// Widened past minWidthForSidebar (100) — sidebarWidth() collapses to 0
	// below that regardless of sidebarOpen, which would make this fixture
	// silently test nothing. Still narrow enough (78 of pane column left)
	// that the eight tabs overflow it.
	m.width = 120
	m.sidebarOpen = true
	m.sidebarWidth = 22
	wantOverflow(t, m)

	x := 5
	if !m.projectSidebarSwallowsMouse(x, 0) {
		t.Fatalf("fixture precondition: (%d, 0) must be inside the project sidebar's swallow zone", x)
	}
	if x >= m.projectSidebarWidth() {
		t.Fatalf("fixture precondition: x=%d must be less than projectSidebarWidth()=%d", x, m.projectSidebarWidth())
	}

	updated, _ := m.Update(tea.MouseWheelMsg{X: x, Y: 0, Button: tea.MouseWheelDown})
	got := updated.(Model)

	if got.tabScrollFirst != 0 || got.tabScrollAnchor != "" {
		t.Fatalf("wheel inside the project sidebar touched tab-bar scroll state: first=%d anchor=%q",
			got.tabScrollFirst, got.tabScrollAnchor)
	}
}

// Starting an inline tab rename (F2, default binding for tab.rename) while
// scrolled must return the bar to auto mode: renaming a tab that is
// scrolled out of view would type into a label nobody can see.
func TestTabRenameWhileScrolled_ReturnsToAutoMode(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	wantOverflow(t, m)

	scrolled := wheelAtTabBar(t, m, tea.MouseWheelDown)
	if !scrolled.tabBarManualMode() {
		t.Fatal("fixture precondition: expected manual mode after scrolling")
	}

	updated, _ := scrolled.handleKey(tea.KeyPressMsg{Code: tea.KeyF2})
	got := updated.(Model)
	if !got.renaming {
		t.Fatal("F2 did not enter tab-rename mode")
	}
	if got.tabBarManualMode() {
		t.Fatal("manual mode still in effect after starting a tab rename — the active tab may be scrolled out of view")
	}
	visible := false
	for _, s := range got.tabSpans() {
		if s.index == got.activeTabIdx() {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatal("active tab is not visible after starting a rename in auto mode")
	}
}

// Switching away from a scrolled tab and back to it must NOT resurrect the
// stale scroll window. tabBarManualMode's compare only makes a mismatched
// anchor INERT while some other tab is active; coming back to the ORIGINAL
// tab by any non-click means (here, two ordinary Alt+N switches) makes the
// compare true again unless something actually CLEARS the anchor in
// between — which is exactly what Model.Update's defer
// (normalizeTabScrollAnchor) does. Driven through Update, not by calling
// switchTab directly, because that defer is the fix under test.
func TestSwitchAwayAndBack_ReturnsToAutoMode(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	m.client = newFakeConn()
	wantOverflow(t, m)

	scrolled := wheelAtTabBar(t, m, tea.MouseWheelDown)
	if !scrolled.tabBarManualMode() {
		t.Fatal("fixture precondition: expected manual mode after scrolling")
	}
	hiddenAfterScroll := true
	for _, s := range scrolled.tabSpans() {
		if s.index == 0 {
			hiddenAfterScroll = false
			break
		}
	}
	if !hiddenAfterScroll {
		t.Fatal("fixture precondition: tab 0 must be scrolled out of view")
	}

	// Away: Alt+2 switches to tab index 1 (tab.switch_2).
	updated, _ := scrolled.Update(tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt})
	away := updated.(Model)
	if away.activeTabIdx() != 1 {
		t.Fatalf("fixture precondition: Alt+2 switched to tab %d, want 1", away.activeTabIdx())
	}

	// Back: Alt+1 switches back to tab 0 — the ORIGINAL tab, the one the
	// stale anchor still names.
	updated, _ = away.Update(tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt})
	back := updated.(Model)
	if back.activeTabIdx() != 0 {
		t.Fatalf("Alt+1 switched to tab %d, want 0", back.activeTabIdx())
	}
	if back.tabBarManualMode() {
		t.Fatal("manual mode reactivated on returning to the originally-scrolled tab")
	}
	visible := false
	for _, s := range back.tabSpans() {
		if s.index == 0 {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatal("active tab 0 is not visible after switching away and back — the stale scroll window was resurrected")
	}
}

// Same class of bug, one level up: switching PROJECT away and back must also
// return the bar to auto mode. A project switch changes activeTabIdx()'s
// meaning without ever touching tabScrollAnchor, so the away leg alone
// (moving to a project whose active tab has a different ID) is what the
// normalize defer clears against; the anchor is already gone by the time the
// user comes back.
func TestSwitchProjectAwayAndBack_ReturnsToAutoMode(t *testing.T) {
	t.Parallel()
	m := newTabBarScrollModel(eightOverflowingTabNames(), 0)
	m.client = newFakeConn()
	m.projects = append(m.projects, &ProjectModel{
		ID: "proj-b", Name: "B",
		tabs: []*TabModel{NewTabModel("only", "Only")},
	})
	wantOverflow(t, m)

	scrolled := wheelAtTabBar(t, m, tea.MouseWheelDown)
	if !scrolled.tabBarManualMode() {
		t.Fatal("fixture precondition: expected manual mode after scrolling")
	}

	// Away: Alt+Shift+Right (project.next) switches to project B.
	updated, _ := scrolled.Update(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: tea.KeyRight})
	away := updated.(Model)
	if away.activeProject != 1 {
		t.Fatalf("fixture precondition: project.next moved to project %d, want 1", away.activeProject)
	}

	// Back: Alt+Shift+Left (project.prev) returns to project A — the one
	// the stale anchor still names.
	updated, _ = away.Update(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: tea.KeyLeft})
	back := updated.(Model)
	if back.activeProject != 0 {
		t.Fatalf("project.prev moved to project %d, want 0", back.activeProject)
	}
	if back.tabBarManualMode() {
		t.Fatal("manual mode reactivated on returning to the originally-scrolled project")
	}
	visible := false
	for _, s := range back.tabSpans() {
		if s.index == back.activeTabIdx() {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatal("active tab is not visible after switching project away and back")
	}
}
