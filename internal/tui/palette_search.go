package tui

import "strings"

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
