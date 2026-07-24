package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestParsePaletteQuery(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantMode paletteMode
		wantTerm string
	}{
		{"", paletteModeCommand, ""},
		{"close", paletteModeCommand, "close"},
		{"/", paletteModeContent, ""},
		{"/refused", paletteModeContent, "refused"},
		{"/ two words", paletteModeContent, " two words"},
	} {
		mode, term := parsePaletteQuery(tc.in)
		if mode != tc.wantMode || term != tc.wantTerm {
			t.Errorf("parse(%q) = (%v,%q), want (%v,%q)", tc.in, mode, term, tc.wantMode, tc.wantTerm)
		}
	}
}

func TestApplyPaneSearch_ResolvesLabels(t *testing.T) {
	m := newSplitDragTestModel(t) // panes p1, p2 on tab 0
	m.palette.query = "/p"
	m.palette.mode = paletteModeContent
	m.palette.term = "p"
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
	if len(m2.palette.hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(m2.palette.hits))
	}
	if m2.palette.hits[0].paneID != "p1" || m2.palette.hits[0].detail != "3×" {
		t.Errorf("hit0 = %+v", m2.palette.hits[0])
	}
	if !strings.Contains(m2.palette.hits[0].label, "p1") && !strings.Contains(m2.palette.hits[0].label, "1.1") {
		t.Errorf("label should identify the pane: %q", m2.palette.hits[0].label)
	}
}

func TestApplyPaneSearch_DropsStale(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.mode = paletteModeContent
	m.palette.term = "current"
	m.palette.hits = []paletteHit{{paneID: "p1", label: "old"}}

	stale := ipc.PaneSearchRespPayload{Query: "old-term", Hits: []ipc.PaneSearchHit{{PaneID: "p2", Matches: 9}}}
	m2 := m.applyPaneSearch(stale)
	if len(m2.palette.hits) != 1 || m2.palette.hits[0].paneID != "p1" {
		t.Errorf("stale response must not replace hits, got %+v", m2.palette.hits)
	}
}

// TestApplyPaneSearch_AcceptsDaemonEcho is the TUI half of the staleness seam
// pinned daemon-side by TestPaneSearchResponse_EchoesQueryVerbatim: whatever
// term the TUI sends, the daemon echoes it verbatim, and the TUI must ACCEPT
// that response. Terms with leading/trailing whitespace are the ones that broke
// when the daemon trimmed its echo — the response was dropped forever and the
// palette hung on "Searching…".
func TestApplyPaneSearch_AcceptsDaemonEcho(t *testing.T) {
	for _, term := range []string{"refused", " refused", "refused ", "two words"} {
		t.Run(fmt.Sprintf("%q", term), func(t *testing.T) {
			m := newSplitDragTestModel(t)
			// Drive the real query path so term is derived exactly as it is at
			// runtime (parsePaletteQuery keeps the term untrimmed).
			m.palette.query = "/" + term
			m.palette.mode, m.palette.term = parsePaletteQuery(m.palette.query)
			m.palette.searching = true
			m.palette.timedOut = true // a late response must clear this too

			// What the daemon sends back: the request query, verbatim.
			resp := ipc.PaneSearchRespPayload{
				Query: m.palette.term,
				Hits:  []ipc.PaneSearchHit{{PaneID: "p1", Matches: 2, Excerpt: "connection refused"}},
			}
			got := m.applyPaneSearch(resp)
			if len(got.palette.hits) != 1 {
				t.Fatalf("hits = %d, want 1 — the daemon's own echo must never look stale", len(got.palette.hits))
			}
			if got.palette.searching {
				t.Error("searching should clear once the response is applied")
			}
			if got.palette.timedOut {
				t.Error("a response for the current term must clear the timed-out state")
			}
		})
	}
}

