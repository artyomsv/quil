package tui

// consoleGridTarget computes the cell grid that fits a console window's
// client area, returning grow=true when it exceeds the current grid on
// either axis.
//
// Legacy conhost shrinks its screen buffer together with the window, but
// never grows it back when the window is enlarged or maximized — it paints
// dead space instead and GetConsoleScreenBufferInfo keeps reporting the
// stale grid, so polling alone can't recover. Only growth needs the Win32
// fixup (fixupConsoleGrid); shrinks are handled natively.
func consoleGridTarget(clientW, clientH, fontW, fontH, curCols, curRows int) (cols, rows int, grow bool) {
	if clientW <= 0 || clientH <= 0 || fontW <= 0 || fontH <= 0 {
		return 0, 0, false
	}
	cols = clientW / fontW
	rows = clientH / fontH
	if cols < 1 || rows < 1 {
		return 0, 0, false
	}
	if cols <= curCols && rows <= curRows {
		return 0, 0, false
	}
	// Grow only — never shrink an axis as a side effect of the fixup.
	if cols < curCols {
		cols = curCols
	}
	if rows < curRows {
		rows = curRows
	}
	return cols, rows, true
}
