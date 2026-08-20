package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// viewerModel opens content in the read-only full-screen viewer with the
// editor already sized the way renderTOMLEditorFullScreen sizes it, since the
// mouse geometry is derived from that layout.
func viewerModel(t *testing.T, content string, width, height int, softWrap bool) Model {
	t.Helper()
	m := Model{width: width, height: height, cfg: config.Default()}
	mdl, _ := m.openReadonlyText("Input @ test", content)
	got := mdl.(Model)
	got.tomlEditor.SoftWrap = softWrap
	got.tomlEditor.ViewWidth = width
	got.tomlEditor.ViewHeight = height - 2 // title bar + status bar
	return got
}

func TestViewerOwnsMouse(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "hello", 80, 24, false)
	if !m.viewerOwnsMouse() {
		t.Error("viewer should own the mouse while dialogLogViewer is up")
	}
	m.dialog = dialogNone
	if m.viewerOwnsMouse() {
		t.Error("viewer must not own the mouse once the dialog is closed")
	}
	m.dialog = dialogLogViewer
	m.tomlEditor = nil
	if m.viewerOwnsMouse() {
		t.Error("viewer must not own the mouse with no editor")
	}
}

// View() renders ONLY the dialog while a modal is open, so any mouse event that
// falls through to the pane hit-tests acts on a layout the user cannot see. The
// concrete pre-existing bug: a press inside the history modal armed
// hitTestSplitBorder and the release ran finishSplitDrag — moving a split ratio,
// resizing two PTYs and persisting the new layout, entirely invisibly.
func TestModalSwallowsMouse(t *testing.T) {
	t.Parallel()
	t.Run("every modal swallows", func(t *testing.T) {
		t.Parallel()
		for _, d := range []dialogScreen{
			dialogCommandHistory, dialogAbout, dialogSettings, dialogCreatePane,
			dialogCommandPalette, dialogProcesses, dialogConfirm,
		} {
			m := Model{width: 100, height: 30, dialog: d}
			if !m.modalSwallowsMouse() {
				t.Errorf("dialog %v does not swallow the mouse", d)
			}
		}
	})

	t.Run("no dialog does not swallow", func(t *testing.T) {
		t.Parallel()
		m := Model{width: 100, height: 30, dialog: dialogNone}
		if m.modalSwallowsMouse() {
			t.Error("panes must keep the mouse when no dialog is open")
		}
	})

	// The read-only viewer IS a dialog but handles its own mouse — a blanket
	// swallow ahead of viewerOwnsMouse would take back its drag-selection.
	t.Run("the viewer keeps its own mouse", func(t *testing.T) {
		t.Parallel()
		m := viewerModel(t, "alpha\nbravo", 80, 24, false)
		if !m.viewerOwnsMouse() {
			t.Fatal("viewerOwnsMouse must be true for dialogLogViewer")
		}
		gutter := m.tomlEditor.GutterWidth()
		mdl, _ := m.handleViewerMouseClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: gutter, Y: 1})
		if !mdl.(Model).viewerMouseDown {
			t.Error("viewer drag must still arm while a dialog is open")
		}
	})
}

// Terminals routinely drop the release when the button comes up outside the
// window. Without a reset on open, the next viewer's first hover resumed the
// previous drag from a document position in a different buffer.
func TestOpenViewer_ClearsAStaleDrag(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "alpha\nbravo", 80, 24, false)
	gutter := m.tomlEditor.GutterWidth()
	mdl, _ := m.handleViewerMouseClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: gutter, Y: 1})
	m = mdl.(Model)
	m.viewerAnchorRow, m.viewerAnchorCol = 1, 5
	if !m.viewerMouseDown {
		t.Fatal("setup: drag should be armed")
	}

	// No release arrives; the user Escs out and opens a different entry.
	mdl2, _ := m.openReadonlyText("Input @ other", "completely different text")
	got := mdl2.(Model)
	if got.viewerMouseDown {
		t.Fatal("a stale drag survived into the next viewer session")
	}
	mdl3, _ := got.handleViewerMouseMotion(tea.MouseMotionMsg{X: gutter + 4, Y: 1})
	if sel := mdl3.(Model).tomlEditor.Sel; sel != nil {
		t.Fatalf("hover with no button held created a selection: %+v", sel)
	}
}

