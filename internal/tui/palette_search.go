package tui

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// paletteMode selects how the palette interprets its query.
type paletteMode int

const (
	paletteModeCommand paletteMode = iota // fuzzy command list (default)
	paletteModeContent                    // literal search across pane scrollback ("/")
)

// paletteHit is the TUI-side, label-resolved form of an ipc.PaneSearchHit. The
// daemon returns only paneID+count+excerpt; label/detail are resolved locally.
type paletteHit struct {
	paneID  string
	label   string // "2.1 · claude-code · myproj"
	detail  string // "3×" or "3× capped"
	excerpt string
}

// parsePaletteQuery classifies a raw query. A leading "/" switches to content
// mode with the remainder as the search term (leading slash consumed, nothing
// else trimmed — the term may intentionally start with a space). Anything else
// is command mode with the query verbatim.
func parsePaletteQuery(query string) (paletteMode, string) {
	if strings.HasPrefix(query, "/") {
		return paletteModeContent, query[1:]
	}
	return paletteModeCommand, query
}

// paletteSearchDebounceMsg fires after the keystroke debounce; term is the
// search term captured when the tick was scheduled.
type paletteSearchDebounceMsg struct{ term string }

// paneSearchRespMsg carries a daemon content-search response into Update.
type paneSearchRespMsg struct{ Resp ipc.PaneSearchRespPayload }

// paneNavLabel resolves a pane id to its navigation label and short CWD, or
// ok=false if the pane is gone. Iterates tabs/leaves so it can compute the
// same 1-based i.j indices formatPaneNav uses.
func (m *Model) paneNavLabel(paneID string) (label, cwd string, ok bool) {
	home, _ := os.UserHomeDir()
	for i, tab := range m.tabs {
		if tab == nil {
			continue
		}
		for j, p := range tab.Leaves() {
			if p != nil && p.ID == paneID {
				return formatPaneNav(i, j, p), shortCWD(p.CWD, home), true
			}
		}
	}
	return "", "", false
}

// requestPaneSearch fires MsgPaneSearchReq for term (fire-and-forget); the
// response arrives via listenForMessages → paneSearchRespMsg. Mirrors
// requestHistory.
func (m Model) requestPaneSearch(term string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneSearchReq, ipc.PaneSearchReqPayload{Query: term})
		if err != nil {
			log.Printf("requestPaneSearch: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("search-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestPaneSearch: send: %v", err)
		}
		return nil
	}
}

// paletteSearchDebounce schedules a debounce tick; when it fires the Update
// handler checks the term is still current before issuing the request. 150ms
// coalesces keystrokes so we do not search on every character.
func paletteSearchDebounce(term string) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return paletteSearchDebounceMsg{term: term}
	})
}

// applyPaneSearch stores a fresh result set, resolving each hit's label locally.
// Stale responses (echoed Query != current term) are ignored — the same guard
// applyHistoryList uses against the active dialog.
func (m Model) applyPaneSearch(resp ipc.PaneSearchRespPayload) Model {
	if m.palette.mode != paletteModeContent || resp.Query != m.palette.term {
		return m
	}
	hits := make([]paletteHit, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		label, _, ok := m.paneNavLabel(h.PaneID)
		if !ok {
			continue // pane vanished since the daemon scanned
		}
		detail := fmt.Sprintf("%d×", h.Matches)
		if resp.Truncated {
			detail += " capped"
		}
		hits = append(hits, paletteHit{paneID: h.PaneID, label: label, detail: detail, excerpt: h.Excerpt})
	}
	m.palette.hits = hits
	m.palette.searching = false
	m.palette.truncated = resp.Truncated
	if m.palette.cursor >= len(hits) {
		m.palette.cursor = len(hits) - 1
	}
	if m.palette.cursor < 0 {
		m.palette.cursor = 0
	}
	return m
}
