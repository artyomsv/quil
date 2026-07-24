package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// hitRows returns just the palActGoToPane rows from a display list.
func hitRows(display []paletteCommand) []paletteCommand {
	var hits []paletteCommand
	for _, c := range display {
		if c.action == palActGoToPane && !c.header && !c.info {
			hits = append(hits, c)
		}
	}
	return hits
}

func TestApplyPaneSearch_BuildsGoToPaneRows(t *testing.T) {
	m := newSplitDragTestModel(t) // panes p1, p2 on tab 0
	m.palette.query = "p"
	m.palette.searching = true

	resp := ipc.PaneSearchRespPayload{
		Query: "p",
		Hits: []ipc.PaneSearchHit{
			{PaneID: "p1", Matches: 3, Excerpt: "prompt here"},
			{PaneID: "p2", Matches: 1, Excerpt: "another"},
		},
	}
	m2 := m.applyPaneSearch(resp)
	if m2.palette.searching {
		t.Error("searching should clear after a response")
	}
	if len(m2.palette.contentHits) != 2 {
		t.Fatalf("contentHits = %d, want 2", len(m2.palette.contentHits))
	}
	h0 := m2.palette.contentHits[0]
	if h0.action != palActGoToPane || h0.arg != "p1" || !h0.enabled {
		t.Errorf("hit0 should be an enabled palActGoToPane row for p1: %+v", h0)
	}
	if h0.detail != "3×" {
		t.Errorf("hit0 detail = %q, want %q", h0.detail, "3×")
	}
	if h0.excerpt != "prompt here" {
		t.Errorf("hit0 excerpt = %q", h0.excerpt)
	}
	if !strings.Contains(h0.label, "p1") && !strings.Contains(h0.label, "1.1") {
		t.Errorf("label should identify the pane: %q", h0.label)
	}
}

func TestApplyPaneSearch_DropsStale(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.query = "current"
	m.palette.contentHits = []paletteCommand{{action: palActGoToPane, arg: "p1", enabled: true, label: "old"}}

	stale := ipc.PaneSearchRespPayload{Query: "old-query", Hits: []ipc.PaneSearchHit{{PaneID: "p2", Matches: 9}}}
	m2 := m.applyPaneSearch(stale)
	if len(m2.palette.contentHits) != 1 || m2.palette.contentHits[0].arg != "p1" {
		t.Errorf("stale response must not replace hits, got %+v", m2.palette.contentHits)
	}
}

// TestApplyPaneSearch_AcceptsDaemonEcho is the TUI half of the staleness seam
// pinned daemon-side by TestPaneSearchResponse_EchoesQueryVerbatim: whatever
// query the TUI sends, the daemon echoes it verbatim, and the TUI must ACCEPT
// that response. Queries with leading/trailing whitespace are the ones that broke
// when the daemon trimmed its echo — the response was dropped forever and the
// palette hung on "Searching…".
func TestApplyPaneSearch_AcceptsDaemonEcho(t *testing.T) {
	for _, query := range []string{"refused", " refused", "refused ", "two words"} {
		t.Run(fmt.Sprintf("%q", query), func(t *testing.T) {
			m := newSplitDragTestModel(t)
			m.palette.query = query
			m.palette.searching = true
			m.palette.timedOut = true // a late response must clear this too

			// What the daemon sends back: the request query, verbatim.
			resp := ipc.PaneSearchRespPayload{
				Query: query,
				Hits:  []ipc.PaneSearchHit{{PaneID: "p1", Matches: 2, Excerpt: "connection refused"}},
			}
			got := m.applyPaneSearch(resp)
			if len(got.palette.contentHits) != 1 {
				t.Fatalf("contentHits = %d, want 1 — the daemon's own echo must never look stale", len(got.palette.contentHits))
			}
			if got.palette.searching {
				t.Error("searching should clear once the response is applied")
			}
			if got.palette.timedOut {
				t.Error("a response for the current query must clear the timed-out state")
			}
		})
	}
}

// TestApplyPaneSearch_CappedLabelIsPerHit: the payload-level Truncated is a
// "some pane capped" summary, so labelling every hit from it would mark honest
// counts as capped.
func TestApplyPaneSearch_CappedLabelIsPerHit(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.query = "err"

	got := m.applyPaneSearch(ipc.PaneSearchRespPayload{
		Query:     "err",
		Truncated: true,
		Hits: []ipc.PaneSearchHit{
			{PaneID: "p1", Matches: 1000, Truncated: true},
			{PaneID: "p2", Matches: 2},
		},
	})
	if len(got.palette.contentHits) != 2 {
		t.Fatalf("contentHits = %d, want 2", len(got.palette.contentHits))
	}
	if got.palette.contentHits[0].detail != "1000× capped" {
		t.Errorf("capped hit detail = %q, want %q", got.palette.contentHits[0].detail, "1000× capped")
	}
	if got.palette.contentHits[1].detail != "2×" {
		t.Errorf("uncapped hit detail = %q, want %q (a sibling pane's cap must not mislabel it)", got.palette.contentHits[1].detail, "2×")
	}
}

