package tui

// paneVTSize is the single authority for a pane's terminal grid size.
// Non-canvas panes get their layout rect minus the border. Wide-canvas
// panes (claude-code, opencode via [display] wide_canvas) always get the
// full tab-area canvas minus the border, regardless of their rect — the
// pane's PTY therefore never resizes when the grid, sidebar, notes panel,
// or focus mode change; the preview renderer (pane_preview.go) adapts the
// wide buffer to small rects instead. A zero canvas (tests, before the
// first resizeTabs) falls back to the rect.
func paneVTSize(wideCanvas bool, rectW, rectH, canvasW, canvasH int) (cols, rows int) {
	w, h := rectW, rectH
	if wideCanvas && canvasW > 0 && canvasH > 0 {
		w, h = canvasW, canvasH
	}
	cols, rows = w-2, h-2
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}
