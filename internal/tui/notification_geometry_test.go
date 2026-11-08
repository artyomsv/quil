package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/charmbracelet/x/ansi"
)

// liveLoc reports every pane as alive, in project "proj", tab "tab".
func liveLoc(paneID string) paneSource { return paneSource{Label: "proj · tab", Alive: true} }

// deadLoc reports every pane as gone.
func deadLoc(paneID string) paneSource { return paneSource{} }

func geomEvent(id, title, message string) ipc.PaneEventPayload {
	return ipc.PaneEventPayload{
		ID: id, PaneID: "pane-" + id, PaneName: "shell",
		Type: "process_exit", Title: title, Message: message,
		Severity: "info", Timestamp: 0,
	}
}

// linesText extracts the text of each rendered line.
func linesText(lines []renderedLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text
	}
	return out
}

func renderedText(lines []renderedLine) string {
	return ansi.Strip(strings.Join(linesText(lines), "\n"))
}

// A card with no Message is one line shorter than one with it. This is the
// whole reason the viewport is indexed in LINES rather than cards.
func TestNotificationLines_VariableCardHeight(t *testing.T) {
	withExcerpt := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "the last output line")},
		28, 0, false, liveLoc,
	)
	without := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "")},
		28, 0, false, liveLoc,
	)
	if len(withExcerpt) != len(without)+1 {
		t.Errorf("card heights: with excerpt %d lines, without %d; want exactly one more",
			len(withExcerpt), len(without))
	}
}

// Every line names the event it belongs to, or -1 for chrome. This is what the
// mouse hit test reads.
func TestNotificationLines_EventIndexPerLine(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{
			geomEvent("TEST-1", "first", "excerpt one"),
			geomEvent("TEST-2", "second", ""),
		},
		28, 0, false, liveLoc,
	)

	seen := map[int]int{}
	for _, l := range lines {
		seen[l.eventIdx]++
	}
	if seen[0] != 4 {
		t.Errorf("event 0 lines: got %d, want 4 (name, title, location, excerpt)", seen[0])
	}
	if seen[1] != 3 {
		t.Errorf("event 1 lines: got %d, want 3 (name, title, location)", seen[1])
	}
	if seen[-1] < 2 {
		t.Errorf("separator lines: got %d, want at least 2 (one per card plus the tail)", seen[-1])
	}
}

// The location line names project and tab, so a click's destination is visible
// before the click.
func TestNotificationLines_ShowsLocation(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "")},
		40, 0, false, liveLoc,
	)
	if joined := renderedText(lines); !strings.Contains(joined, "proj · tab") {
		t.Errorf("rendered card has no location line:\n%s", joined)
	}
}

// A card whose pane is gone must not advertise a jump it cannot perform.
func TestNotificationLines_DeadPaneIsMarked(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Pane closed", "")},
		40, 0, false, deadLoc,
	)
	if joined := renderedText(lines); !strings.Contains(joined, "(closed)") {
		t.Errorf("dead pane card has no (closed) marker:\n%s", joined)
	}
}

// A nil locator is what a test that does not care passes. It must render, and
// must not claim every pane is closed.
func TestNotificationLines_NilLocatorRenders(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "")},
		40, 0, false, nil,
	)
	joined := renderedText(lines)
	if !strings.Contains(joined, "Process exited") {
		t.Errorf("nil locator lost the card:\n%s", joined)
	}
	if strings.Contains(joined, "(closed)") {
		t.Errorf("nil locator marked a live pane closed:\n%s", joined)
	}
}

