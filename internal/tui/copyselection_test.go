package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// These swap the package-level clipboardWriteText seam and pin the click
// clock, so they are not parallel.

// captureCopies points clipboardWriteText at a recorder for the test.
func captureCopies(t *testing.T) *[]string {
	t.Helper()
	var copied []string
	prev := clipboardWriteText
	clipboardWriteText = func(s string) error { copied = append(copied, s); return nil }
	t.Cleanup(func() { clipboardWriteText = prev })
	return &copied
}

// runCopy runs cmd, which must be a copy, and returns what it reports.
func runCopy(t *testing.T, cmd tea.Cmd) textCopiedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no copy command")
	}
	got := cmd()
	msg, ok := got.(textCopiedMsg)
	if !ok {
		t.Fatalf("copy command returned %T (%+v), want textCopiedMsg", got, got)
	}
	return msg
}

// runIfAny runs cmd when there is one, for paths that must NOT copy.
func runIfAny(cmd tea.Cmd) {
	if cmd != nil {
		cmd()
	}
}

func copyTestModel(copyOnSelect bool) Model {
	m := pasteTestModel(&fakeSender{})
	m.cfg.UI.CopyOnSelect = copyOnSelect
	m.curTabs()[0].ActivePaneModel().AppendOutput([]byte("echo hello-world done"))
	return m
}

// dragSelect drags across row 0 of the active pane from col a to col b and
// returns the model and the release's command.
func dragSelect(t *testing.T, m Model, a, b int) (Model, tea.Cmd) {
	t.Helper()
	x0, y := paneCellScreen(m, a, 0)
	x1, _ := paneCellScreen(m, b, 0)
	next, _ := m.Update(tea.MouseClickMsg{X: x0, Y: y, Button: tea.MouseLeft})
	next, _ = next.(Model).Update(tea.MouseMotionMsg{X: x1, Y: y, Button: tea.MouseLeft})
	next, cmd := next.(Model).Update(tea.MouseReleaseMsg{X: x1, Y: y, Button: tea.MouseLeft})
	m = next.(Model)
	if m.selection == nil {
		t.Fatal("setup: the drag made no selection")
	}
	return m, cmd
}

func TestCopyOnSelect_DefaultsOff(t *testing.T) {
	if config.Default().UI.CopyOnSelect {
		t.Fatal("copy_on_select must default off: it changes the established select-then-copy gesture")
	}
}

func TestCopyOnSelect_PaneDragRelease(t *testing.T) {
	t.Run("off: the release copies nothing", func(t *testing.T) {
		copied := captureCopies(t)
		_, cmd := dragSelect(t, copyTestModel(false), 0, 3)
		runIfAny(cmd)
		if len(*copied) != 0 {
			t.Errorf("copied %q with copy_on_select off", *copied)
		}
	})
	t.Run("on: the release copies and keeps the selection", func(t *testing.T) {
		copied := captureCopies(t)
		m, cmd := dragSelect(t, copyTestModel(true), 0, 3)
		if msg := runCopy(t, cmd); msg.text != "echo" || msg.err != nil {
			t.Errorf("copy = %+v, want %q", msg, "echo")
		}
		if len(*copied) != 1 || (*copied)[0] != "echo" {
			t.Errorf("clipboard got %q, want [echo]", *copied)
		}
		if m.selection == nil {
			t.Error("the selection was dropped; copy-on-select should leave it showing")
		}
	})
}

func TestCopyOnSelect_PaneDoubleAndTripleClick(t *testing.T) {
	pinClickClock(t)
	for _, on := range []bool{false, true} {
		copied := captureCopies(t)
		m := copyTestModel(on)
		x, y := paneCellScreen(m, 8, 0) // inside "hello-world"
		m = clickAt(m, x, y)

		// The second press selects the word; the copy rides on that press.
		next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		m = next.(Model)
		if on {
			if msg := runCopy(t, cmd); msg.text != "hello-world" {
				t.Errorf("double click copied %q, want %q", msg.text, "hello-world")
			}
		} else {
			runIfAny(cmd)
		}
		next, _ = m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
		m = next.(Model)

		next, cmd = m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		if on {
			if msg := runCopy(t, cmd); msg.text != "echo hello-world done" {
				t.Errorf("triple click copied %q, want the sentence", msg.text)
			}
		} else {
			runIfAny(cmd)
			if len(*copied) != 0 {
				t.Errorf("copied %q with copy_on_select off", *copied)
			}
		}
		_ = next
	}
}