// TestPaletteDisplay_EmptyQueryCommandsOnly: with no query the palette browses
// commands and runs no content search — no "Found in panes" section.
func TestPaletteDisplay_EmptyQueryCommandsOnly(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("", m.palette.commands)
	m.palette.query = ""

	display := m.paletteDisplay()
	if len(display) != len(m.palette.filtered) {
		t.Errorf("empty query: display = %d rows, want the %d filtered commands only", len(display), len(m.palette.filtered))
	}
	for _, c := range display {
		if c.label == "Found in panes" {
			t.Error("empty query must not show the content section")
		}
	}
}

// TestPaletteDisplay_CombinesCommandsAndHits: a non-empty query shows the
// filtered commands, then a "Found in panes" header, then the pane hits — one
// list the cursor walks (the header is skipped).
func TestPaletteDisplay_CombinesCommandsAndHits(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.commands = m.buildPaletteCommands()
	m.palette.query = "split"
	m.palette.filtered = filterPalette("split", m.palette.commands)
	m.palette.contentHits = []paletteCommand{
		{action: palActGoToPane, arg: "p2", enabled: true, label: "1.2 · terminal", detail: "2×", excerpt: "split found here"},
	}

	display := m.paletteDisplay()

	var headerIdx, hitIdx = -1, -1
	for i, c := range display {
		if c.header && c.label == "Found in panes" {
			headerIdx = i
		}
		if c.action == palActGoToPane && c.arg == "p2" {
			hitIdx = i
		}
	}
	if headerIdx < 0 || hitIdx < 0 || hitIdx < headerIdx {
		t.Fatalf("want a 'Found in panes' header followed by the p2 hit, got header=%d hit=%d", headerIdx, hitIdx)
	}
	// There must be at least one filtered command before the header (split H/V).
	if headerIdx == 0 {
		t.Error("filtered commands should precede the content section")
	}
	// The cursor must be able to reach the hit, skipping the header.
	if got := nextSelectable(display, headerIdx-1, +1); got != hitIdx && !display[got].selectable() {
		t.Errorf("nextSelectable from the last command should land on a selectable row near the hit, got %d", got)
	}
	if !display[hitIdx].selectable() || display[headerIdx].selectable() {
		t.Error("hit rows are selectable; the section header is not")
	}
}

// TestPaletteDisplay_States exercises the three content-section status lines.
func TestPaletteDisplay_States(t *testing.T) {
	find := func(display []paletteCommand, sub string) bool {
		for _, c := range display {
			if c.info && strings.Contains(c.label, sub) {
				return true
			}
		}
		return false
	}
	m := newSplitDragTestModel(t)
	m.palette.commands = m.buildPaletteCommands()
	m.palette.query = "x"
	m.palette.filtered = filterPalette("x", m.palette.commands)

	m.palette.searching = true
	if !find(m.paletteDisplay(), "Searching") {
		t.Error("in-flight search should show a Searching status row")
	}
	m.palette.searching = false
	m.palette.timedOut = true
	if !find(m.paletteDisplay(), "timed out") {
		t.Error("a timed-out search should show its diagnosable status row")
	}
	m.palette.timedOut = false
	if !find(m.paletteDisplay(), "No matches") {
		t.Error("a completed search with no hits should show a no-matches status row")
	}
}

// TestRenderCommandPalette_ContentWidthSafe: a hostile hit (wide-glyph label +
// very long excerpt) at a narrow width must never render a line wider than the
// box interior, or the dialog border wraps.
func TestRenderCommandPalette_ContentWidthSafe(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.width = 30
	m.palette.commands = m.buildPaletteCommands()
	m.palette.query = "x"
	m.palette.filtered = filterPalette("x", m.palette.commands)
	m.palette.contentHits = []paletteCommand{{
		action:  palActGoToPane,
		arg:     "p1",
		enabled: true,
		label:   "🚀🚀🚀🚀 very long pane name here",
		detail:  "999×",
		excerpt: strings.Repeat("long excerpt ", 20),
	}}
	m.palette.truncated = true
	inner := m.paletteInnerWidth()
	for i, line := range strings.Split(renderCommandPalette(*m), "\n") {
		if w := lipgloss.Width(line); w > inner {
			t.Errorf("line %d width %d exceeds inner %d: %q", i, w, inner, line)
		}
	}
}

