package tui

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/charmbracelet/x/ansi"
)

// Wide glyphs (CJK, emoji) occupy two columns: the lead cell carries the
// rune with Width=2, the following continuation cell has Width=0 and empty
// content. The cell-loop renderers must skip continuation cells — emitting
// a space for them drifts everything after the glyph one column right.

func wideCharPane(t *testing.T) *PaneModel {
	t.Helper()
	pane := NewPaneModel("wide", testRingBufSize)
	pane.ResizeVT(20, 4)
	pane.AppendOutput([]byte("你X")) // 你 at cols 0-1, X at col 2
	return pane
}

func TestStyledCellLine_WideChar_NoPhantomSpace(t *testing.T) {
	pane := wideCharPane(t)

	line := pane.styledCellLine(func(x int) *uv.Cell {
		return pane.vt.CellAt(x, 0)
	}, pane.vt.Width())

	if got := ansi.StringWidth(line); got != 3 {
		t.Errorf("rendered line %q has display width %d, want 3 (你=2 + X=1)", line, got)
	}
}

func TestStyledCellLineWithSelection_WideChar_NoPhantomSpace(t *testing.T) {
	pane := wideCharPane(t)

	getCell := func(x int) *uv.Cell { return pane.vt.CellAt(x, 0) }

	t.Run("no selection on row", func(t *testing.T) {
		line := pane.styledCellLineWithSelection(getCell, pane.vt.Width(), -1, -1)
		if got := ansi.StringWidth(line); got != 3 {
			t.Errorf("rendered line %q has display width %d, want 3", line, got)
		}
	})

	t.Run("selection covering wide char", func(t *testing.T) {
		line := pane.styledCellLineWithSelection(getCell, pane.vt.Width(), 0, 2)
		if got := ansi.StringWidth(line); got != 3 {
			t.Errorf("rendered line %q has display width %d, want 3", line, got)
		}
	})
}

func TestInsertCursor_WideChar_NoPhantomSpace(t *testing.T) {
	pane := wideCharPane(t)

	// insertCursor rebuilds the cursor's row cell-by-cell across the full
	// pane width; the phantom continuation space pushes it to width+1.
	content := pane.vt.Render()
	out := pane.insertCursor(content)

	firstLine, _, _ := strings.Cut(out, "\n")
	w := pane.vt.Width()
	if got := ansi.StringWidth(firstLine); got != w {
		t.Errorf("cursor line has display width %d, want %d (pane width)", got, w)
	}
}
