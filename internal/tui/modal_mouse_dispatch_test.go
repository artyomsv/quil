package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Dispatch-level regression tests for the modal mouse swallow.
//
// TestModalSwallowsMouse (viewer_mouse_test.go) proves the PREDICATE returns
// the right bool. That is not the fix. The fix is the four
// `if m.modalSwallowsMouse() { … return }` branches sitting ahead of the
// split-border, tab-bar and scroll hit-testing in Update, and only a real
// m.Update(tea.Mouse…Msg) call exercises those. Same shape as
// ctxmenu_dispatch_test.go's TestCtxMenu_LeftClickOutsideCloses, which pins the
// neighbouring "the closing click must be swallowed, not arm a drag" rule.
//
// Each case is paired with a dialogNone control. Without it a green test proves
// nothing: if the coordinates stopped hitting the border, the swallow assertion
// would pass for the wrong reason forever.

// A press on a split border with a modal open used to arm hitTestSplitBorder,
// and the release ran finishSplitDrag — moving the ratio, resizing two PTYs and
// persisting the layout to the daemon, all behind an opaque dialog.
func TestUpdate_ModalOpen_MouseDoesNotArmSplitDrag(t *testing.T) {
	t.Parallel()
	const borderX, borderY = 50, 10

	t.Run("control: no dialog arms the drag", func(t *testing.T) {
		t.Parallel()
		m := newSplitDragTestModel(t)
		updated, _ := m.Update(tea.MouseClickMsg{X: borderX, Y: borderY, Button: tea.MouseLeft})
		if updated.(Model).splitDragNode == nil {
			t.Fatal("fixture no longer hits the split border — the swallow assertions below would pass vacuously")
		}
	})

	for _, d := range []struct {
		name   string
		dialog dialogScreen
	}{
		{"history", dialogCommandHistory},
		{"about", dialogAbout},
		{"palette", dialogCommandPalette},
	} {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			m := newSplitDragTestModel(t)
			m.dialog = d.dialog
			before := m.curTabs()[0].Root.Ratio

			updated, _ := m.Update(tea.MouseClickMsg{X: borderX, Y: borderY, Button: tea.MouseLeft})
			got := updated.(Model)
			if got.splitDragNode != nil {
				t.Fatal("press armed a split drag behind the dialog")
			}
			if got.mouseDown {
				t.Error("press armed a pane text selection behind the dialog")
			}

			// Drag and release: the ratio must not move even if a drag had been
			// armed some other way.
			updated, _ = got.Update(tea.MouseMotionMsg{X: borderX - 20, Y: borderY, Button: tea.MouseLeft})
			updated, _ = updated.(Model).Update(tea.MouseReleaseMsg{X: borderX - 20, Y: borderY, Button: tea.MouseLeft})
			afterM := updated.(Model)
			if after := afterM.curTabs()[0].Root.Ratio; after != before {
				t.Errorf("split ratio moved behind the dialog: %v → %v", before, after)
			}
		})
	}
}

// Row 0 is the tab bar: a press there reached hitTestTab and switched the
// active tab behind the modal.
func TestUpdate_ModalOpen_MouseDoesNotSwitchTabs(t *testing.T) {
	t.Parallel()
	mk := func(t *testing.T) Model {
		t.Helper()
		m := newModelForTest([]string{"one", "two", "three"}, 0)
		m.notifications = NewNotificationCenter(30, 200)
		m.width, m.height = 100, 40
		return m
	}
	// Derived, not guessed: the rendered tab carries padding the label width
	// does not include, so the first column of tab 1 is not width(label 0)+1.
	// Ask the hit-test where it actually is so restyling the bar cannot quietly
	// turn this into a click on empty space.
	clickX := -1
	for x, probe := 0, mk(t); x < probe.width; x++ {
		if probe.hitTestTab(x) == 1 {
			clickX = x
			break
		}
	}
	if clickX < 0 {
		t.Fatal("no column hit-tests to tab 1")
	}

	t.Run("control: no dialog switches the tab", func(t *testing.T) {
		t.Parallel()
		m := mk(t)
		updated, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: 0, Button: tea.MouseLeft})
		got := updated.(Model)
		if got.activeTabIdx() == 0 {
			t.Fatalf("fixture click at X=%d did not switch tabs — the swallow assertion below would pass vacuously", clickX)
		}
	})

	t.Run("dialog open holds the tab", func(t *testing.T) {
		t.Parallel()
		m := mk(t)
		m.dialog = dialogCommandHistory
		updated, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: 0, Button: tea.MouseLeft})
		got := updated.(Model)
		if got.activeTabIdx() != 0 {
			t.Errorf("active tab moved to %d behind the dialog", got.activeTabIdx())
		}
		if got.tabDragFromIdx >= 0 {
			t.Errorf("tab reorder drag armed behind the dialog (idx %d)", got.tabDragFromIdx)
		}
	})
}

// The wheel had a history-list branch but nothing stopped every OTHER modal
// from scrolling a pane's scrollback underneath it.
func TestUpdate_ModalOpen_WheelDoesNotScrollPanes(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.dialog = dialogAbout
	pane := m.curTabs()[0].ActivePaneModel()
	if pane == nil {
		t.Fatal("fixture has no active pane")
	}
	for i := 0; i < 200; i++ {
		pane.AppendOutput([]byte("filler line\r\n"))
	}
	// Control: the pane must actually be scrollable, or "did not scroll" is
	// true for the wrong reason.
	pane.ScrollUp(3)
	if pane.scrollBack == 0 {
		t.Fatal("fixture pane has no scrollback to scroll")
	}
	pane.ResetScroll()

	updated, _ := m.Update(tea.MouseWheelMsg{X: 20, Y: 10, Button: tea.MouseWheelUp})
	afterM := updated.(Model)
	if after := afterM.curTabs()[0].ActivePaneModel().scrollBack; after != 0 {
		t.Errorf("pane scrolled behind the dialog: 0 → %d", after)
	}
}