func TestRightClick_WithSelection(t *testing.T) {
	t.Run("off: copies the selection, as before", func(t *testing.T) {
		captureCopies(t)
		m, _ := dragSelect(t, copyTestModel(false), 0, 3)
		x, y := paneCellScreen(m, 1, 0)
		next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
		m = next.(Model)
		if msg := runCopy(t, cmd); msg.text != "echo" {
			t.Errorf("right-click copied %q, want %q", msg.text, "echo")
		}
		if m.ctxMenu.open() {
			t.Error("right-click with a selection opened the menu with copy_on_select off")
		}
	})
	t.Run("on: opens the context menu instead of copying again", func(t *testing.T) {
		copied := captureCopies(t)
		m, cmd := dragSelect(t, copyTestModel(true), 0, 3)
		runIfAny(cmd) // the copy-on-select copy
		*copied = nil
		x, y := paneCellScreen(m, 1, 0)
		next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
		m = next.(Model)
		runIfAny(cmd)
		if len(*copied) != 0 {
			t.Errorf("right-click copied %q again", *copied)
		}
		if m.selection != nil {
			t.Error("right-click kept the selection")
		}
		if !m.ctxMenu.open() {
			t.Error("right-click did not open the pane context menu")
		}
	})
}

func TestEnterCopy_ReportsThroughTextCopiedMsg(t *testing.T) {
	captureCopies(t)
	m, _ := dragSelect(t, copyTestModel(false), 0, 3)
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if msg := runCopy(t, cmd); msg.text != "echo" {
		t.Errorf("Enter copied %q, want %q", msg.text, "echo")
	}
	if next.(Model).selection != nil {
		t.Error("Enter kept the selection")
	}
}

func TestCopyOnSelect_NotesDragRelease(t *testing.T) {
	for _, on := range []bool{false, true} {
		copied := captureCopies(t)
		ne, err := NewNotesEditor(t.TempDir(), "pane-cos", "Shell", 40, 10)
		if err != nil {
			t.Fatalf("NewNotesEditor: %v", err)
		}
		ne.editor.Lines = []string{"alpha", "beta"}
		ne.BeginSelection(0, 0)
		ne.ExtendSelection(1, 4)
		m := newNotesTestModel(t, ne)
		m.cfg.UI.CopyOnSelect = on
		m.tabDragFromIdx = -1 // NewModel's "no tab drag"; a bare Model has 0
		m.notesMouseDown = true

		next, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft})
		if on {
			if msg := runCopy(t, cmd); msg.text != "alpha\nbeta" {
				t.Errorf("notes drag copied %q, want %q", msg.text, "alpha\nbeta")
			}
			if !next.(Model).notesEditor.HasSelection() {
				t.Error("copy-on-select dropped the notes selection")
			}
		} else {
			runIfAny(cmd)
			if len(*copied) != 0 {
				t.Errorf("notes drag copied %q with copy_on_select off", *copied)
			}
		}
	}
}

func TestCopyOnSelect_ViewerDragRelease(t *testing.T) {
	for _, on := range []bool{false, true} {
		copied := captureCopies(t)
		m := viewerModel(t, "alpha\nbravo", 80, 24, false)
		m.cfg.UI.CopyOnSelect = on
		m.tomlEditor.beginSelectionAt(0, 0)
		m.tomlEditor.extendSelectionAt(0, 5)
		m.viewerMouseDown = true

		_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft})
		if on {
			if msg := runCopy(t, cmd); msg.text != "alpha" {
				t.Errorf("viewer drag copied %q, want %q", msg.text, "alpha")
			}
		} else {
			runIfAny(cmd)
			if len(*copied) != 0 {
				t.Errorf("viewer drag copied %q with copy_on_select off", *copied)
			}
		}
	}
}