// TestApplyPaneSearch_CappedLabelIsPerHit: the payload-level Truncated is a
// "some pane capped" summary, so labelling every hit from it would mark honest
// counts as capped.
func TestApplyPaneSearch_CappedLabelIsPerHit(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.mode = paletteModeContent
	m.palette.term = "err"

	got := m.applyPaneSearch(ipc.PaneSearchRespPayload{
		Query:     "err",
		Truncated: true,
		Hits: []ipc.PaneSearchHit{
			{PaneID: "p1", Matches: 1000, Truncated: true},
			{PaneID: "p2", Matches: 2},
		},
	})
	if len(got.palette.hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(got.palette.hits))
	}
	if got.palette.hits[0].detail != "1000× capped" {
		t.Errorf("capped hit detail = %q, want %q", got.palette.hits[0].detail, "1000× capped")
	}
	if got.palette.hits[1].detail != "2×" {
		t.Errorf("uncapped hit detail = %q, want %q (a sibling pane's cap must not mislabel it)", got.palette.hits[1].detail, "2×")
	}
}

// TestRenderPaletteContent_TimedOut: a request that never answers must render a
// diagnosable message, not an indefinite "Searching…".
func TestRenderPaletteContent_TimedOut(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.mode = paletteModeContent
	m.palette.term = "refused"
	m.palette.timedOut = true
	out := renderPaletteContent(*m, m.paletteInnerWidth())
	if !strings.Contains(out, "timed out") {
		t.Errorf("timed-out message missing:\n%s", out)
	}
	if strings.Contains(out, "Searching") {
		t.Errorf("timed-out state must replace the searching hint:\n%s", out)
	}
}

// TestPaletteSearchTimeout_Update drives the Update branch: the timeout applies
// only to the still-outstanding current term, and never re-arms the IPC listen
// loop (it consumed no daemon message).
func TestPaletteSearchTimeout_Update(t *testing.T) {
	base := func() Model {
		m := newSplitDragTestModel(t)
		m.dialog = dialogCommandPalette
		m.palette.mode = paletteModeContent
		m.palette.term = "refused"
		m.palette.searching = true
		return *m
	}

	updated, cmd := base().Update(paletteSearchTimeoutMsg{term: "refused"})
	got := updated.(Model)
	if !got.palette.timedOut || got.palette.searching {
		t.Errorf("current outstanding term should time out: timedOut=%v searching=%v", got.palette.timedOut, got.palette.searching)
	}
	if cmd != nil {
		t.Error("the timeout is a LOCAL timer — returning a cmd here would double-arm listenForMessages")
	}

	// Term moved on since the tick was scheduled.
	moved := base()
	moved.palette.term = "refused now"
	updated, _ = moved.Update(paletteSearchTimeoutMsg{term: "refused"})
	if updated.(Model).palette.timedOut {
		t.Error("a tick for a superseded term must not time out the current search")
	}

	// The response already landed (searching cleared).
	answered := base()
	answered.palette.searching = false
	updated, _ = answered.Update(paletteSearchTimeoutMsg{term: "refused"})
	if updated.(Model).palette.timedOut {
		t.Error("no request outstanding — the tick must be a no-op")
	}
}

func TestPaletteHitWindow(t *testing.T) {
	if s, e := paletteHitWindow(0, 3); s != 0 || e != 3 {
		t.Errorf("small: got [%d,%d), want [0,3)", s, e)
	}
	s, e := paletteHitWindow(paletteVisibleHits+2, 40)
	if cursor := paletteVisibleHits + 2; cursor < s || cursor >= e {
		t.Errorf("cursor %d not in window [%d,%d)", cursor, s, e)
	}
}

