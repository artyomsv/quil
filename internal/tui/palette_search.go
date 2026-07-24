package tui

import (
	"fmt"
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// paletteSearchDebounceMsg fires after the keystroke debounce; query is the
// palette query captured when the tick was scheduled.
type paletteSearchDebounceMsg struct{ query string }

// paneSearchRespMsg carries a daemon content-search response into Update.
type paneSearchRespMsg struct{ Resp ipc.PaneSearchRespPayload }

// paletteSearchTimeoutMsg fires paletteSearchTimeout after a request was
// issued; query is the palette query captured when the tick was scheduled.
type paletteSearchTimeoutMsg struct{ query string }

// paneNavLabel resolves a pane id to its navigation label, or ok=false if the
// pane is gone. Iterates tabs/leaves so it can compute the same 1-based i.j
// indices formatPaneNav uses.
func (m *Model) paneNavLabel(paneID string) (label string, ok bool) {
	for i, tab := range m.tabs {
		if tab == nil {
			continue
		}
		for j, p := range tab.Leaves() {
			if p != nil && p.ID == paneID {
				return formatPaneNav(i, j, p), true
			}
		}
	}
	return "", false
}

// requestPaneSearch fires MsgPaneSearchReq for query (fire-and-forget); the
// response arrives via listenForMessages → paneSearchRespMsg. Mirrors
// requestHistory.
func (m Model) requestPaneSearch(query string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneSearchReq, ipc.PaneSearchReqPayload{Query: query})
		if err != nil {
			log.Printf("requestPaneSearch: marshal: %v", err)
			return nil
		}
		// The ID is decorative here: respondTo unicasts to this conn regardless
		// of it, and staleness is resolved by the echoed query — do not trust
		// it as a correlation mechanism.
		msg.ID = fmt.Sprintf("search-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestPaneSearch: send: %v", err)
		}
		return nil
	}
}

// paletteSearchDebounce schedules a debounce tick; when it fires the Update
// handler checks the query is still current before issuing the request. 150ms
// coalesces keystrokes so we do not search on every character.
func paletteSearchDebounce(query string) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return paletteSearchDebounceMsg{query: query}
	})
}

// paletteSearchTimeout schedules the failure fallback for an issued request.
// If it fires while the same query is still outstanding, the palette shows a
// diagnosable message instead of sitting on "Searching…" forever — which is
// what a wedged daemon, or a new TUI talking to a daemon that does not know
// MsgPaneSearchReq (handleMessage silently drops unknown types), would produce.
func paletteSearchTimeout(query string) tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return paletteSearchTimeoutMsg{query: query}
	})
}

// applyPaneSearch stores a fresh result set as palActGoToPane rows, resolving
// each hit's label locally. Stale responses (echoed Query != current query) are
// ignored — the same guard applyHistoryList uses against the active dialog; it
// also drops responses after the palette closed (closeCommandPalette zeroes the
// query). A response for the current query is accepted even after the timeout
// fired, and clears the timed-out state.
func (m Model) applyPaneSearch(resp ipc.PaneSearchRespPayload) Model {
	if resp.Query != m.palette.query {
		return m
	}
	hits := make([]paletteCommand, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		label, ok := m.paneNavLabel(h.PaneID)
		if !ok {
			continue // pane vanished since the daemon scanned
		}
		detail := fmt.Sprintf("%d×", h.Matches)
		if h.Truncated {
			// Per-HIT flag: the payload-level Truncated is a "some pane was
			// capped" summary and would mislabel every honest count here.
			detail += " capped"
		}
		hits = append(hits, paletteCommand{
			action:  palActGoToPane,
			arg:     h.PaneID,
			enabled: true,
			label:   label,
			detail:  detail,
			excerpt: h.Excerpt,
		})
	}
	m.palette.contentHits = hits
	m.palette.searching = false
	m.palette.timedOut = false
	m.palette.truncated = resp.Truncated
	m.palette.cursor = clampPaletteCursor(m.paletteDisplay(), m.palette.cursor)
	return m
}
