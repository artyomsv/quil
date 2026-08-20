package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Frame assembly without re-measuring what the renderers already sized.
//
// Measured 2026-08-20 on a 41-tab / 200x50 frame with realistic pane content:
// ansi.stringWidth is 54.7% of a frame whose pane caches are all warm, and 97%
// of that arrives through lipgloss's own join internals —
//
//	getLines        44.3%
//	JoinVertical    35.7%
//	JoinHorizontal  17.1%
//	lipgloss.Width   2.9%   <- our own call sites
//
// which is why memoising our `lipgloss.Width` calls was measured and REJECTED:
// it could reach ~1.6% of a frame. The joins are where the time is.
//
// What they spend it on is the part worth stating: every block View() assembles
// is ALREADY rectangular, because each is produced by a lipgloss style with an
// explicit width. Measured on the same frame — tab bar 1 line of 178, tab
// content 48 lines all 178, sidebar 49 lines all 22. So the joins walk ~50 lines
// of grapheme clusters per frame to discover they need to pad nothing.
//
// These helpers take the width the caller already knows and concatenate. They
// are NOT a general lipgloss replacement: each checks its assumption and falls
// back to lipgloss when the check fails.
//
// The check is a SAMPLE, not a proof — see blockIsWidth. A block that is the
// declared width on its first and last lines but ragged in the INTERIOR takes
// the fast path and disagrees with lipgloss: vertically the short line goes
// unpadded, horizontally the right column shifts left on that row. So the
// guarantee is not "never wrong", it is "wrong only for a block nothing in the
// frame produces" — and that premise is what
// TestFrameBlocks_AreRectangularAcrossGeometries exists to defend. If a future
// renderer emits a ragged interior, that test is what should fail.
//
// `lipgloss` remains the width AUTHORITY, per the project invariant — these
// helpers never compute a width by another route. They only skip measurement
// they can prove is unnecessary, and defer to lipgloss when they cannot.

// blockIsWidth reports whether every line of s is exactly w display cells.
//
// Checks the FIRST and LAST line rather than all of them. That is the whole
// point — an all-lines check costs exactly what the join costs — and it is
// sound for the blocks this is used on, which are rectangular by construction:
// a lipgloss style with .Width(n) pads every line it emits.
//
// The interior is covered by TestFrameJoins_MatchLipglossOnRealFrames, which
// runs REAL frames through both paths at three tab counts and three geometries,
// and by TestFrameBlocks_AreRectangular, which asserts the premise directly.
// (TestJoinVerticalWidth_MatchesLipgloss covers the shapes, using synthetic
// table cases rather than frames.)
//
// A block that fails this returns false and the caller falls back to lipgloss.
// For a block that is UNIFORM, or ragged at either sampled line, the failure
// mode is "slower". For one ragged only in its interior it is "wrong" — see the
// package comment above, and TestBlockIsWidth_OnlyChecksFirstAndLastLine, which
// asserts that divergence rather than describing it.
func blockIsWidth(s string, w int) bool {
	if s == "" {
		return false
	}
	first := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first = s[:i]
	}
	if lipgloss.Width(first) != w {
		return false
	}
	last := s
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		last = s[i+1:]
	}
	return lipgloss.Width(last) == w
}

// joinVerticalWidth is lipgloss.JoinVertical(lipgloss.Left, blocks...) for the
// case the frame assembly actually produces: every block already exactly `width`
// cells wide, so no block needs padding and the join is a concatenation.
//
// Falls back to lipgloss whenever that does not hold.
func joinVerticalWidth(width int, blocks ...string) string {
	if width <= 0 || len(blocks) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, blocks...)
	}
	n := 0
	for _, b := range blocks {
		if !blockIsWidth(b, width) {
			return lipgloss.JoinVertical(lipgloss.Left, blocks...)
		}
		n += len(b) + 1
	}
	var sb strings.Builder
	sb.Grow(n)
	for i, b := range blocks {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b)
	}
	return sb.String()
}

// joinHorizontalWidth is lipgloss.JoinHorizontal(lipgloss.Top, left, right) for
// two blocks that are already rectangular AND already the same height, which is
// what the sidebar and the pane area are: both are built to the same content
// height, so lipgloss's height-padding pass has nothing to do either.
//
// Falls back to lipgloss on any mismatch, including a line-count difference —
// the case where lipgloss would pad the shorter block and a concatenation would
// silently drop rows.
func joinHorizontalWidth(leftW, rightW int, left, right string) string {
	if leftW <= 0 || rightW <= 0 ||
		!blockIsWidth(left, leftW) || !blockIsWidth(right, rightW) {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}
	lLines := strings.Split(left, "\n")
	rLines := strings.Split(right, "\n")
	if len(lLines) != len(rLines) {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}
	var sb strings.Builder
	sb.Grow(len(left) + len(right) + len(lLines))
	for i := range lLines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(lLines[i])
		sb.WriteString(rLines[i])
	}
	return sb.String()
}
