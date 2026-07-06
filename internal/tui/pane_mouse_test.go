package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestPaneModel_MouseTracking_ReflectsModeSequences feeds the DEC mouse-mode
// set/reset escape sequences through the real VT emulator (the path used by
// live PTY output) and verifies MouseTracking() tracks them. This is the
// signal the wheel handler uses to decide whether to forward the event to the
// child app (opencode, claude-code, vim, …) instead of scrolling Quil's own
// scrollback.
func TestPaneModel_MouseTracking_ReflectsModeSequences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seq  string
	}{
		{"x10 ?9", "\x1b[?9h"},
		{"normal ?1000", "\x1b[?1000h"},
		{"button-event ?1002", "\x1b[?1002h"},
		{"any-event ?1003", "\x1b[?1003h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := NewPaneModel("mt-"+tt.name, testRingBufSize)
			if p.MouseTracking() {
				t.Fatal("MouseTracking() = true before any mode set, want false")
			}
			p.AppendOutput([]byte(tt.seq))
			if !p.MouseTracking() {
				t.Errorf("MouseTracking() = false after %q, want true", tt.seq)
			}
		})
	}
}

// TestPaneModel_MouseTracking_DisableOfUnsetModeKeepsTracking guards the
// per-mode bool design: an app may enable ?1002 then later reset ?1000 (a mode
// it never set). A single shared bool would wrongly clear tracking; per-mode
// bools keep ?1002 active.
func TestPaneModel_MouseTracking_DisableOfUnsetModeKeepsTracking(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("mt-disable", testRingBufSize)
	p.AppendOutput([]byte("\x1b[?1002h")) // enable button-event tracking
	p.AppendOutput([]byte("\x1b[?1000l")) // reset normal tracking (never set)
	if !p.MouseTracking() {
		t.Error("MouseTracking() = false after resetting an unset mode, want true (?1002 still active)")
	}
	p.AppendOutput([]byte("\x1b[?1002l")) // reset the mode that was set
	if p.MouseTracking() {
		t.Error("MouseTracking() = true after resetting ?1002, want false")
	}
}

// TestPaneModel_WheelForwardSeq_NoTrackingReturnsNil ensures a plain terminal
// pane (no mouse tracking) never forwards — the wheel handler must fall through
// to Quil's local scrollback for those.
func TestPaneModel_WheelForwardSeq_NoTrackingReturnsNil(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("wf-none", testRingBufSize)
	if seq := p.wheelForwardSeq(true, 0, 0); seq != nil {
		t.Errorf("wheelForwardSeq with no tracking = %q, want nil", seq)
	}
}

// TestPaneModel_WheelForwardSeq_SGRAndX10 verifies the encoded sequence picks
// SGR when ?1006 is set and legacy X10 otherwise, with wheel-up/down button
// codes and 1-based clamped coordinates.
func TestPaneModel_WheelForwardSeq_SGRAndX10(t *testing.T) {
	t.Parallel()

	t.Run("sgr wheel up", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("wf-sgr", testRingBufSize)
		p.ResizeVT(80, 24)
		p.AppendOutput([]byte("\x1b[?1000h\x1b[?1006h")) // normal tracking + SGR
		got := p.wheelForwardSeq(true, 4, 9)
		// Wheel up button = 64; SGR is 1-based: col 5, row 10; press = 'M'.
		want := []byte("\x1b[<64;5;10M")
		if !bytes.Equal(got, want) {
			t.Errorf("wheelForwardSeq = %q, want %q", got, want)
		}
	})

	t.Run("sgr wheel down", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("wf-sgr-down", testRingBufSize)
		p.ResizeVT(80, 24)
		p.AppendOutput([]byte("\x1b[?1000h\x1b[?1006h"))
		got := p.wheelForwardSeq(false, 0, 0)
		want := []byte("\x1b[<65;1;1M") // wheel down = 65
		if !bytes.Equal(got, want) {
			t.Errorf("wheelForwardSeq = %q, want %q", got, want)
		}
	})

	t.Run("x10 fallback when no sgr", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("wf-x10", testRingBufSize)
		p.ResizeVT(80, 24)
		p.AppendOutput([]byte("\x1b[?1000h")) // tracking but no SGR
		got := p.wheelForwardSeq(true, 4, 9)
		// X10 encodes button 64 -> 32+64 = 96 = ' '+'@' ... assert it is a valid
		// CSI M sequence and does NOT use the SGR '<' form.
		if !bytes.HasPrefix(got, []byte("\x1b[M")) {
			t.Errorf("wheelForwardSeq = %q, want X10 CSI M prefix", got)
		}
	})
}