func TestLogViewerPosAt(t *testing.T) {
	t.Parallel()
	// Three short lines, no wrapping — geometry only.
	m := viewerModel(t, "alpha\nbravo\ncharlie", 80, 24, false)
	gutter := m.tomlEditor.GutterWidth()

	t.Run("title bar is not a document position", func(t *testing.T) {
		if _, _, ok := m.logViewerPosAt(gutter+2, 0); ok {
			t.Error("row 0 is the title bar")
		}
	})
	t.Run("status bar is not a document position", func(t *testing.T) {
		if _, _, ok := m.logViewerPosAt(gutter+2, m.height-1); ok {
			t.Error("the last row is the status bar")
		}
	})
	t.Run("first body row is line 0", func(t *testing.T) {
		row, col, ok := m.logViewerPosAt(gutter+3, 1)
		if !ok || row != 0 || col != 3 {
			t.Fatalf("got (row=%d col=%d ok=%v), want (0, 3, true)", row, col, ok)
		}
	})
	t.Run("gutter clamps to column 0", func(t *testing.T) {
		row, col, ok := m.logViewerPosAt(0, 2)
		if !ok || row != 1 || col != 0 {
			t.Fatalf("got (row=%d col=%d ok=%v), want (1, 0, true)", row, col, ok)
		}
	})
	// Both branches of logViewerPosAt must return a position that is valid to
	// index Lines with, so ok==true means "valid document position" for every
	// caller — not just the ones that happen to clamp again.
	t.Run("click below the last line clamps into the document", func(t *testing.T) {
		row, col, ok := m.logViewerPosAt(gutter+200, m.height-2)
		if !ok {
			t.Fatal("a click inside the body should resolve")
		}
		if row < 0 || row >= len(m.tomlEditor.Lines) {
			t.Fatalf("row %d out of range for %d lines", row, len(m.tomlEditor.Lines))
		}
		if lineLen := runeLen(m.tomlEditor.Lines[row]); col < 0 || col > lineLen {
			t.Fatalf("col %d out of range for a %d-rune line", col, lineLen)
		}
	})
	t.Run("scroll offset is applied", func(t *testing.T) {
		scrolled := m
		scrolled.tomlEditor.ScrollTop = 1
		row, _, ok := scrolled.logViewerPosAt(gutter, 1)
		if !ok || row != 1 {
			t.Fatalf("got (row=%d ok=%v), want row 1 with ScrollTop=1", row, ok)
		}
	})
}

// With SoftWrap on, ScrollTop and screen rows index VISUAL rows — a click on
// the second screen row of one long prompt must resolve to line 0, not line 1.
func TestLogViewerPosAt_SoftWrapResolvesToLogicalPosition(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, strings.Repeat("x", 300), 80, 24, true)
	e := m.tomlEditor
	contentW := e.contentWForLayout()

	row, col, ok := m.logViewerPosAt(e.GutterWidth()+5, 2) // second body row
	if !ok {
		t.Fatal("second body row should be a document position")
	}
	if row != 0 {
		t.Fatalf("row = %d, want 0 (one wrapped logical line)", row)
	}
	if want := contentW + 5; col != want {
		t.Fatalf("col = %d, want %d (continuation row offset)", col, want)
	}
}

func TestViewerMouse_DragSelects(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "alpha\nbravo", 80, 24, false)
	gutter := m.tomlEditor.GutterWidth()

	mdl, _ := m.handleViewerMouseClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: gutter, Y: 1})
	m = mdl.(Model)
	if !m.viewerMouseDown {
		t.Fatal("left press should arm the drag")
	}
	mdl, _ = m.handleViewerMouseMotion(tea.MouseMotionMsg{X: gutter + 5, Y: 2})
	m = mdl.(Model)

	e := m.tomlEditor
	if e.Sel == nil || e.Sel.IsEmpty() {
		t.Fatal("drag should have produced a selection")
	}
	if got, want := editorExtractText(e.Lines, e.Sel), "alpha\nbravo"; got != want {
		t.Fatalf("selected %q, want %q", got, want)
	}
}

// Motion with no press is a hover, not a drag.
func TestViewerMouse_MotionWithoutPressDoesNotSelect(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "alpha\nbravo", 80, 24, false)
	mdl, _ := m.handleViewerMouseMotion(tea.MouseMotionMsg{X: 20, Y: 2})
	if e := mdl.(Model).tomlEditor; e.Sel != nil {
		t.Fatalf("hover created a selection: %+v", e.Sel)
	}
}

