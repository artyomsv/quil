package tui

import (
	"strings"
	"testing"
)

// setContentWidth configures e.ViewWidth so that contentWForLayout()
// returns exactly cw. Keeps the soft-wrap tests from depending on the
// current GutterWidth() implementation detail.
func setContentWidth(t *testing.T, e *TextEditor, cw int) {
	t.Helper()
	e.ViewWidth = cw + e.GutterWidth() + 1
}

// TestVisualLayout_NoWrap_OneRowPerLogical verifies the 1:1 mapping when
// SoftWrap is off so TOML editor and log viewer semantics are untouched.
func TestVisualLayout_NoWrap_OneRowPerLogical(t *testing.T) {
	t.Parallel()
	e := NewTextEditor("alpha\nbeta\ngamma", "", 80, 10)
	e.SoftWrap = false

	layout := e.visualLayout(40)
	if len(layout) != 3 {
		t.Fatalf("want 3 visual rows, got %d", len(layout))
	}
	for i, vr := range layout {
		if vr.Logical != i {
			t.Errorf("row %d: Logical=%d, want %d", i, vr.Logical, i)
		}
		if vr.Start != 0 {
			t.Errorf("row %d: Start=%d, want 0", i, vr.Start)
		}
		wantEnd := runeLen(e.Lines[i])
		if vr.End != wantEnd {
			t.Errorf("row %d: End=%d, want %d", i, vr.End, wantEnd)
		}
	}
}

// TestVisualLayout_WrapAtContentWidth checks the character-wrap split.
func TestVisualLayout_WrapAtContentWidth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		line     string
		contentW int
		want     []visualRow
	}{
		{
			name:     "exact multiple",
			line:     strings.Repeat("a", 80),
			contentW: 40,
			want: []visualRow{
				{Logical: 0, Start: 0, End: 40},
				{Logical: 0, Start: 40, End: 80},
			},
		},
		{
			name:     "partial tail",
			line:     strings.Repeat("a", 100),
			contentW: 40,
			want: []visualRow{
				{Logical: 0, Start: 0, End: 40},
				{Logical: 0, Start: 40, End: 80},
				{Logical: 0, Start: 80, End: 100},
			},
		},
		{
			name:     "fits",
			line:     "hello world",
			contentW: 40,
			want:     []visualRow{{Logical: 0, Start: 0, End: 11}},
		},
		{
			name:     "empty line still produces one row",
			line:     "",
			contentW: 40,
			want:     []visualRow{{Logical: 0, Start: 0, End: 0}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := NewTextEditor(tt.line, "", 80, 10)
			e.SoftWrap = true
			got := e.visualLayout(tt.contentW)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d rows, want %d", len(got), len(tt.want))
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("row %d: got %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

// TestVisualLayout_MultipleLinesMixedLengths covers wrap interleaved with
// short lines — makes sure continuation rows don't leak across logical
// lines.
func TestVisualLayout_MultipleLinesMixedLengths(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 50) + "\nok\n" + strings.Repeat("b", 30)
	e := NewTextEditor(content, "", 80, 10)
	e.SoftWrap = true
	layout := e.visualLayout(20)
	// line 0: 3 rows (20 + 20 + 10); line 1: 1 row; line 2: 2 rows (20 + 10)
	want := []visualRow{
		{0, 0, 20}, {0, 20, 40}, {0, 40, 50},
		{1, 0, 2},
		{2, 0, 20}, {2, 20, 30},
	}
	if len(layout) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(layout), len(want), layout)
	}
	for i, vr := range want {
		if layout[i] != vr {
			t.Errorf("row %d: got %+v, want %+v", i, layout[i], vr)
		}
	}
}

// TestVisualToLogical_RoundTripsCursor verifies that every logical
// position round-trips cleanly through the visual mapping.
func TestVisualToLogical_RoundTripsCursor(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("x", 95)+"\nshort", "", 80, 10)
	e.SoftWrap = true
	contentW := 20
	layout := e.visualLayout(contentW)

	for row := 0; row < len(e.Lines); row++ {
		rl := runeLen(e.Lines[row])
		for col := 0; col <= rl; col++ {
			e.CursorRow = row
			e.CursorCol = col
			vi := e.cursorVisualRow(layout)
			vr := layout[vi]
			vcol := col - vr.Start
			gotRow, gotCol := e.visualToLogical(layout, vi, vcol)
			if gotRow != row || gotCol != col {
				t.Errorf("(row=%d,col=%d) -> vi=%d vcol=%d -> (row=%d,col=%d)",
					row, col, vi, vcol, gotRow, gotCol)
			}
		}
	}
}