// A release that ends no drag must not copy a selection made some other way
// (the keyboard), even with copy_on_select on.
func TestCopyOnSelect_ViewerReleaseWithoutDragDoesNotCopy(t *testing.T) {
	copied := captureCopies(t)
	m := viewerModel(t, "alpha\nbravo", 80, 24, false)
	m.cfg.UI.CopyOnSelect = true
	m.tomlEditor.beginSelectionAt(0, 0)
	m.tomlEditor.extendSelectionAt(0, 5)
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft})
	runIfAny(cmd)
	if len(*copied) != 0 {
		t.Errorf("a stray release copied %q", *copied)
	}
}

func TestTextCopiedMsg_Flashes(t *testing.T) {
	m := copyTestModel(false)
	next, cmd := m.Update(textCopiedMsg{text: "one\ntwo\n"})
	got := next.(Model)
	if got.flashText != "Copied 2 lines" || got.flashErr || cmd == nil {
		t.Errorf("flash = %q (err %v, cmd %v), want %q", got.flashText, got.flashErr, cmd != nil, "Copied 2 lines")
	}

	next, _ = m.Update(textCopiedMsg{text: "x", err: errors.New("no clipboard tool found")})
	got = next.(Model)
	if !got.flashErr || !strings.Contains(got.flashText, "could not copy") {
		t.Errorf("error flash = %q (err %v), want a copy failure", got.flashText, got.flashErr)
	}
}

func TestCopiedFlash(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]string{
		"x":            "Copied 1 character",
		"hello-wörld":  "Copied 11 characters",
		"one\ntwo":     "Copied 2 lines",
		"one\ntwo\n":   "Copied 2 lines",
		"a\nb\nc\nd\n": "Copied 4 lines",
		"single\n":     "Copied 6 characters",
	} {
		if got := copiedFlash(text); got != want {
			t.Errorf("copiedFlash(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestCopyTextCmd_EmptyCopiesNothing(t *testing.T) {
	if copyTextCmd("", "pane") != nil {
		t.Error("an empty copy returned a command")
	}
}

// twoPaneCopyModel splits the test tab into left "p1" (focused) and right "p2",
// sizes each VT to its box and returns the right pane's content origin.
func twoPaneCopyModel(t *testing.T, copyOnSelect bool) (m Model, rightX, rightY int) {
	t.Helper()
	m = pasteTestModel(&fakeSender{})
	m.cfg.UI.CopyOnSelect = copyOnSelect
	tab := m.curTabs()[0]
	ph := tab.Root.SplitLeaf("p1", SplitHorizontal)
	ph.fill(NewPaneModel("p2", 1024))
	var rects []PaneRect
	tab.Root.CollectRects(m.projectSidebarWidth(), 1, m.paneAreaWidth(), m.height-chromeHeight, &rects)
	for _, r := range rects {
		r.Pane.ResizeVT(r.W-2, r.H-2)
		if r.Pane.ID == "p1" {
			r.Pane.AppendOutput([]byte("AAAA"))
		} else {
			r.Pane.AppendOutput([]byte("bbbb"))
			rightX, rightY = r.OX+1, r.OY+1
		}
	}
	if tab.ActivePane != "p1" {
		t.Fatalf("setup: focused pane = %q, want p1", tab.ActivePane)
	}
	return m, rightX, rightY
}

// A drag in a pane without focus selects there without moving focus; the
// copy must come from that pane, not the focused one.
func TestCopy_SelectionInUnfocusedPaneCopiesThatPane(t *testing.T) {
	// Pressed at column 3 and dragged back to column 0: the pane's first
	// column sits in the split border's grab zone, so a press there would
	// arm a border resize instead of a selection.
	drag := func(m Model, x, y int) (Model, tea.Cmd) {
		next, _ := m.Update(tea.MouseClickMsg{X: x + 3, Y: y, Button: tea.MouseLeft})
		next, _ = next.(Model).Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
		next, cmd := next.(Model).Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
		got := next.(Model)
		if got.selection == nil || got.selection.PaneID != "p2" {
			t.Fatalf("setup: the drag did not select in p2: %+v", got.selection)
		}
		return got, cmd
	}
	t.Run("copy on select", func(t *testing.T) {
		captureCopies(t)
		m, x, y := twoPaneCopyModel(t, true)
		_, cmd := drag(m, x, y)
		if msg := runCopy(t, cmd); msg.text != "bbbb" {
			t.Errorf("copied %q, want the dragged pane's %q", msg.text, "bbbb")
		}
	})
	t.Run("right-click", func(t *testing.T) {
		captureCopies(t)
		m, x, y := twoPaneCopyModel(t, false)
		m, _ = drag(m, x, y)
		_, cmd := m.Update(tea.MouseClickMsg{X: x + 2, Y: y, Button: tea.MouseRight})
		if msg := runCopy(t, cmd); msg.text != "bbbb" {
			t.Errorf("copied %q, want the dragged pane's %q", msg.text, "bbbb")
		}
	})
}

func TestCopyOnSelect_SameCellJitterDoesNotCopy(t *testing.T) {
	copied := captureCopies(t)
	m := copyTestModel(true)
	x, y := paneCellScreen(m, 2, 0)
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	next, _ = next.(Model).Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := next.(Model).Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	runIfAny(cmd)
	if len(*copied) != 0 {
		t.Errorf("a click with same-cell jitter copied %q", *copied)
	}
}

func TestCopyOnSelect_ReleaseAfterDoubleClickDoesNotCopyAgain(t *testing.T) {
	pinClickClock(t)
	captureCopies(t)
	m := copyTestModel(true)
	x, y := paneCellScreen(m, 8, 0)
	m = clickAt(m, x, y)
	next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	runCopy(t, cmd)
	if _, cmd := next.(Model).Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}); cmd != nil {
		t.Error("the release after a double-click returned a second copy")
	}
}

