package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// overlayRight composites overlay onto the right edge of base. base is a
// block of totalW-wide lines; the rightmost overlayW columns of every base
// line are replaced by the matching overlay line (blank-filled when the
// overlay block is shorter). Used to draw the notification sidebar on TOP
// of the tab content instead of reserving layout width — panes keep full
// width, so toggling the sidebar never resizes a PTY (the root amplifier
// of the claude-code repaint artifacts; see
// docs/superpowers/specs/2026-07-04-resize-artifacts-design.md).
//
// ANSI-aware: the base line is truncated with ansi.Truncate (a wide glyph
// that would straddle the cut is dropped and padded with a space) and
// closed with an SGR reset so base styling never bleeds into the overlay.
func overlayRight(base, overlay string, totalW, overlayW int) string {
	if overlayW <= 0 || overlayW >= totalW {
		return base
	}
	keepW := totalW - overlayW
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	blank := strings.Repeat(" ", overlayW)

	out := make([]string, len(baseLines))
	for i, bl := range baseLines {
		left := ansi.Truncate(bl, keepW, "")
		// Reset BEFORE padding: if Truncate left an SGR open at the cut
		// (e.g. a trailing background color), padding the gap without a
		// reset first would bleed that background into the columns between
		// the pane content and the overlay.
		pad := ""
		if n := keepW - ansi.StringWidth(left); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		right := blank
		if i < len(overlayLines) {
			right = overlayLines[i]
		}
		out[i] = left + "\x1b[0m" + pad + right
	}
	return strings.Join(out, "\n")
}

// overlayAt composites box onto base with box's top-left cell at column x,
// row y (both 0-based within base). base is a block of totalW-wide lines.
// Same ANSI discipline as overlayRight: segments are cut with ansi.Truncate /
// ansi.Cut and closed with an SGR reset on BOTH sides of the box so
// base styling never bleeds into it and the box's styling never bleeds into
// the preserved right tail. Used by the pane context menu — a positional
// popup that, like the sidebar, must not reserve layout width (a layout
// change would resize PTYs; see the 2026-07-04 resize-artifacts design).
//
// The caller (ctxMenuPos) is responsible for clamping the box on screen;
// out-of-range inputs return base unchanged as a backstop, and box rows
// below base's last line are dropped.
func overlayAt(base, box string, x, y, totalW int) string {
	if x < 0 || y < 0 || totalW <= 0 {
		return base
	}
	boxLines := strings.Split(box, "\n")
	boxW := 0
	for _, bl := range boxLines {
		if w := ansi.StringWidth(bl); w > boxW {
			boxW = w
		}
	}
	if boxW == 0 || x+boxW > totalW {
		return base
	}
	baseLines := strings.Split(base, "\n")
	for i, bl := range boxLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		left := ansi.Truncate(baseLines[row], x, "")
		pad := ""
		if n := x - ansi.StringWidth(left); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		// Cut the right part starting from x + boxW to the end.
		// If a wide glyph straddles the cut boundary, ansi.Cut may include it,
		// so we cap the result to the remaining width.
		baselineWidth := ansi.StringWidth(baseLines[row])
		cutStartCell := x + ansi.StringWidth(bl)
		right := ansi.Cut(baseLines[row], cutStartCell, baselineWidth)
		rightMaxWidth := baselineWidth - cutStartCell
		if rightMaxWidth < 0 {
			rightMaxWidth = 0
		}
		if rightMaxWidth > 0 && ansi.StringWidth(right) > rightMaxWidth {
			right = ansi.Truncate(right, rightMaxWidth, "")
		}
		// Pad the right tail to match the target width: when Truncate drops a
		// wide glyph, right may undershoot rightMaxWidth.
		if n := rightMaxWidth - ansi.StringWidth(right); n > 0 {
			right += strings.Repeat(" ", n)
		}
		baseLines[row] = left + "\x1b[0m" + pad + bl + "\x1b[0m" + right
	}
	return strings.Join(baseLines, "\n")
}