// TestPaletteContent_EnterNavigates drives Enter through handleCommandPaletteKey
// so a regression in the content-hit Enter path (close-before-navigate, or a
// hit not dispatching as palActGoToPane) is caught. The query matches no command
// so the cursor lands directly on the hit.
func TestPaletteContent_EnterNavigates(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	m.palette.commands = m.buildPaletteCommands()
	m.palette.query = "zznomatch"
	m.palette.filtered = filterPalette("zznomatch", m.palette.commands)
	m.palette.contentHits = []paletteCommand{
		{action: palActGoToPane, arg: "p2", enabled: true, label: "1.2 · terminal", excerpt: "hit"},
	}
	m.palette.cursor = firstSelectable(m.paletteDisplay())

	updated, _ := m.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(Model)
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone (palette should close on Enter)", got.dialog)
	}
	if tab := got.activeTabModel(); tab == nil || tab.ActivePane != "p2" {
		t.Error("Enter on a content hit should activate the pane under the cursor")
	}
}

// TestAfterPaletteQueryChange_UnifiedSearch: a non-empty query filters commands
// AND kicks off a content search (searching set, stale hits dropped, a debounce
// cmd returned); an empty query browses commands with no search.
func TestAfterPaletteQueryChange_UnifiedSearch(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.commands = m.buildPaletteCommands()

	// Non-empty query.
	m.palette.query = "err"
	m.palette.contentHits = []paletteCommand{{action: palActGoToPane, arg: "p1", enabled: true, label: "stale"}}
	updated, cmd := m.afterPaletteQueryChange()
	got := updated.(Model)
	if len(got.palette.contentHits) != 0 {
		t.Errorf("stale hits should be dropped on query change, got %+v", got.palette.contentHits)
	}
	if !got.palette.searching {
		t.Error("a non-empty query should set searching so 'Searching…' is reachable immediately")
	}
	if cmd == nil {
		t.Error("a non-empty query should schedule a content-search debounce")
	}
	if len(got.palette.filtered) == 0 {
		t.Error("commands should still be filtered alongside the content search")
	}

	// Empty query.
	got.palette.query = ""
	updated, cmd = got.afterPaletteQueryChange()
	got = updated.(Model)
	if got.palette.searching || len(got.palette.contentHits) != 0 {
		t.Error("an empty query must clear content state")
	}
	if cmd != nil {
		t.Error("an empty query must not schedule a content search")
	}
}

// TestPaletteSearchDebounce_Update: the debounce fires a request only for the
// current non-empty query and never re-arms the IPC listen loop.
func TestPaletteSearchDebounce_Update(t *testing.T) {
	base := func() Model {
		m := newSplitDragTestModel(t)
		m.dialog = dialogCommandPalette
		m.palette.query = "refused"
		return *m
	}
	updated, cmd := base().Update(paletteSearchDebounceMsg{query: "refused"})
	got := updated.(Model)
	if !got.palette.searching {
		t.Error("the debounce for the current query should mark searching")
	}
	if cmd == nil {
		t.Error("the debounce should issue the request + timeout batch")
	}

	// Query moved on since the tick was scheduled → no request.
	moved := base()
	moved.palette.query = "refused now"
	if _, cmd := moved.Update(paletteSearchDebounceMsg{query: "refused"}); cmd != nil {
		t.Error("a debounce for a superseded query must not fire a request")
	}
}

// TestPaletteSearchTimeout_Update drives the Update branch: the timeout applies
// only to the still-outstanding current query, and never re-arms the IPC listen
// loop (it consumed no daemon message).
func TestPaletteSearchTimeout_Update(t *testing.T) {
	base := func() Model {
		m := newSplitDragTestModel(t)
		m.dialog = dialogCommandPalette
		m.palette.query = "refused"
		m.palette.searching = true
		return *m
	}

	updated, cmd := base().Update(paletteSearchTimeoutMsg{query: "refused"})
	got := updated.(Model)
	if !got.palette.timedOut || got.palette.searching {
		t.Errorf("current outstanding query should time out: timedOut=%v searching=%v", got.palette.timedOut, got.palette.searching)
	}
	if cmd != nil {
		t.Error("the timeout is a LOCAL timer — returning a cmd here would double-arm listenForMessages")
	}

	// Query moved on since the tick was scheduled.
	moved := base()
	moved.palette.query = "refused now"
	updated, _ = moved.Update(paletteSearchTimeoutMsg{query: "refused"})
	if updated.(Model).palette.timedOut {
		t.Error("a tick for a superseded query must not time out the current search")
	}

	// The response already landed (searching cleared).
	answered := base()
	answered.palette.searching = false
	updated, _ = answered.Update(paletteSearchTimeoutMsg{query: "refused"})
	if updated.(Model).palette.timedOut {
		t.Error("no request outstanding — the tick must be a no-op")
	}
}
