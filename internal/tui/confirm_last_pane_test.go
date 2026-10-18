package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const lastPaneNeedle = "the tab closes too"

// Issue #262: the daemon closes a tab with its last pane, and the Ctrl+W
// confirm says so before the user presses Enter.
func TestRenderConfirmDialog_LastPaneSaysTheTabCloses(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-only"})
	m = applyModel(m.openClosePaneConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if !strings.Contains(out, lastPaneNeedle) {
		t.Errorf("closing a tab's only pane does not say the tab closes:\n%s", out)
	}
}

func TestRenderConfirmDialog_PaneWithSiblingsSaysNothingAboutTheTab(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-a"}, &PaneModel{ID: "pane-b"})
	m = applyModel(m.openClosePaneConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if strings.Contains(out, lastPaneNeedle) {
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

	if strings.Contains(out, lastPaneNeedle) {
		t.Errorf("the notice survived a sibling arriving:\n%s", out)
	}
}

// The daemon keeps a tab a worktree add is still filling, so the confirm must
// not promise a close there. Covers this client's own replace, its own create,
// and a preparing leaf another client's add put in the tab.
func TestRenderConfirmDialog_TabInFlightMakesNoPromise(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(m *Model, pane *PaneModel)
	}{
		{"own worktree replace", func(m *Model, _ *PaneModel) {
			id := m.projects[0].tabs[0].ID
			m.worktreeReplaced = map[string]*PaneModel{id: {ID: "pane-held"}}
		}},
		{"own worktree create", func(m *Model, _ *PaneModel) {
			id := m.projects[0].tabs[0].ID
			m.worktreeCreates = map[string]string{id: "feat/a"}
		}},
		{"preparing leaf", func(_ *Model, pane *PaneModel) {
			pane.PreparingWorktree = "feat/a"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := &PaneModel{ID: "pane-only"}
			m := modelWithPanes(pane)
			m = applyModel(m.openClosePaneConfirm())
			tc.arm(&m, pane)

			out := stripANSI(m.renderConfirmDialog())

			if strings.Contains(out, lastPaneNeedle) {
				t.Errorf("a tab in flight promises a close the daemon will not make:\n%s", out)
			}
		})
	}
}

// The tab close confirm is unchanged.
func TestRenderConfirmDialog_TabCloseHasNoPaneNotice(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-only"})
	m = applyModel(m.openCloseTabConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if strings.Contains(out, lastPaneNeedle) {
		t.Errorf("the tab close confirm carries the pane notice:\n%s", out)
	}
}

// Through renderDialog, not renderConfirmDialog: the wrap happens in the box,
// after the content is built. At the smallest supported terminal the notice
// must neither wrap nor push the box past the bottom edge — lipgloss.Place
// clips neither.
func TestConfirmDialog_LastPaneFitsTheSmallestTerminal(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-only", Name: "x"})
	m.width, m.height = minTermWidth, minTermHeight
	m = applyModel(m.openClosePaneConfirm())

	frame := m.renderDialog()
	lines := strings.Split(frame, "\n")

	if len(lines) > minTermHeight {
		t.Errorf("the dialog is %d rows on a %d-row terminal:\n%s", len(lines), minTermHeight, stripANSI(frame))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > minTermWidth {
			t.Errorf("row %d is %d cells on a %d-cell terminal: %q", i, w, minTermWidth, stripANSI(l))
		}
	}
	if !strings.Contains(stripANSI(frame), lastPaneNotice) {
		t.Errorf("the notice was cut or wrapped at %dx%d:\n%s", minTermWidth, minTermHeight, stripANSI(frame))
	}
}
