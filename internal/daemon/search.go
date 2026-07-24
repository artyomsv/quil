package daemon

import (
	"log"
	"sort"
	"strings"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/charmbracelet/x/ansi"
)

const (
	// maxPaneMatches bounds the per-pane match COUNT we report, not the walk:
	// the scan deliberately continues past the cap so the excerpt stays the
	// most-recent match. Hitting it sets Truncated. (The walk itself is already
	// bounded by the ring buffer's fixed capacity.)
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
	// Single-pass line walk rather than strings.Split: this runs per pane on
	// every debounced keystroke, and Split allocates a []string header per line
	// (~4k for a 256 KB buffer). The slicing below yields exactly Split's lines,
	// including the trailing empty one after a final "\n".
	//
	// The per-line ToLower is deliberate: lowercasing the whole buffer once and
	// tracking byte offsets would be cheaper, but ToLower can change a string's
	// byte length for some Unicode (e.g. U+0130), which would desynchronize the
	// offsets and corrupt the excerpt.
	for rest := stripped; ; {
		nl := strings.IndexByte(rest, '\n')
		line := rest
		if nl >= 0 {
			line = rest[:nl]
		}
		if strings.Contains(strings.ToLower(line), lowerTerm) {
			lastLine = line
			if matches < maxPaneMatches {
				matches++
			} else {
				truncated = true
			}
		}
		if nl < 0 {
			break
		}
		rest = rest[nl+1:]
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
			hits = append(hits, ipc.PaneSearchHit{PaneID: pane.ID, Matches: n, Excerpt: excerpt, Truncated: trunc})
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

// handlePaneSearchReq answers a content search: scan all panes, return hits
// (unicast to the requesting conn). Never spawns panes; muted panes are
// included (mute governs notifications, not search).
func (d *Daemon) handlePaneSearchReq(conn *ipc.Conn, msg *ipc.Message) {
	respondTo(conn, msg.ID, ipc.MsgPaneSearchResp, d.paneSearchResponse(msg))
}

// paneSearchResponse decodes a search request and builds its response payload.
// Split from handlePaneSearchReq so the decode → scan → echo seam is testable
// without an ipc.Conn.
//
// CONTRACT: Query echoes req.Query VERBATIM — never trimmed, never otherwise
// normalized. The TUI drops any response whose echoed query differs from its
// current (deliberately untrimmed) search term, so normalizing here would make
// every whitespace-bearing term look permanently stale and hang the palette on
// "Searching…". searchPanes trims internally for matching; that must not leak
// into the echo.
func (d *Daemon) paneSearchResponse(msg *ipc.Message) ipc.PaneSearchRespPayload {
	var req ipc.PaneSearchReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handlePaneSearchReq: decode: %v", err)
		return ipc.PaneSearchRespPayload{}
	}
	hits, truncated := d.searchPanes(req.Query)
	return ipc.PaneSearchRespPayload{
		Query:     req.Query,
		Hits:      hits,
		Truncated: truncated,
	}
}