func TestRenderPaletteContent_States(t *testing.T) {
	m := newSplitDragTestModel(t)
	// Empty term.
	m.palette.mode = paletteModeContent
	m.palette.term = ""
	if out := renderPaletteContent(*m, m.paletteInnerWidth()); !strings.Contains(out, "Type to search") {
		t.Errorf("empty term hint missing:\n%s", out)
	}
	// Searching.
	m.palette.term = "x"
	m.palette.searching = true
	if out := renderPaletteContent(*m, m.paletteInnerWidth()); !strings.Contains(out, "Searching") {
		t.Errorf("searching hint missing:\n%s", out)
	}
	// No hits.
	m.palette.searching = false
	m.palette.hits = nil
	if out := renderPaletteContent(*m, m.paletteInnerWidth()); !strings.Contains(out, "No matches") {
		t.Errorf("no-match hint missing:\n%s", out)
	}
}

func TestRenderPaletteContent_WidthSafe(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.width = 30
	m.palette.mode = paletteModeContent
	m.palette.term = "x"
	m.palette.hits = []paletteHit{{
		paneID:  "p1",
		label:   "🚀🚀🚀🚀 very long pane name here",
		detail:  "999×",
		excerpt: strings.Repeat("long excerpt ", 20),
	}}
	inner := m.paletteInnerWidth()
	for i, line := range strings.Split(renderPaletteContent(*m, inner), "\n") {
		if w := lipgloss.Width(line); w > inner {
			t.Errorf("line %d width %d exceeds inner %d: %q", i, w, inner, line)
		}
	}
}

func TestPaletteContent_EnterNavigatesDirect(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	m.palette.mode = paletteModeContent
	m.palette.hits = []paletteHit{{paneID: "p2", label: "1.2 · terminal"}}
	m.palette.cursor = 0

	updated, _ := m.goToPane("p2")
	m2 := updated.(Model)
	if tab := m2.activeTabModel(); tab == nil || tab.ActivePane != "p2" {
		t.Errorf("goToPane should activate p2")
	}
}

// TestPaletteContent_EnterFullPath drives Enter through handleCommandPaletteKey
// (not a direct goToPane call) so a regression that drops or reorders the
// closeCommandPalette() call in the content-mode Enter branch is caught.
func TestPaletteContent_EnterFullPath(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	m.palette.mode = paletteModeContent
	m.palette.hits = []paletteHit{{paneID: "p2", label: "1.2 · terminal"}}
	m.palette.cursor = 0

	updated, _ := m.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(Model)
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone (palette should close on Enter)", got.dialog)
	}
	if tab := got.activeTabModel(); tab == nil || tab.ActivePane != "p2" {
		t.Error("Enter in content mode should activate the pane under the cursor")
	}
}

// TestAfterPaletteQueryChange_ClearsStaleHitsOnTermChange drives the actual
// mutation path (afterPaletteQueryChange), not renderPaletteContent's state
// switch directly — a query change that leaves old hits in place would
// otherwise render the previous term's results under the new query header.
func TestAfterPaletteQueryChange_ClearsStaleHitsOnTermChange(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.mode = paletteModeContent
	m.palette.query = "/e"
	m.palette.term = "e"
	m.palette.hits = []paletteHit{{paneID: "p1", label: "stale hit for 'e'"}}
	m.palette.searching = false

	// Extend the term: "/e" -> "/er". The previous term's hits must not
	// survive into the new term's (pre-response) state.
	m.palette.query = "/er"
	updated, _ := m.afterPaletteQueryChange()
	got := updated.(Model)

	if len(got.palette.hits) != 0 {
		t.Errorf("hits after term change = %+v, want cleared", got.palette.hits)
	}
	if !got.palette.searching {
		t.Error("searching should be true immediately after a term change, so \"Searching…\" is reachable on refinement")
	}
	if got.palette.term != "er" {
		t.Fatalf("term = %q, want %q", got.palette.term, "er")
	}

	// The rendered view must not show the stale "e" hit under the "er" header.
	out := renderPaletteContent(got, got.paletteInnerWidth())
	if strings.Contains(out, "stale hit for 'e'") {
		t.Errorf("stale hit rendered under changed query:\n%s", out)
	}
	if !strings.Contains(out, "Searching") {
		t.Errorf("expected the Searching state to be reachable on term change:\n%s", out)
	}
}
