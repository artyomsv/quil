package tui

import (
	"strings"
	"testing"

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