// A click outside the body (title/status bar) must not arm a drag — otherwise
// the next motion would select from a position the user never pressed on.
func TestViewerMouse_ClickOnChromeDoesNotArmDrag(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "alpha\nbravo", 80, 24, false)
	mdl, _ := m.handleViewerMouseClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 0})
	if mdl.(Model).viewerMouseDown {
		t.Error("a click on the title bar armed a drag")
	}
}

func TestViewerMouse_RightClickCopiesAndClearsSelection(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "alpha\nbravo", 80, 24, false)
	m.tomlEditor.beginSelectionAt(0, 0)
	m.tomlEditor.extendSelectionAt(0, 5)

	mdl, cmd := m.handleViewerMouseClick(tea.MouseClickMsg{Button: tea.MouseRight, X: 10, Y: 1})
	if cmd == nil {
		t.Fatal("right-click on a selection should return a clipboard command")
	}
	if sel := mdl.(Model).tomlEditor.Sel; sel != nil {
		t.Errorf("selection should be cleared after copy, got %+v", sel)
	}
}

func TestViewerMouse_RightClickWithoutSelectionIsANoOp(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, "alpha", 80, 24, false)
	if _, cmd := m.handleViewerMouseClick(tea.MouseClickMsg{Button: tea.MouseRight, X: 10, Y: 1}); cmd != nil {
		t.Error("right-click with nothing selected should do nothing")
	}
}

func TestScrollViewer(t *testing.T) {
	t.Parallel()
	// 200 short lines against a 22-row body.
	content := strings.TrimSuffix(strings.Repeat("line\n", 200), "\n")
	m := viewerModel(t, content, 80, 24, false)
	e := m.tomlEditor
	step := m.cfg.UI.MouseScrollLines
	if step < 1 {
		step = 3
	}

	m.scrollViewer(tea.MouseWheelDown)
	if e.ScrollTop != step {
		t.Fatalf("ScrollTop = %d, want %d after one wheel-down", e.ScrollTop, step)
	}
	m.scrollViewer(tea.MouseWheelUp)
	m.scrollViewer(tea.MouseWheelUp)
	if e.ScrollTop != 0 {
		t.Fatalf("ScrollTop = %d, want 0 (clamped at the top)", e.ScrollTop)
	}
	for i := 0; i < 500; i++ {
		m.scrollViewer(tea.MouseWheelDown)
	}
	if want := len(e.Lines) - e.ViewHeight; e.ScrollTop != want {
		t.Fatalf("ScrollTop = %d, want %d (clamped at the bottom)", e.ScrollTop, want)
	}
}

// The wheel must not disturb an active selection: ScrollTop moves, the cursor
// and Sel do not.
func TestScrollViewer_LeavesSelectionAlone(t *testing.T) {
	t.Parallel()
	content := strings.TrimSuffix(strings.Repeat("line\n", 200), "\n")
	m := viewerModel(t, content, 80, 24, false)
	e := m.tomlEditor
	e.beginSelectionAt(0, 0)
	e.extendSelectionAt(1, 4)

	m.scrollViewer(tea.MouseWheelDown)

	if e.Sel == nil || e.Sel.IsEmpty() {
		t.Fatal("wheel destroyed the selection")
	}
	if e.CursorRow != 1 || e.CursorCol != 4 {
		t.Fatalf("wheel moved the cursor to (%d,%d)", e.CursorRow, e.CursorCol)
	}
}

// SoftWrap makes one logical line span many screen rows, so the bottom stop has
// to be measured against the wrapped layout — len(Lines) would stop scrolling
// after the first screenful of a single long prompt.
func TestScrollViewer_SoftWrapScrollsPastTheLogicalLineCount(t *testing.T) {
	t.Parallel()
	m := viewerModel(t, strings.Repeat("x", 5000), 80, 24, true)
	e := m.tomlEditor
	for i := 0; i < 100; i++ {
		m.scrollViewer(tea.MouseWheelDown)
	}
	if e.ScrollTop <= len(e.Lines) {
		t.Fatalf("ScrollTop = %d, want well past len(Lines)=%d for a wrapped buffer", e.ScrollTop, len(e.Lines))
	}
	want := len(e.visualLayout(e.contentWForLayout())) - e.ViewHeight
	if e.ScrollTop != want {
		t.Fatalf("ScrollTop = %d, want %d (last visual row window)", e.ScrollTop, want)
	}
}
