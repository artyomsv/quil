package tui

import "testing"

// Legacy conhost shrinks its screen buffer when the window shrinks, but
// NEVER grows it back when the window is enlarged or maximized — it paints
// dead space instead, and GetConsoleScreenBufferInfo keeps reporting the
// stale grid (observed: window 1936x1056 px, grid stuck at 117x30).
// consoleGridTarget decides when the Win32 fixup must grow the grid.

func TestConsoleGridTarget(t *testing.T) {
	tests := []struct {
		name                           string
		clientW, clientH, fontW, fontH int
		curCols, curRows               int
		wantCols, wantRows             int
		wantGrow                       bool
	}{
		{
			name:    "maximized window with stale small grid grows",
			clientW: 1920, clientH: 1020, fontW: 8, fontH: 16,
			curCols: 117, curRows: 30,
			wantCols: 240, wantRows: 63, wantGrow: true,
		},
		{
			name:    "grid already matches client area — no grow",
			clientW: 960, clientH: 480, fontW: 8, fontH: 16,
			curCols: 120, curRows: 30,
			wantGrow: false,
		},
		{
			name:    "window smaller than grid (shrink) — conhost handles natively",
			clientW: 800, clientH: 400, fontW: 8, fontH: 16,
			curCols: 120, curRows: 40,
			wantGrow: false,
		},
		{
			name:    "one axis grows, other axis never shrinks below current",
			clientW: 1920, clientH: 400, fontW: 8, fontH: 16,
			curCols: 117, curRows: 30,
			wantCols: 240, wantRows: 30, wantGrow: true,
		},
		{
			name:    "zero font metrics — refuse to act",
			clientW: 1920, clientH: 1020, fontW: 0, fontH: 16,
			curCols: 117, curRows: 30,
			wantGrow: false,
		},
		{
			name:    "zero client area — refuse to act",
			clientW: 0, clientH: 0, fontW: 8, fontH: 16,
			curCols: 117, curRows: 30,
			wantGrow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cols, rows, grow := consoleGridTarget(tt.clientW, tt.clientH, tt.fontW, tt.fontH, tt.curCols, tt.curRows)
			if grow != tt.wantGrow {
				t.Fatalf("grow = %v, want %v", grow, tt.wantGrow)
			}
			if !grow {
				return
			}
			if cols != tt.wantCols || rows != tt.wantRows {
				t.Errorf("target = %dx%d, want %dx%d", cols, rows, tt.wantCols, tt.wantRows)
			}
		})
	}
}