// Every rendered line must fit the sidebar in CELLS, not runes.
//
// This is a correctness requirement rather than a cosmetic one, and it is new
// with this feature. lipgloss WRAPS rather than clips, so one over-wide line
// becomes two screen rows — and eventIndexAtRow maps a screen row to a card by
// indexing the logical line list, assuming one screen row per line. A single
// wrapped line therefore shifts every card below it, and a click selects,
// navigates to, or dismisses a different notification than the one under the
// pointer. While the sidebar was keyboard-only an over-wide line was merely
// ugly; now the display IS the decision.
//
// 构建 is 2 runes and 4 cells, so a rune-counting cut passes twice the budget.
func TestNotificationLines_WideGlyphsStayWithinTheWidth(t *testing.T) {
	const innerW = 28
	wide := ipc.PaneEventPayload{
		ID:       "TEST-1",
		PaneID:   "pane-TEST-1",
		PaneName: "构建构建构建构建构建构建构建构建构建构建",
		Type:     "process_exit",
		Title:    "进程已退出进程已退出进程已退出进程已退出进程已退出",
		Message:  "最后一行输出最后一行输出最后一行输出最后一行输出",
		Severity: "error",
		Data:     map[string]string{"count": "12"},
	}
	wideLoc := func(string) paneSource {
		return paneSource{Label: "项目名称项目名称项目名称 · 标签页标签页标签页", Alive: true}
	}

	for _, l := range notificationLines([]ipc.PaneEventPayload{wide}, innerW, 0, true, wideLoc) {
		if w := lipgloss.Width(l.text); w > innerW {
			t.Errorf("line %q is %d cells wide, want at most %d — lipgloss will wrap it and desync the hit test",
				ansi.Strip(l.text), w, innerW)
		}
	}
}

// The name/age row pads the gap between them, so both operands must be
// measured in cells too, or the padding overshoots by the wide runes' extra
// columns.
func TestNotificationLines_WideNameKeepsTheAgeOnTheSameRow(t *testing.T) {
	const innerW = 24
	lines := notificationLines([]ipc.PaneEventPayload{{
		ID: "TEST-1", PaneID: "pane-TEST-1",
		PaneName: "构建构建构建构建构建构建",
		Type:     "process_exit", Title: "t", Severity: "info",
	}}, innerW, 0, false, liveLoc)

	for _, l := range lines {
		if w := lipgloss.Width(l.text); w > innerW {
			t.Errorf("line %q is %d cells, want at most %d", ansi.Strip(l.text), w, innerW)
		}
	}
}

// The cheap arithmetic pass and the styling renderer must produce the same
// shape, or a click resolves against a layout that was never drawn.
//
// Asserted against a FIXED expected list, not by comparing the two functions to
// each other — a self-comparison passes on any geometry, including a broken
// one. The fixture mixes a card with an excerpt and one without, which is the
// only thing that varies a card's height.
func TestNotificationLineOwners_MatchesTheRenderer(t *testing.T) {
	events := []ipc.PaneEventPayload{
		geomEvent("TEST-1", "first", "an excerpt"),
		geomEvent("TEST-2", "second", ""),
	}

	// separator, name, title, location, excerpt | separator, name, title,
	// location | trailing separator.
	want := []int{-1, 0, 0, 0, 0, -1, 1, 1, 1, -1}
	got := notificationLineOwners(events)

	if len(got) != len(want) {
		t.Fatalf("owners length: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("owners[%d]: got %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}

	// And the renderer agrees with that same fixed shape.
	lines := notificationLines(events, 28, 0, false, liveLoc)
	if len(lines) != len(want) {
		t.Fatalf("renderer produced %d lines, owners %d — the two passes disagree",
			len(lines), len(want))
	}
	for i := range want {
		if lines[i].eventIdx != want[i] {
			t.Errorf("renderer line %d owner: got %d, want %d", i, lines[i].eventIdx, want[i])
		}
	}
}

func TestNotificationLineOwners_Empty(t *testing.T) {
	if got := notificationLineOwners(nil); len(got) != 0 {
		t.Errorf("owners for an empty list: got %v, want none", got)
	}
}

// Shrinking the terminal below the draw threshold leaves an undrawn strip. A
// click there must resolve to nothing, or it selects and dismisses a card the
// user cannot see.
func TestEventIndexAtRow_RefusesWhenTheSidebarIsNotDrawn(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 10; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	nc.ScrollBy(5, 20)

	// height 4 => innerH 2, below View's `innerH < 3` refusal.
	if got := nc.eventIndexAtRow(4, 4, liveLoc); got != -1 {
		t.Errorf("eventIndexAtRow on an undrawn sidebar: got %d, want -1", got)
	}
}

// revealCursor's scroll-UP branch. Only the downward direction was covered, so
// `if first < nc.scroll` could be replaced with `if false` and stay green —
// meaning `k` back above the fold would never bring the card into view.
func TestRevealCursor_ScrollsUpToTheSelection(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	nc.SelectIndex(15, height) // scrolls down
	deep := nc.scroll
	if deep == 0 {
		t.Fatal("fixture did not scroll down; the up branch cannot be exercised")
	}

	nc.cursor = 0
	nc.revealCursor(height)

	if nc.scroll >= deep {
		t.Fatalf("scroll after selecting the newest card: got %d, want less than %d", nc.scroll, deep)
	}
	// The contract is that the selected card is WHOLE on screen, not that the
	// scroll hits a particular number — card 0's first line is its separator,
	// which the viewport is allowed to leave above the fold.
	owners := notificationLineOwners(nc.visibleEvents())
	vh := notifyViewportHeight(height)
	for i, owner := range owners {
		if owner != 0 {
			continue
		}
		if i < nc.scroll || i >= nc.scroll+vh {
			t.Errorf("line %d of the selected card is outside the viewport [%d, %d)",
				i, nc.scroll, nc.scroll+vh)
		}
	}
}

// A sidebar too narrow or too short to draw must render nothing rather than
// something the box then wraps.
func TestView_RefusesDegenerateSizes(t *testing.T) {
	nc := NewNotificationCenter(6, 50)
	nc.AddEvent(geomEvent("TEST-1", "title", "excerpt"))

	if got := nc.View(4, nil); got != "" {
		t.Errorf("View at height 4: got %q, want empty", got)
	}

	narrow := NewNotificationCenter(5, 50)
	narrow.AddEvent(geomEvent("TEST-1", "title", "excerpt"))
	if got := narrow.View(20, nil); got != "" {
		t.Errorf("View at width 5 (innerW 3): got %q, want empty", got)
	}
}

func TestNotificationLines_RefusesANarrowBox(t *testing.T) {
	got := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "title", "excerpt")},
		4, 0, false, liveLoc,
	)
	if got != nil {
		t.Errorf("notificationLines at innerW 4: got %d lines, want none", len(got))
	}
}

