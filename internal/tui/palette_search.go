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

const paletteVisibleHits = 6 // pane hits shown before the list scrolls (2 lines each)

// paletteHitWindow returns the [start,end) slice of hits to render, sized to
// paletteVisibleHits and shifted to keep cursor visible.
func paletteHitWindow(cursor, n int) (int, int) {
	if n <= paletteVisibleHits {
		return 0, n
	}
	start := 0
	if cursor >= paletteVisibleHits {
		start = cursor - paletteVisibleHits + 1
	}
	if max := n - paletteVisibleHits; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	return start, start + paletteVisibleHits
}

// renderPaletteContent renders the content-search view: a "Search pane content"
// header + term, then one two-line entry per matching pane (a selectable label
// row and a dim excerpt row), or a state hint. Every line is clamped to inner.
func renderPaletteContent(m Model, inner int) string {
	var b strings.Builder
	subtle := func(s string) string { return dialogSubtle.Render(truncateToWidth(s, inner)) }

	// Header: "/ " prompt + term + caret.
	qAvail := inner - 3
	if qAvail < 1 {
		qAvail = 1
	}
	b.WriteString(dialogTitle.Render("/ "))
	b.WriteString(dialogEditStyle.Render(lastCellsToWidth(m.palette.term, qAvail) + "│"))
	b.WriteByte('\n')
	b.WriteString(subtle("Search pane content"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	const hint = "↑↓ nav · Enter go · Esc close"

	switch {
	case strings.TrimSpace(m.palette.term) == "":
		b.WriteString(subtle("  Type to search across all panes"))
		b.WriteString("\n\n")
		b.WriteString(subtle(hint))
		return b.String()
	case len(m.palette.hits) == 0 && m.palette.searching:
		b.WriteString(subtle("  Searching…"))
		b.WriteString("\n\n")
		b.WriteString(subtle(hint))
		return b.String()
	case len(m.palette.hits) == 0:
		b.WriteString(subtle("  No matches in any pane"))
		b.WriteString("\n\n")
		b.WriteString(subtle(hint))
		return b.String()
	}

	start, end := paletteHitWindow(m.palette.cursor, len(m.palette.hits))
	if start > 0 {
		b.WriteString(subtle(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		h := m.palette.hits[i]
		b.WriteString(renderPaletteHitRow(h, i == m.palette.cursor, inner))
		b.WriteByte('\n')
		b.WriteString(ctxMenuDisabledStyle.Render(truncateToWidth("    "+h.excerpt, inner)))
		b.WriteByte('\n')
	}
	if end < len(m.palette.hits) {
		b.WriteString(subtle(fmt.Sprintf("  ↓ %d more", len(m.palette.hits)-end)))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(subtle(hint))
	return b.String()
}

// renderPaletteHitRow renders a hit's label row through the SHARED row
// renderer (renderPaletteLine, palette.go) so command rows and hit rows cannot
// drift apart.
func renderPaletteHitRow(h paletteHit, cursor bool, inner int) string {
	return renderPaletteLine(h.label, h.detail, cursor, false, inner)
}