// With copy_on_select on, right-click opens the menu only for a selection
// it copied. One it did not copy must still be copied, never dropped.
func TestCopyOnSelect_RightClickCopiesWhatItDidNotCopy(t *testing.T) {
	t.Run("a selection changed by the keyboard", func(t *testing.T) {
		captureCopies(t)
		m, cmd := dragSelect(t, copyTestModel(true), 0, 3)
		runCopy(t, cmd)
		if !m.selection.copied {
			t.Fatal("setup: the drag's selection is not marked copied")
		}
		next, _ := m.handleSelectionKey("shift+right")
		m = next.(Model)
		if m.selection.copied {
			t.Fatal("a keyboard change left the selection marked copied")
		}
		x, y := paneCellScreen(m, 1, 0)
		next, cmd = m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
		if msg := runCopy(t, cmd); !strings.HasPrefix(msg.text, "echo") {
			t.Errorf("right-click copied %q, want the changed selection", msg.text)
		}
		if next.(Model).ctxMenu.open() {
			t.Error("right-click opened the menu instead of copying")
		}
	})
	t.Run("notes mode, where no menu opens", func(t *testing.T) {
		captureCopies(t)
		m, cmd := dragSelect(t, copyTestModel(true), 0, 3)
		runCopy(t, cmd)
		m.notesMode = true
		x, y := paneCellScreen(m, 1, 0)
		_, cmd = m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
		if msg := runCopy(t, cmd); msg.text != "echo" {
			t.Errorf("right-click in notes mode copied %q, want %q", msg.text, "echo")
		}
	})
}

func TestSettingsRow_CopyOnSelectToggles(t *testing.T) {
	m := &Model{cfg: config.Default()}
	var row settingsField
	for _, f := range settingsFields() {
		if f.label == "Copy on select" {
			row = f
		}
	}
	if row.set == nil || !row.isBool {
		t.Fatal("no Copy on select toggle row")
	}
	if got := row.get(m); got != boolStr(false) {
		t.Errorf("default shows %q", got)
	}
	row.set(m, "")
	if !m.cfg.UI.CopyOnSelect || !m.configChanged {
		t.Errorf("toggle on: CopyOnSelect=%v configChanged=%v", m.cfg.UI.CopyOnSelect, m.configChanged)
	}
	row.set(m, "")
	if m.cfg.UI.CopyOnSelect {
		t.Error("toggle off left CopyOnSelect on")
	}
}