// The floor matters because the viewport height is used as a divisor and a
// window size; zero or negative would make clampScroll compute a bogus bound.
func TestNotifyViewportHeight_FloorsAtOne(t *testing.T) {
	for _, h := range []int{0, 1, 4, -3} {
		if got := notifyViewportHeight(h); got < 1 {
			t.Errorf("notifyViewportHeight(%d) = %d, want at least 1", h, got)
		}
	}
}

// The empty state must fit the box it declares. lipgloss does not clip, so one
// extra interior row is drawn past the bottom border.
func TestView_EmptyStateFitsTheMinimumHeight(t *testing.T) {
	nc := NewNotificationCenter(30, 50)

	const height = 5 // innerH == 3, the smallest View will draw
	got := nc.View(height, nil)
	if got == "" {
		t.Fatal("View refused the minimum drawable height")
	}
	if n := len(strings.Split(got, "\n")); n != height {
		t.Errorf("box height: got %d rows, want %d", n, height)
	}
}

func TestScrollBy_ClampsAtBothEnds(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	nc.ScrollBy(-5, height)
	if nc.scroll != 0 {
		t.Errorf("scroll after scrolling up from the top: got %d, want 0", nc.scroll)
	}

	nc.ScrollBy(10000, height)
	total := len(notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, nil))
	want := total - notifyViewportHeight(height)
	if want < 0 {
		want = 0
	}
	if nc.scroll != want {
		t.Errorf("scroll after scrolling past the end: got %d, want %d", nc.scroll, want)
	}
}

// Reading history is impossible on a busy workspace if the text slides out from
// under the reader on every arrival.
//
// An UNCHANGED scroll offset is not the contract, and asserting it was wrong:
// nc.scroll indexes a list that grows from the TOP, so a new five-line card
// inserted above leaves the same number naming content five lines newer. What
// must hold is that the same CARD stays at the top of the viewport — so the
// offset has to move by exactly the height of what was inserted.
//
// The wheel scrolls without moving the cursor, so "cursor 0, scrolled deep into
// history" is the ordinary state of someone reading back, and it is the case a
// cursor-only fix misses.
func TestAddEvent_KeepsTheViewportOnTheSameCard(t *testing.T) {
	for _, cursor := range []int{0, 3} {
		nc := NewNotificationCenter(30, 50)
		for i := 0; i < 20; i++ {
			nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
		}
		nc.cursor = cursor
		nc.ScrollBy(6, 20)
		if nc.scroll == 0 {
			t.Fatalf("cursor=%d: fixture did not scroll; the test proves nothing", cursor)
		}

		before := ownerAtLine(nc, nc.scroll)
		if before == "" {
			t.Fatalf("cursor=%d: no card at the viewport top", cursor)
		}

		nc.AddEvent(geomEvent("TEST-new", "arriving", "excerpt"))

		if after := ownerAtLine(nc, nc.scroll); after != before {
			t.Errorf("cursor=%d: viewport top was %q, is now %q — the list shifted under the reader",
				cursor, before, after)
		}
	}
}