// TestCursorVisualRow_EndOfLogicalLine ensures a cursor sitting at the
// boundary between a visual row and its continuation is resolved to the
// continuation row, except at end-of-logical-line where it stays on the
// last visual row.
func TestCursorVisualRow_EndOfLogicalLine(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 40), "", 80, 10)
	e.SoftWrap = true
	layout := e.visualLayout(20)
	// Boundary between row 0 and row 1.
	e.CursorRow = 0
	e.CursorCol = 20
	if got := e.cursorVisualRow(layout); got != 1 {
		t.Errorf("cursor at wrap boundary: want vi=1 (continuation), got %d", got)
	}
	// End of logical line (col == runeLen).
	e.CursorCol = 40
	if got := e.cursorVisualRow(layout); got != 1 {
		t.Errorf("cursor at end-of-line: want vi=1 (last visual row), got %d", got)
	}
}

// TestCursorVisualRow_OutOfRangeLogical exercises the defensive branch:
// if CursorRow points past the document, the function falls back to
// the last visual row instead of returning 0 and referencing an
// unrelated logical line.
func TestCursorVisualRow_OutOfRangeLogical(t *testing.T) {
	t.Parallel()
	e := NewTextEditor("short\nalso short\nthird", "", 80, 10)
	e.SoftWrap = true
	layout := e.visualLayout(20)
	e.CursorRow = 99
	e.CursorCol = 0
	got := e.cursorVisualRow(layout)
	if got != len(layout)-1 {
		t.Errorf("out-of-range CursorRow: want vi=%d (last row), got %d",
			len(layout)-1, got)
	}
}

// TestVerticalMove_WithinWrappedLine moves the cursor down from a wrapped
// first row into its own continuation row, not to the next logical line.
func TestVerticalMove_WithinWrappedLine(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 60)+"\nnext line", "", 80, 10)
	e.SoftWrap = true
	setContentWidth(t, e, 20)
	e.CursorRow = 0
	e.CursorCol = 5
	e.verticalMove(1)
	if e.CursorRow != 0 {
		t.Errorf("want stay on logical row 0, got row=%d", e.CursorRow)
	}
	if e.CursorCol != 25 {
		t.Errorf("want col=25 (5 + 20 continuation offset), got col=%d", e.CursorCol)
	}
	// Another down-move reaches row 2 (second continuation row).
	e.verticalMove(1)
	if e.CursorRow != 0 || e.CursorCol != 45 {
		t.Errorf("second down: want row=0 col=45, got row=%d col=%d",
			e.CursorRow, e.CursorCol)
	}
	// Third down-move crosses into the next logical line.
	e.verticalMove(1)
	if e.CursorRow != 1 {
		t.Errorf("third down: want row=1, got row=%d", e.CursorRow)
	}
}

// TestVerticalMove_NoWrapLogicalStep covers the SoftWrap=false path — a
// down-move is one logical row.
func TestVerticalMove_NoWrapLogicalStep(t *testing.T) {
	t.Parallel()
	e := NewTextEditor("one\ntwo\nthree", "", 80, 10)
	e.SoftWrap = false
	e.CursorRow = 0
	e.CursorCol = 2
	e.verticalMove(1)
	if e.CursorRow != 1 {
		t.Errorf("want row=1, got %d", e.CursorRow)
	}
	// col clamps to 2 (within "two").
	if e.CursorCol != 2 {
		t.Errorf("want col=2, got %d", e.CursorCol)
	}
}

