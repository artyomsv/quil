package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
)

// newTabBarScrollModel builds a model the same way newModelForTest does, but
// gives every tab a real PaneModel — the "no pane scrolled" claim in these
// tests needs a real scroll offset to observe, not a nil ActivePaneModel that
// could never move. Sized at 60x40 with no project sidebar, which the fixture
// below (8 ten-character tab names) reliably overflows.
func newTabBarScrollModel(names []string, activeIdx int) Model {
	m := newModelForTest(names, activeIdx)
	for i, tab := range m.curTabs() {
		pane := NewPaneModel(fmt.Sprintf("pane-%d", i), 256)
		pane.Active = true
		tab.Root = NewLeaf(pane)
		tab.ActivePane = pane.ID
	}
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 60, 40
	return m
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

	beforeSpans := m.tabSpans()
	beforeFirst := beforeSpans[0].index
	beforeActiveID := m.curTabs()[m.activeTabIdx()].ID
	beforeScroll := activePaneScrollBack(t, m)

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
	if got := activePaneScrollBack(t, got); got != beforeScroll {
		t.Fatalf("active pane scrollBack = %d, want unchanged %d — the wheel must not reach the pane",
			got, beforeScroll)
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
		want := fmt.Sprintf("%d:%s", s.index+1, cur.curTabs()[s.index].Name)
		got := strings.Index(row, want)
		if got < 0 {
			t.Errorf("tab %d is in tabSpans but not painted in the bar: %q", s.index, row)
			continue
		}
		if got < s.start || got >= s.start+s.width {
			t.Errorf("tab %d is painted at column %d but tabSpans puts it at [%d,%d)",
				s.index, got, s.start, s.start+s.width)
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
	if newMaxFirst >= oldMaxFirst {
		t.Fatalf("fixture precondition: newMaxFirst=%d must be less than oldMaxFirst=%d "+
			"or the stale tabScrollFirst is never actually out of range", newMaxFirst, oldMaxFirst)
	}
	if spans[0].index > newMaxFirst {
		t.Fatalf("first visible tab = %d after closing tabs, want clamped to <= %d", spans[0].index, newMaxFirst)
	}

	// The next notch must move from the CLAMPED position, not the stale one:
	// wheel-up should move to newMaxFirst-1 (or stay at 0), never underflow
	// or jump from the old, now out-of-range, value.
	next := wheelAtTabBar(t, cur, tea.MouseWheelUp)
	if next.tabScrollFirst < 0 || next.tabScrollFirst > newMaxFirst {
		t.Fatalf("tabScrollFirst = %d after a notch past a closed-tab clamp, want within [0,%d]",
			next.tabScrollFirst, newMaxFirst)
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
	beforeScroll := activePaneScrollBack(t, m)

	got := wheelAtTabBar(t, m, tea.MouseWheelDown)

	if got.tabScrollFirst != 0 || got.tabScrollAnchor != "" {
		t.Fatalf("wheel over a fully-fitting bar changed state: first=%d anchor=%q",
			got.tabScrollFirst, got.tabScrollAnchor)
	}
	if s := activePaneScrollBack(t, got); s != beforeScroll {
		t.Fatalf("active pane scrollBack = %d, want unchanged %d", s, beforeScroll)
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
