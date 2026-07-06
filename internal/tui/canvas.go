package tui

const defaultMinNativeCols = 80

// paneVTSize is the single authority for a pane's terminal grid size.
// Non-canvas panes get their layout rect minus the border. Wide-canvas
// panes (claude-code, opencode via [display] wide_canvas) use the window
// canvas ONLY when their rect is too narrow to render the AI transcript
// readably (rectW-2 < minNativeCols); at or above the threshold they
// render natively at their own rect size, like a normal pane. The
// preview renderer (pane_preview.go) adapts the wide buffer to small
// rects when the canvas path is taken. A zero canvas (tests, before the
// first resizeTabs) falls back to the rect. minNativeCols <= 0 defaults
// to defaultMinNativeCols (80).
func paneVTSize(wideCanvas bool, minNativeCols, rectW, rectH, canvasW, canvasH int) (cols, rows int) {
	if minNativeCols <= 0 {
		minNativeCols = defaultMinNativeCols
	}
	w, h := rectW, rectH
	// Use the window canvas ONLY when the rect is too narrow to render the
	// AI transcript readably; at or above the threshold the pane renders
	// natively at its own width (and resizes like a normal pane).
	if wideCanvas && canvasW > 0 && canvasH > 0 && rectW-2 < minNativeCols {
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
