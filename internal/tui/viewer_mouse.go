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
func (m Model) scrollViewer(button tea.MouseButton) {
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
