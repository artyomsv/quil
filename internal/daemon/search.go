package daemon

import (
	"sort"
	"strings"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/charmbracelet/x/ansi"
)

const (
	// maxPaneMatches bounds per-pane counting so a huge buffer full of the term
	// cannot make one search walk unboundedly; hitting it sets Truncated.
	maxPaneMatches = 1000
	// maxExcerptCells caps the preview line width (display cells, ASCII-safe here
	// since we only rune-count the collapsed line's length as a coarse bound).
	maxExcerptCells = 160
)

// scanPaneMatches strips ANSI from raw pane output, counts lines that contain
// lowerTerm (case-insensitive; caller pre-lowers the term), and returns the LAST
// matching line as a whitespace-collapsed, length-capped excerpt. matches stops
// accumulating at maxPaneMatches, in which case truncated is true. An empty
// lowerTerm yields (0, "", false).
func scanPaneMatches(raw []byte, lowerTerm string) (matches int, excerpt string, truncated bool) {
	if lowerTerm == "" || len(raw) == 0 {
		return 0, "", false
	}
	stripped := ansi.Strip(string(raw))
	var lastLine string
	for _, line := range strings.Split(stripped, "\n") {
		if strings.Contains(strings.ToLower(line), lowerTerm) {
			lastLine = line
			if matches < maxPaneMatches {
				matches++
			} else {
				truncated = true
			}
		}
	}
	if matches == 0 {
		return 0, "", false
	}
	return matches, collapseExcerpt(lastLine), truncated
}

// collapseExcerpt trims a preview line, collapses internal whitespace runs to
// single spaces, and caps its rune length with an ellipsis.
func collapseExcerpt(line string) string {
	collapsed := strings.Join(strings.Fields(line), " ")
	runes := []rune(collapsed)
	if len(runes) > maxExcerptCells {
		return string(runes[:maxExcerptCells-1]) + "…"
	}
	return collapsed
}

// searchPanes scans every pane's loaded OutputBuf across all tabs for term and
// returns hits sorted by match count (desc), then pane id. It never spawns a
// dormant pane — only already-buffered content is searched. truncated is true
// if any pane hit maxPaneMatches.
func (d *Daemon) searchPanes(term string) (hits []ipc.PaneSearchHit, truncated bool) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, false
	}
	lower := strings.ToLower(term)
	_, tabs, panesByTab := d.session.SnapshotState()
	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			if pane.OutputBuf == nil {
				continue
			}
			n, excerpt, trunc := scanPaneMatches(pane.OutputBuf.Bytes(), lower)
			if n == 0 {
				continue
			}
			if trunc {
				truncated = true
			}
			hits = append(hits, ipc.PaneSearchHit{PaneID: pane.ID, Matches: n, Excerpt: excerpt})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Matches != hits[j].Matches {
			return hits[i].Matches > hits[j].Matches
		}
		return hits[i].PaneID < hits[j].PaneID
	})
	return hits, truncated
}
