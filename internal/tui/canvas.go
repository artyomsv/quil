package tui

const defaultMinNativeCols = 80

// paneVTSize is the single authority for a pane's terminal grid size.
// Non-canvas panes get their layout rect minus the border. Wide-canvas
// panes (claude-code, opencode via [display] wide_canvas) use the window
// canvas ONLY when their rect is too narrow to render the AI transcript
// readably; at or above the threshold they render natively at their own
// rect size, like a normal pane. The preview renderer (pane_preview.go)
// adapts the wide buffer to small rects when the canvas path is taken. A
// zero canvas (tests, before the first resizeTabs) falls back to the
// rect. minNativeCols <= 0 defaults to defaultMinNativeCols (80).
//
// SIZE comes from rectW/rectH; the MODE comes from nativeW — the width
// this rect would have if the project sidebar reserved nothing (see
// resizeNode). They differ only while the sidebar is open, and splitting
// them is what stops CHROME from re-moding a pane. With one width doing
// both jobs, a 22-column sidebar on a 185-column terminal moved an even
// two-pane split from 92/93 to 81/82 — straddling the threshold, so ONE
// of two identical siblings flipped to a 161-column canvas cropped into
// 79 while the other stayed native at 80. Toggling chrome must change how
// much of a pane you can see, never how it decides to render. nativeW <= 0
// means "no chrome to discount" and falls back to rectW.
func paneVTSize(wideCanvas bool, minNativeCols, rectW, rectH, nativeW, canvasW, canvasH int) (cols, rows int) {
	if minNativeCols <= 0 {
		minNativeCols = defaultMinNativeCols
	}
	if nativeW <= 0 {
		nativeW = rectW
	}
	w, h := rectW, rectH
	// Use the window canvas ONLY when the rect is too narrow to render the
	// AI transcript readably; at or above the threshold the pane renders
	// natively at its own width (and resizes like a normal pane).
	if wideCanvas && canvasW > 0 && canvasH > 0 && nativeW-2 < minNativeCols {
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