// ownerAtLine returns the id of the card occupying a viewport line, or "".
func ownerAtLine(nc *NotificationCenter, line int) string {
	vis := nc.visibleEvents()
	owners := notificationLineOwners(vis)
	for i := line; i < len(owners); i++ {
		if idx := owners[i]; idx >= 0 && idx < len(vis) {
			return vis[idx].ID
		}
	}
	return ""
}

// Revealing a hidden group inserts cards above the viewport too.
func TestSetGroups_KeepsTheViewportOnTheSameCard(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-v"+string(rune('a'+i)), "title", "excerpt"))
	}
	// A hidden card that will appear ABOVE the viewport when its group is on.
	nc.AddEvent(ipc.PaneEventPayload{
		ID: "TEST-hidden", PaneID: "pane-h", PaneName: "shell",
		Type: "output_idle", Title: "Output idle", Message: "x", Severity: "info",
	})
	nc.ScrollBy(6, 20)
	before := ownerAtLine(nc, nc.scroll)
	if before == "" {
		t.Fatal("fixture: no card at the viewport top")
	}

	nc.SetGroups(showOnly(groupProcess, groupIdle))

	if after := ownerAtLine(nc, nc.scroll); after != before {
		t.Errorf("viewport top was %q, is now %q — revealing a group shifted the list",
			before, after)
	}
}

// At the newest edge the viewport FOLLOWS the top, rather than being anchored:
// scroll 0 means "show the newest", and a new event landing there is what the
// user wants to see.
func TestAddEvent_AtTheTopTheViewportFollows(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(geomEvent("TEST-1", "title", "excerpt"))

	nc.AddEvent(geomEvent("TEST-2", "arriving", "excerpt"))

	if nc.scroll != 0 {
		t.Errorf("scroll at the newest edge: got %d, want 0", nc.scroll)
	}
	if got := ownerAtLine(nc, 0); got != "TEST-2" {
		t.Errorf("top card: got %q, want the newest (TEST-2)", got)
	}
}

func TestEventIndexAtRow(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(geomEvent("TEST-1", "first", "excerpt"))
	nc.AddEvent(geomEvent("TEST-2", "second", "excerpt"))
	const height = 20

	// Row 0 is the tab bar, 1 the box top border, 2 the title — all chrome.
	for _, y := range []int{0, 1, 2} {
		if got := nc.eventIndexAtRow(y, height, liveLoc); got != -1 {
			t.Errorf("eventIndexAtRow(%d) = %d, want -1 (chrome)", y, got)
		}
	}
	// Viewport line 0 (screen row 3) is the newest card's separator; line 1
	// (screen row 4) is its name line.
	if got := nc.eventIndexAtRow(3, height, liveLoc); got != -1 {
		t.Errorf("eventIndexAtRow(3) = %d, want -1 (separator)", got)
	}
	if got := nc.eventIndexAtRow(4, height, liveLoc); got != 0 {
		t.Errorf("eventIndexAtRow(4) = %d, want 0 (newest card's name line)", got)
	}
	// Far below the viewport.
	if got := nc.eventIndexAtRow(height+5, height, liveLoc); got != -1 {
		t.Errorf("eventIndexAtRow past the viewport = %d, want -1", got)
	}
}

// The hit test must follow the scroll, or a click lands on the card that was
// there before the user scrolled.
func TestEventIndexAtRow_FollowsScroll(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 10; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	atTop := nc.eventIndexAtRow(4, height, liveLoc)
	nc.ScrollBy(5, height)
	afterScroll := nc.eventIndexAtRow(4, height, liveLoc)

	if atTop == afterScroll {
		t.Errorf("eventIndexAtRow(4) is %d both before and after scrolling; the hit test ignores scroll", atTop)
	}
}

