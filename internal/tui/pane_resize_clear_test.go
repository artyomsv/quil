package tui

import (
	"strings"
	"testing"
)

// AI panes (claude-code, opencode) repaint their whole viewport when the
// child sees a resize. Quil's VT emulator does not reflow on resize, so
// without intervention the repaint lands BELOW the stale frame wrapped at
// the old width — mixed-width text and duplicated transcript chunks on the
// visible screen. ResizeVT therefore pushes the old frame into scrollback
// (honest history, no data loss) and hands the child a blank screen.

// aiPane builds a claude-code pane at 40x10 with content written at that width.
func aiPane(t *testing.T) *PaneModel {
	t.Helper()
	p := NewPaneModel("pane-clear-test", 4096)
	p.Type = "claude-code"
	p.ResizeVT(40, 10)
	p.AppendOutput([]byte("first line\r\nsecond line\r\n" + strings.Repeat("w", 60) + "\r\nprompt> "))
	return p
}

func screenText(p *PaneModel) string {
	var b strings.Builder
	for y := 0; y < p.vt.Height(); y++ {
		for x := 0; x < p.vt.Width(); x++ {
			if c := p.vt.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
	}
	return b.String()
}

func TestResizeVT_WidthChange_AIPane_CleansScreen(t *testing.T) {
	p := aiPane(t)
	defer p.Dispose()
	if p.screenBlank() {
		t.Fatal("setup: screen must have content before resize")
	}
	sbBefore := p.vt.ScrollbackLen()

	p.ResizeVT(80, 10)

	if !p.screenBlank() {
		t.Errorf("screen not blank after width change:\n%q", screenText(p))
	}
	if p.vt.ScrollbackLen() <= sbBefore {
		t.Errorf("scrollback %d -> %d: old screen content must be pushed, not dropped",
			sbBefore, p.vt.ScrollbackLen())
	}
	if pos := p.vt.CursorPosition(); pos.X != 0 || pos.Y != 0 {
		t.Errorf("cursor at %d,%d, want 0,0 (homed)", pos.X, pos.Y)
	}
}

func TestResizeVT_WidthChange_ScrollbackKeepsContent(t *testing.T) {
	p := aiPane(t)
	defer p.Dispose()
	p.ResizeVT(80, 10)
	found := false
	for y := 0; y < p.vt.ScrollbackLen() && !found; y++ {
		var b strings.Builder
		for x := 0; x < 40; x++ {
			if c := p.vt.ScrollbackCellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		if strings.Contains(b.String(), "first line") {
			found = true
		}
	}
	if !found {
		t.Error("pre-resize content missing from scrollback — data loss")
	}
}

func TestResizeVT_HeightOnlyChange_KeepsScreen(t *testing.T) {
	p := aiPane(t)
	defer p.Dispose()
	p.ResizeVT(40, 20)
	if p.screenBlank() {
		t.Error("height-only change must not clear the screen")
	}
}

func TestResizeVT_TerminalPane_KeepsScreen(t *testing.T) {
	p := aiPane(t)
	defer p.Dispose()
	p.Type = "terminal"
	p.ResizeVT(80, 10)
	if p.screenBlank() {
		t.Error("terminal panes must keep their screen on width change")
	}
}

func TestResizeVT_AltScreen_NoScrollbackPush(t *testing.T) {
	p := NewPaneModel("pane-alt-test", 4096)
	defer p.Dispose()
	p.Type = "claude-code"
	p.ResizeVT(40, 10)
	p.AppendOutput([]byte("\x1b[?1049h\x1b[Halt screen content"))
	sbBefore := p.vt.ScrollbackLen()
	p.ResizeVT(80, 10)
	if p.vt.ScrollbackLen() != sbBefore {
		t.Errorf("altscreen pane pushed to scrollback on width change (%d -> %d)",
			sbBefore, p.vt.ScrollbackLen())
	}
}

func TestClearsOnWidthResize_Types(t *testing.T) {
	cases := map[string]bool{
		"claude-code": true,
		"opencode":    true,
		"terminal":    false,
		"":            false,
		"ssh":         false,
		"k9s":         false,
	}
	for typ, want := range cases {
		if got := clearsOnWidthResize(typ); got != want {
			t.Errorf("clearsOnWidthResize(%q) = %v, want %v", typ, got, want)
		}
	}
}
