package tui

import (
	"strings"
	"testing"
)

func TestTabLabel_ShowsEagerMarker(t *testing.T) {
	m := Model{activeTab: 0}
	eager := newTestPane("pane-1")
	eager.Eager = true
	root := &LayoutNode{Pane: eager}
	m.tabs = []*TabModel{{ID: "tab-1", Name: "build", Root: root}}

	got := m.tabLabel(0)
	if !strings.Contains(got, "●") {
		t.Errorf("tabLabel for a tab with an eager pane should contain ●; got %q", got)
	}
}

func TestTabLabel_NoMarkerWithoutEagerPane(t *testing.T) {
	m := Model{activeTab: 0}
	root := &LayoutNode{Pane: newTestPane("pane-1")}
	m.tabs = []*TabModel{{ID: "tab-1", Name: "build", Root: root}}

	if got := m.tabLabel(0); strings.Contains(got, "●") {
		t.Errorf("tabLabel without eager panes should not contain ●; got %q", got)
	}
}

func TestTabLabel_ActiveTab_ShowsEagerMarker(t *testing.T) {
	m := Model{activeTab: 0}
	eager := newTestPane("pane-1")
	eager.Eager = true
	root := &LayoutNode{Pane: eager}
	m.tabs = []*TabModel{{ID: "tab-1", Name: "work", Root: root}}

	got := m.tabLabel(0)
	if !strings.Contains(got, "●") {
		t.Errorf("active tab with eager pane should contain ●; got %q", got)
	}
	// Active tab also has "* " prefix
	if !strings.Contains(got, "* ") {
		t.Errorf("active tab should have '* ' prefix; got %q", got)
	}
}

func TestTabLabel_NilRoot_NoMarker(t *testing.T) {
	m := Model{activeTab: 1}
	m.tabs = []*TabModel{
		{ID: "tab-1", Name: "shell", Root: nil},
		{ID: "tab-2", Name: "other", Root: nil},
	}

	// Non-active tab with nil root should not panic and not show marker
	got := m.tabLabel(0)
	if strings.Contains(got, "●") {
		t.Errorf("tab with nil root should not contain ●; got %q", got)
	}
}

func TestTabLabel_MultiPane_OneEager_ShowsMarker(t *testing.T) {
	m := Model{activeTab: 1}
	lazy := newTestPane("pane-lazy")
	eager := newTestPane("pane-eager")
	eager.Eager = true
	root := &LayoutNode{
		Left:  &LayoutNode{Pane: lazy},
		Right: &LayoutNode{Pane: eager},
	}
	m.tabs = []*TabModel{
		{ID: "tab-1", Name: "split", Root: root},
		{ID: "tab-2", Name: "other"},
	}

	got := m.tabLabel(0)
	if !strings.Contains(got, "●") {
		t.Errorf("tab with at least one eager pane should contain ●; got %q", got)
	}
}
