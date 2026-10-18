package tui

import (
	"strings"
	"testing"
)

const lastPaneNotice = "the tab closes too"

// Issue #262: the daemon closes a tab with its last pane, and the Ctrl+W
// confirm says so before the user presses Enter.
func TestRenderConfirmDialog_LastPaneSaysTheTabCloses(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-only"})
	m = applyModel(m.openClosePaneConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if !strings.Contains(out, lastPaneNotice) {
		t.Errorf("closing a tab's only pane does not say the tab closes:\n%s", out)
	}
}

func TestRenderConfirmDialog_PaneWithSiblingsSaysNothingAboutTheTab(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-a"}, &PaneModel{ID: "pane-b"})
	m = applyModel(m.openClosePaneConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if strings.Contains(out, lastPaneNotice) {
		t.Errorf("a pane with a sibling claims the tab closes:\n%s", out)
	}
}

// Read at render: a sibling that arrives while the dialog is open (another
// client's split, an MCP create) removes the notice.
func TestRenderConfirmDialog_LastPaneNoticeFollowsTheTab(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-only"})
	m = applyModel(m.openClosePaneConfirm())
	tab := m.projects[0].tabs[0]
	tab.Root = tabWith(&PaneModel{ID: "pane-only"}, &PaneModel{ID: "pane-new"}).Root
	tab.invalidateLeaves()

	out := stripANSI(m.renderConfirmDialog())

	if strings.Contains(out, lastPaneNotice) {
		t.Errorf("the notice survived a sibling arriving:\n%s", out)
	}
}

// The tab close confirm is unchanged.
func TestRenderConfirmDialog_TabCloseHasNoPaneNotice(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-only"})
	m = applyModel(m.openCloseTabConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if strings.Contains(out, lastPaneNotice) {
		t.Errorf("the tab close confirm carries the pane notice:\n%s", out)
	}
}
