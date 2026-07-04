package tui

import "testing"

func TestPaneVTSize(t *testing.T) {
	cases := []struct {
		name                     string
		wide                     bool
		rectW, rectH, canW, canH int
		wantCols, wantRows       int
	}{
		{"normal pane uses rect", false, 60, 20, 200, 50, 58, 18},
		{"canvas pane uses canvas", true, 60, 20, 200, 50, 198, 48},
		{"canvas degenerate clamps", true, 60, 20, 1, 1, 1, 1},
		{"normal degenerate clamps", false, 2, 2, 200, 50, 1, 1},
		{"zero canvas falls back to rect", true, 60, 20, 0, 0, 58, 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := paneVTSize(tc.wide, tc.rectW, tc.rectH, tc.canW, tc.canH)
			if c != tc.wantCols || r != tc.wantRows {
				t.Errorf("got %dx%d, want %dx%d", c, r, tc.wantCols, tc.wantRows)
			}
		})
	}
}

// Zoom must not resize a canvas pane: the grid resize and the focus-mode
// resize must produce the same canvas-derived VT size. This is the core
// invariant of the wide-canvas design — Ctrl+E stops being a PTY resize.
func TestTabResize_CanvasPane_FocusToggleKeepsVTSize(t *testing.T) {
	a := NewPaneModel("a", 4096)
	defer a.Dispose()
	a.WideCanvas = true
	b := NewPaneModel("b", 4096)
	defer b.Dispose()

	tab := NewTabModel("t", "T")
	tab.Root = NewLeaf(a)
	ph := tab.Root.SplitLeaf("a", SplitHorizontal)
	ph.Pane = b
	tab.ActivePane = "a"

	tab.SetCanvas(200, 50)
	tab.Resize(200, 50)
	wantW, wantH := 198, 48
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Fatalf("grid: canvas pane VT %dx%d, want %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}
	if b.vt.Width() >= wantW {
		t.Fatalf("non-canvas pane VT width %d must track its rect, not the canvas", b.vt.Width())
	}

	tab.ToggleFocus()
	tab.Resize(200, 50)
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Errorf("focus: canvas pane VT %dx%d, want unchanged %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}

	tab.ExitFocus()
	tab.Resize(200, 50)
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Errorf("back to grid: canvas pane VT %dx%d, want unchanged %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}
}
