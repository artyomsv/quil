package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/clipboard"
)

// Mouse support for the full-screen read-only viewer (dialogLogViewer): the F1
// log viewers and the input-history entry opened via openReadonlyText.
//
// Keyboard selection (shift+arrows, ctrl+a) and copy (Enter / Ctrl+C) already
// worked — TextEditor.ReadOnly gates only the mutating paths. What was missing
// was the gesture every other text surface in Quil supports: drag to select,
// right-click to copy. Without it the natural way to lift a prompt out of the
// history detail view simply did nothing.
//
// The geometry lives in logViewerPosAt (model.go, beside its notes-editor
// counterpart); the selection primitives live on TextEditor (editor.go) and are
// shared with the notes editor.

// viewerOwnsMouse reports whether the read-only full-screen viewer is up and
// should receive mouse events. It is checked BEFORE every other swallow in
// Update's mouse branches because View() paints this viewer over the entire
// screen — panes, sidebar and lazygit overlay included — so paint priority and
// input priority have to agree.
func (m Model) viewerOwnsMouse() bool {
	return m.dialog == dialogLogViewer && m.tomlEditor != nil
}

// modalSwallowsMouse reports whether a modal dialog is up and must absorb the
// event instead of letting it reach the panes.
//
// View() renders ONLY the dialog while one is open — the panes are not on
// screen at all. Anything that falls through therefore acts on a layout the
// user cannot see: a left-press inside the history modal used to arm
// hitTestSplitBorder, and the release ran finishSplitDrag, moving a split ratio,
// resizing two PTYs and persisting the new layout to the daemon with no visible
// feedback whatsoever. A press at row 0 reached hitTestTab and switched the
// active tab behind the dialog the same way.
//
// This is checked AFTER viewerOwnsMouse: the full-screen read-only viewer is
// itself a dialog and handles its own mouse, so a blanket swallow ahead of it
// would take back the selection support this very commit adds.
func (m Model) modalSwallowsMouse() bool {
	return m.dialog != dialogNone
}

// handleViewerMouseClick arms a selection drag on left press and copies the
// current selection on right press, mirroring the notes editor and the pane
// text-selection gestures.
func (m Model) handleViewerMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	e := m.tomlEditor
	switch msg.Button {
	case tea.MouseRight:
		if e.Sel == nil || e.Sel.IsEmpty() {
			return m, nil
		}
		text := editorExtractText(e.Lines, e.Sel)
		e.Sel = nil
		if text == "" {
			return m, nil
		}
		return m, func() tea.Msg {
			if err := clipboard.Write(text); err != nil {
				log.Printf("viewer clipboard write: %v", err)
			}
			return nil
		}

	case tea.MouseLeft:
		row, col, ok := m.logViewerPosAt(msg.X, msg.Y)
		if !ok {
			return m, nil // title bar / status bar — not a document position
		}
		// The anchor is resolved once, here, and stored: a drag that scrolls
		// the buffer changes what screen row (x, y) maps to, so re-deriving it
		// from the press coordinates on every motion event would drift.
		m.clearDragState()
		m.viewerMouseDown = true
		m.viewerAnchorRow = row
		m.viewerAnchorCol = col
		e.setCursorAt(row, col)
		return m, nil
	}
	return m, nil
}

// handleViewerMouseMotion grows the selection while the left button is held.
func (m Model) handleViewerMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if !m.viewerMouseDown {
		return m, nil
	}
	row, col, ok := m.logViewerPosAt(msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	e := m.tomlEditor
	if e.Sel == nil || e.Sel.IsEmpty() {
		e.beginSelectionAt(m.viewerAnchorRow, m.viewerAnchorCol)
	}
	e.extendSelectionAt(row, col)
	return m, nil
}

// scrollViewer moves the viewer's window by the configured wheel step. It moves
// ScrollTop directly and leaves the cursor alone: a wheel is a look-around
// gesture, and dragging the cursor with it would silently collapse an active
// selection the user is about to copy.
//
// Pointer receiver even though every write today lands through the m.tomlEditor
// pointer — a value receiver makes a later `m.field = x` a silent no-op on a
// discarded copy, and a test asserting only on editor state would still pass.
// scrollHistoryList, called three lines away in Update, has the same shape.
func (m *Model) scrollViewer(button tea.MouseButton) {
	e := m.tomlEditor
	lines := m.cfg.UI.MouseScrollLines
	if lines < 1 {
		lines = 3
	}
	switch button {
	case tea.MouseWheelUp:
		e.ScrollTop -= lines
	case tea.MouseWheelDown:
		e.ScrollTop += lines
	default:
		return // horizontal wheel: nothing to scroll
	}
	// ScrollTop indexes VISUAL rows, so the bottom stop has to be measured
	// against the wrapped layout, not len(Lines) — with SoftWrap on, one long
	// prompt is a single logical line spanning many screen rows.
	maxTop := len(e.visualLayout(e.contentWForLayout())) - e.ViewHeight
	if e.ScrollTop > maxTop {
		e.ScrollTop = maxTop
	}
	if e.ScrollTop < 0 {
		e.ScrollTop = 0
	}
}