// TestPaneModel_WheelForwardSeq_ClampsToGrid verifies out-of-range coordinates
// (cursor outside the pane content) clamp into the emulator grid rather than
// producing negative or oversized SGR coordinates.
func TestPaneModel_WheelForwardSeq_ClampsToGrid(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("wf-clamp", testRingBufSize)
	p.ResizeVT(80, 24)
	p.AppendOutput([]byte("\x1b[?1000h\x1b[?1006h"))

	// Negative coords clamp to (0,0) -> SGR 1;1.
	if got, want := p.wheelForwardSeq(true, -5, -3), []byte("\x1b[<64;1;1M"); !bytes.Equal(got, want) {
		t.Errorf("negative clamp = %q, want %q", got, want)
	}
	// Past the grid clamps to width-1/height-1 -> SGR 80;24.
	if got, want := p.wheelForwardSeq(true, 999, 999), []byte("\x1b[<64;80;24M"); !bytes.Equal(got, want) {
		t.Errorf("oversize clamp = %q, want %q", got, want)
	}
}

// A click+drag inside a narrow preview pane must build a selection whose
// anchors are mapped through the wrapped-preview layout (previewPosAt), not
// the raw 1:1 screen mapping used for native panes.
//
// Construction note: applyWorkspaceState's "fresh pane, no saved layout"
// branch always stacks new panes with a VERTICAL split, which leaves each
// leaf at the full window width — previewMode() (width-only) could never
// observe the canvas branch through that path (see canvas_test.go's
// TestApplyWorkspaceState_ThresholdSelectsNativeOrCanvas). This test instead
// supplies a saved Layout with an explicit SplitHorizontal tree via the
// restoreTabLayout branch, so the first leaf's rect is genuinely narrower
// than the canvas and lands in preview mode.
//
// Crop mode (previewWrap=false, the default) would NOT distinguish the fixed
// mapping from the old raw screen mapping here: with no scrollback and short
// content, every absolute row occupies exactly one visual row starting at
// column 0, so both mappings agree by coincidence. The test therefore turns
// on soft-wrap and makes the first logical row wrap into exactly two visual
// segments — the second visual row (screen row 1) still belongs to absolute
// row 0 at a non-zero column offset, and the following logical row ("second
// line") only starts at screen row 2. The old raw mapping treated screen row
// N as absolute row N unconditionally, which is wrong for both cells once
// wrapping is in play; the previewPosAt-routed mapping gets both right.
func TestUpdateMouseSelection_PreviewPane_RoutesThroughLayout(t *testing.T) {
	root := NewLeaf(NewPaneModel("p1", 4096))
	ph := root.SplitLeaf("p1", SplitHorizontal)
	ph.Pane = NewPaneModel("p2", 4096)
	layout, err := MarshalLayout(root)
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}

	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		pluginRegistry: flaggedCanvasRegistry(t),
		mcpHighlights:  make(map[string]bool),
		attached:       true,
		width:          120,
		height:         40,
	}
	state := WorkspaceStateMsg{
		ActiveTab: "t1",
		Tabs:      []TabInfo{{ID: "t1", Name: "AI", Panes: []string{"p1", "p2"}, Layout: layout}},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "t1", Type: "claude-code"},
			{ID: "p2", TabID: "t1", Type: "claude-code"},
		},
	}
	m.applyWorkspaceState(state)
	m.resizeTabs()
	tab := m.tabs[0]
	p := tab.Leaves()[0]
	if !p.previewMode() {
		t.Fatalf("setup: pane must be in preview (innerW %d, vt %d)", p.Width-2, p.vt.Width())
	}

	// Soft-wrap on; row 0 is innerW+15 'a's (wraps into exactly 2 segments —
	// the tail of 15 cols is short enough it can't wrap again), row 1 is a
	// short distinct line.
	p.previewWrap = true
	innerW := p.Width - 2
	p.AppendOutput([]byte(strings.Repeat("a", innerW+15) + "\r\nsecond line\r\n"))
	pvLayout := p.previewLayoutFor(innerW)
	if len(pvLayout.segs) < 2 || len(pvLayout.segs[0]) != 2 {
		t.Fatalf("setup: expected row 0 to wrap into exactly 2 segments, got %d (innerW=%d)", len(pvLayout.segs[0]), innerW)
	}
	// vAnchor (visual row 1) is row 0's wrapped continuation segment; vCursor
	// (visual row 2, i.e. pvLayout.prefix[1]) is the first — and only —
	// visual row of logical row 1 ("second line"). viewStart mirrors the
	// live-view formula in renderPreview/previewPosAt: the emulator grid is a
	// fixed innerH rows regardless of how little was written, so it is
	// generally > 0, not 0.
	innerH := p.Height - 2
	viewStart := pvLayout.totalVisual() - innerH - p.scrollBack
	if viewStart < 0 {
		viewStart = 0
	}
	vAnchor, vCursor := 1, pvLayout.prefix[1]

	tabH := m.height - chromeHeight
	rect := tab.Root.FindPaneRectAt(0, 1, 0, 1, m.paneAreaWidth(), tabH)
	if rect == nil || rect.Pane != p {
		t.Fatalf("setup: expected first leaf at top-left, got %v", rect)
	}
	// Anchor: the wrapped continuation of row 0, a few columns into that
	// segment. Cursor: the start of logical row 1 ("second line").
	startX, startY := rect.OX+1+5, rect.OY+1+(vAnchor-viewStart)
	curX, curY := rect.OX+1+5, rect.OY+1+(vCursor-viewStart)
	m.mouseStartX, m.mouseStartY = startX, startY
	m.updateMouseSelection(tab, curX, curY, tabH)

	if m.selection == nil {
		t.Fatal("preview click+drag produced no selection")
	}
	if m.selection.PaneID != p.ID {
		t.Errorf("selection PaneID = %q, want %q", m.selection.PaneID, p.ID)
	}
	// The anchors must match previewPosAt exactly (proves the branch is wired).
	wantAncCol, wantAncLine, _ := p.previewPosAt(startX-rect.OX-1, startY-rect.OY-1)
	wantCurCol, wantCurLine, _ := p.previewPosAt(curX-rect.OX-1, curY-rect.OY-1)
	if wantAncLine != 0 {
		t.Fatalf("setup: expected anchor to resolve to absolute row 0 (wrapped continuation), got %d", wantAncLine)
	}
	if wantCurLine != 1 {
		t.Fatalf("setup: expected cursor to resolve to absolute row 1 (\"second line\"), got %d", wantCurLine)
	}
	if m.selection.Anchor.Col != wantAncCol || m.selection.Anchor.Line != wantAncLine {
		t.Errorf("anchor = (%d,%d), want (%d,%d)", m.selection.Anchor.Col, m.selection.Anchor.Line, wantAncCol, wantAncLine)
	}
	if m.selection.Cursor.Col != wantCurCol || m.selection.Cursor.Line != wantCurLine {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", m.selection.Cursor.Col, m.selection.Cursor.Line, wantCurCol, wantCurLine)
	}
}