// TestEnsureCursorVisible_SoftWrapCountsVisualRows keeps ScrollTop
// aligned with the cursor's visual row, not its logical row.
func TestEnsureCursorVisible_SoftWrapCountsVisualRows(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 100), "", 80, 10)
	e.SoftWrap = true
	setContentWidth(t, e, 20)
	e.ViewHeight = 2
	// Cursor deep in the wrapped line.
	e.CursorRow = 0
	e.CursorCol = 80
	e.ensureCursorVisible()
	// With contentW=20, visual row 4 is [80,100). Want ScrollTop=3 so
	// the 2-row viewport shows rows 3 and 4.
	if e.ScrollTop != 3 {
		t.Errorf("want ScrollTop=3, got %d", e.ScrollTop)
	}
}

// TestRender_SoftWrap_NoTildeMarker — rendering a wrapped line must
// not inject the "~" truncation marker that non-wrap mode uses.
func TestRender_SoftWrap_NoTildeMarker(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 60), "", 80, 10)
	e.SoftWrap = true
	setContentWidth(t, e, 20)
	e.ViewHeight = 5
	out := e.Render()
	if strings.Contains(out, "a~") {
		t.Errorf("soft-wrap render should not contain truncation marker, got:\n%s", out)
	}
}

// TestRender_NoWrap_KeepsTildeMarker — regression guard: the legacy
// truncation behavior is preserved for TOML editor / log viewer.
func TestRender_NoWrap_KeepsTildeMarker(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 100), "", 80, 10)
	e.SoftWrap = false
	setContentWidth(t, e, 20)
	e.ViewHeight = 1
	out := e.Render()
	if !strings.Contains(out, "~") {
		t.Errorf("non-wrap render should contain truncation marker for overflowing line, got:\n%s", out)
	}
}

// TestRender_SoftWrap_SelectionSpansWrapBoundary asserts a reverse-video
// run is painted on BOTH visual rows when the selection straddles a
// wrap boundary — guards against regressions in the per-visual-row
// selection clipping in renderVisualRow.
func TestRender_SoftWrap_SelectionSpansWrapBoundary(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 40), "", 80, 10)
	e.SoftWrap = true
	setContentWidth(t, e, 20)
	e.ViewHeight = 3
	e.Sel = &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 15},
		Cursor: EditorPos{Row: 0, Col: 25},
	}
	out := e.Render()
	// Split render output by newline. Visual row 0 covers [0,20); the
	// selection [15,25) extends 5 chars into it. Visual row 1 covers
	// [20,40); the selection extends 5 chars from the start.
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 rendered lines, got %d", len(lines))
	}
	// ANSI reverse-video sequence is "\x1b[7m". Both of the first two
	// visual rows must contain at least one such run.
	if !strings.Contains(lines[0], "\x1b[7m") {
		t.Errorf("visual row 0 missing selection highlight, got:\n%q", lines[0])
	}
	if !strings.Contains(lines[1], "\x1b[7m") {
		t.Errorf("visual row 1 (continuation) missing selection highlight, got:\n%q", lines[1])
	}
}

// TestRender_EndOfLineCursor_PastSelection — regression guard for the
// bug where cursor at end-of-line past a shorter selection was
// invisible (no reverse-video glyph on the reserved padding cell).
func TestRender_EndOfLineCursor_PastSelection(t *testing.T) {
	t.Parallel()
	e := NewTextEditor(strings.Repeat("a", 20), "", 80, 10)
	e.SoftWrap = false
	setContentWidth(t, e, 40)
	e.ViewHeight = 1
	e.CursorRow = 0
	e.CursorCol = 20 // at end of line
	e.Sel = &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 5},
		Cursor: EditorPos{Row: 0, Col: 10},
	}
	out := e.Render()
	// The bug rendered only ONE reverse-video run (the selection).
	// The fix adds a second one at end-of-line for the cursor glyph.
	runs := strings.Count(out, "\x1b[7m")
	if runs < 2 {
		t.Errorf("want >=2 reverse-video runs (selection + EOL cursor), got %d in:\n%q",
			runs, out)
	}
}