// SelectIndex must bring an off-screen card into view.
func TestSelectIndex_RevealsCursor(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	nc.SelectIndex(15, height)

	if nc.cursor != 15 {
		t.Fatalf("cursor: got %d, want 15", nc.cursor)
	}
	lines := notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, nil)
	last := -1
	for i, l := range lines {
		if l.eventIdx == 15 {
			last = i
		}
	}
	if last < nc.scroll || last >= nc.scroll+notifyViewportHeight(height) {
		t.Errorf("selected card's last line %d is outside the viewport [%d, %d)",
			last, nc.scroll, nc.scroll+notifyViewportHeight(height))
	}
}

// The empty state must say the filter is why, when a filter is installed.
func TestView_EmptyStateMentionsFilter(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-1", Type: "output_idle"})

	out := ansi.Strip(nc.View(20, nil))
	if !strings.Contains(out, "show all") {
		t.Errorf("filtered empty state does not mention the override:\n%s", out)
	}
}

// A tab holding ONE pane is named by its tab. That is the name the user typed
// for this piece of work; the pane under it has usually never been named, and
// "pane-fd2d33b" identifies nothing.
func TestPaneLocator_SinglePaneTabUsesTheTabName(t *testing.T) {
	only := NewPaneModel("pane-only", 1024)
	tab := tabWith(only)
	tab.Name = "Test"
	m := Model{projects: []*ProjectModel{{ID: "p", Name: "Default", tabs: []*TabModel{tab}}}}

	src := m.paneLocator()("pane-only")

	if src.Name != "Test" {
		t.Errorf("Name: got %q, want the tab name %q", src.Name, "Test")
	}
	// The tab is now the card's title, so repeating it underneath would put the
	// same word on two adjacent lines.
	if src.Label != "Default" {
		t.Errorf("Label: got %q, want the project alone %q", src.Label, "Default")
	}
	if !src.Alive {
		t.Error("Alive: got false, want true")
	}
}

// With several panes the tab name no longer picks one out, so the pane's own
// name is the only thing that does — and the label carries the tab again.
func TestPaneLocator_MultiPaneTabUsesThePaneName(t *testing.T) {
	first := NewPaneModel("pane-first", 1024)
	first.Name = "build"
	second := NewPaneModel("pane-second", 1024)
	tab := tabWith(first, second)
	tab.Name = "Test"
	m := Model{projects: []*ProjectModel{{ID: "p", Name: "Default", tabs: []*TabModel{tab}}}}

	src := m.paneLocator()("pane-first")

	if src.Name != "build" {
		t.Errorf("Name: got %q, want the pane name %q", src.Name, "build")
	}
	if src.Label != "Default · Test" {
		t.Errorf("Label: got %q, want %q", src.Label, "Default · Test")
	}
}

// An unnamed pane in a multi-pane tab has no name to offer, so the renderer
// falls back to the event's own PaneName and then to a truncated id.
func TestNotificationLines_FallsBackWhenTheLocatorHasNoName(t *testing.T) {
	noName := func(string) paneSource { return paneSource{Label: "Default · Test", Alive: true} }
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Reply ready", "")},
		40, 0, false, noName,
	)
	if joined := renderedText(lines); !strings.Contains(joined, "shell") {
		t.Errorf("card lost the event's own pane name:\n%s", joined)
	}
}

// A rollback card can be the ONLY report of what an abandoned worktree create
// left behind (a recovered MCP split or new tab, a template), and its source
// pane is usually gone. At the default sidebar width (inner 28 cells) its
// outcome-first title and message must survive the one-line cut whole.
//
// Fails if the daemon's rollback texts grow past the default card width.
func TestNotificationLines_RollbackOutcomeFitsTheDefaultWidth(t *testing.T) {
	for _, c := range []struct{ title, message string }{
		{"Branch kept: feat/x", "folder gone, branch kept"},
		{"Cleanup incomplete: wt", "folder may remain; see log"},
		{"Cleanup incomplete: wt", "cleanup failed; see log"},
	} {
		out := renderedText(notificationLines(
			[]ipc.PaneEventPayload{geomEvent("TEST-1", c.title, c.message)},
			28, 0, false, deadLoc,
		))
		for _, want := range []string{c.title, c.message, "(closed)"} {
			if !strings.Contains(out, want) {
				t.Errorf("card at the default width lost %q:\n%s", want, out)
			}
		}
	}
}
