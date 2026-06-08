package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// Interactive plugin panes (claude-code, opencode, ...) position the VT
// cursor at their input caret. Quil renders it as a software reverse-video
// overlay (insertCursor) — the same mechanism terminal panes use — instead
// of the real hardware cursor: repositioning the hardware cursor every
// frame desynced Bubble Tea's diff writer on Windows, landing the first
// typed character one cell off ("Test" rendered as "T est").

func cursorTestModel(paneType string) (Model, *PaneModel) {
	pane := NewPaneModel("p1", testRingBufSize)
	pane.Type = paneType
	pane.Active = true
	tab := NewTabModel("t1", "Test")
	tab.Root = &LayoutNode{Pane: pane}
	tab.ActivePane = "p1"
	m := Model{
		cfg:           config.Default(),
		tabs:          []*TabModel{tab},
		activeTab:     0,
		width:         80,
		height:        24,
		notifications: NewNotificationCenter(30, 50),
	}
	m.resizeTabs()
	return m, pane
}

func TestRenderContent_InteractivePane_OverlaysSoftwareCursor(t *testing.T) {
	for _, typ := range []string{"claude-code", "opencode", "terminal"} {
		_, pane := cursorTestModel(typ)
		pane.AppendOutput([]byte("abc")) // VT cursor at (3, 0)

		first, _, _ := strings.Cut(pane.renderContent(nil), "\n")
		if !strings.Contains(first, "\x1b[7m") {
			t.Errorf("type %q: cursor line %q has no reverse-video overlay", typ, first)
		}
	}
}

func TestRenderContent_NoOverlayWhenHidden(t *testing.T) {
	t.Run("DECTCEM hidden", func(t *testing.T) {
		_, pane := cursorTestModel("claude-code")
		pane.AppendOutput([]byte("abc\x1b[?25l"))
		first, _, _ := strings.Cut(pane.renderContent(nil), "\n")
		if strings.Contains(first, "\x1b[7m") {
			t.Error("overlay rendered although the app hid the cursor")
		}
	})

	t.Run("inactive pane", func(t *testing.T) {
		_, pane := cursorTestModel("claude-code")
		pane.Active = false
		pane.AppendOutput([]byte("abc"))
		first, _, _ := strings.Cut(pane.renderContent(nil), "\n")
		if strings.Contains(first, "\x1b[7m") {
			t.Error("overlay rendered on an inactive pane")
		}
	})
}

// The caret line is rebuilt cell-by-cell by insertCursor. Styles must reset
// between cells exactly like styledCellLine does — without the reset, a
// styled run (claude-code's dim hint text) bleeds into the unstyled cells
// after it, painting typed characters in colors that can vanish against the
// background.
func TestInsertCursor_NoStyleBleedAcrossCells(t *testing.T) {
	_, pane := cursorTestModel("claude-code")
	// Red 'R' followed by explicitly reset 'N', cursor lands at col 2.
	pane.AppendOutput([]byte("\x1b[31mR\x1b[0mN"))

	first, _, _ := strings.Cut(pane.renderContent(nil), "\n")

	// 'N' must not inherit the red style: between the styled 'R' and the
	// unstyled 'N' there has to be an SGR reset.
	rIdx := strings.IndexByte(first, 'R')
	nIdx := strings.IndexByte(first, 'N')
	if rIdx < 0 || nIdx < 0 || nIdx < rIdx {
		t.Fatalf("unexpected caret line %q", first)
	}
	between := first[rIdx:nIdx]
	if !strings.Contains(between, "\x1b[m") && !strings.Contains(between, "\x1b[0m") {
		t.Errorf("style bleeds from styled cell into unstyled cell: %q", first)
	}
}

func TestView_NeverSetsHardwareCursor(t *testing.T) {
	m, pane := cursorTestModel("claude-code")
	pane.AppendOutput([]byte("abc"))

	if v := m.View(); v.Cursor != nil {
		t.Errorf("View sets hardware cursor at (%d,%d) — must stay nil (software overlay only)", v.Cursor.X, v.Cursor.Y)
	}
}
