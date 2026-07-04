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
		if pad := keepW - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := blank
		if i < len(overlayLines) {
			right = overlayLines[i]
		}
		out[i] = left + "\x1b[0m" + right
	}
	return strings.Join(out, "\n")
}
