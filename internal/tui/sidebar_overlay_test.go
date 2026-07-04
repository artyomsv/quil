package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// The notification sidebar is a compositor overlay (overlayRight): it takes
// no layout width, so panes keep full width and never resize when it
// toggles — that resize churn was the amplifier behind the claude-code
// repaint artifacts (mixed-width wraps + duplicated transcript chunks in
// background panes).

func newSidebarTestModel() Model {
	return Model{
		cfg:           config.Default(),
		notifications: NewNotificationCenter(30, 50),
		attached:      true,
		width:         200,
		height:        50,
		tabs:          []*TabModel{NewTabModel("tab-1", "Shell")},
		activeTab:     0,
	}
}

func TestPaneAreaWidth_SidebarVisible_FullWidth(t *testing.T) {
	m := newSidebarTestModel()
	m.notifications.visible = true
	if got := m.paneAreaWidth(); got != 200 {
		t.Errorf("paneAreaWidth = %d, want 200 (sidebar must not reserve width)", got)
	}
}

func TestSidebarOverlayWidth_States(t *testing.T) {
	m := newSidebarTestModel()
	if got := m.sidebarOverlayWidth(); got != 0 {
		t.Errorf("hidden: got %d, want 0", got)
	}
	m.notifications.visible = true
	if got := m.sidebarOverlayWidth(); got != m.notifications.width {
		t.Errorf("visible: got %d, want %d", got, m.notifications.width)
	}
	m.dialog = dialogAbout
	if got := m.sidebarOverlayWidth(); got != 0 {
		t.Errorf("dialog open: got %d, want 0", got)
	}
	m.dialog = dialogNone
	m.width = m.notifications.width + minTermWidth - 1
	if got := m.sidebarOverlayWidth(); got != 0 {
		t.Errorf("too narrow: got %d, want 0", got)
	}
}

func TestSidebarOverlayWidth_FocusMode_StillVisible(t *testing.T) {
	m := newSidebarTestModel()
	m.notifications.visible = true
	m.tabs[0].focusMode = true
	if got := m.sidebarOverlayWidth(); got != m.notifications.width {
		t.Errorf("focus mode: got %d, want %d (overlay draws in focus mode too)", got, m.notifications.width)
	}
}

func TestSidebarSwallowsMouse_Regions(t *testing.T) {
	m := newSidebarTestModel()
	m.notifications.visible = true
	sw := m.notifications.width
	edge := m.width - sw
	cases := []struct {
		name string
		x, y int
		want bool
	}{
		{"inside sidebar", edge + 1, 5, true},
		{"left edge of sidebar", edge, 5, true},
		{"pane area", edge - 1, 5, false},
		{"tab bar row", edge + 1, 0, false},
		{"status bar row", edge + 1, m.height - 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.sidebarSwallowsMouse(tc.x, tc.y); got != tc.want {
				t.Errorf("sidebarSwallowsMouse(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
			}
		})
	}
	m.notifications.visible = false
	if m.sidebarSwallowsMouse(edge+1, 5) {
		t.Error("hidden sidebar must not swallow mouse")
	}
}
