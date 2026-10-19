package tui

import (
	"errors"
	"testing"
)

func TestPaneCopyPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		cwd, worktree       string
		wantLabel, wantPath string
	}{
		{"worktree wins over a cd'd CWD", "/w/feat-a/src/pkg", "/w/feat-a", "Copy worktree path", "/w/feat-a"},
		{"no worktree copies the CWD", "/home/u/proj", "", "Copy directory path", "/home/u/proj"},
		{"nothing known", "", "", "Copy directory path", ""},
		// A newline in a pasted path runs the line in a shell.
		{"control character refused", "/tmp/x\nrm -rf ~", "", "Copy directory path", ""},
		{"escape in worktree refused", "/ok", "/w/\x1b]52;c;x\x07", "Copy worktree path", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, path := paneCopyPath(&PaneModel{CWD: tt.cwd, WorktreePath: tt.worktree})
			if label != tt.wantLabel || path != tt.wantPath {
				t.Errorf("paneCopyPath = (%q, %q), want (%q, %q)", label, path, tt.wantLabel, tt.wantPath)
			}
		})
	}
}

func TestBuildCtxMenuItems_CopyPathGatedOnAPath(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.curTabs()[0].Root.Left.Pane

	row := func() ctxMenuItem {
		for _, it := range m.buildCtxMenuItems(pane) {
			if it.id == ctxActCopyPath {
				return it
			}
		}
		t.Fatal("no copy-path row in the pane menu")
		return ctxMenuItem{}
	}
	if row().enabled {
		t.Error("copy-path row enabled for a pane with no path")
	}
	pane.WorktreePath = "/w/feat-a"
	if it := row(); !it.enabled || it.label != "Copy worktree path" {
		t.Errorf("worktree pane: row = %+v, want an enabled \"Copy worktree path\"", it)
	}
}

// TestCopyPath_ThroughTheMenuWritesTheClipboard drives the menu dispatcher, so
// a dropped case arm fails here. Not parallel: it swaps clipboardWriteText.
func TestCopyPath_ThroughTheMenuWritesTheClipboard(t *testing.T) {
	var copied string
	var writeErr error
	prev := clipboardWriteText
	clipboardWriteText = func(s string) error { copied = s; return writeErr }
	t.Cleanup(func() { clipboardWriteText = prev })

	m := newSplitDragTestModel(t)
	pane := m.curTabs()[0].Root.Left.Pane
	pane.CWD = "/w/feat-a/src"
	pane.WorktreePath = "/w/feat-a"
	m.openCtxMenu(pane, 5, 5)

	_, cmd := m.executeCtxMenuItem(ctxMenuItem{id: ctxActCopyPath, label: "Copy worktree path", enabled: true})
	if cmd == nil {
		t.Fatal("copy path returned no command")
	}
	msg, ok := cmd().(pathCopiedMsg)
	if !ok || copied != "/w/feat-a" {
		t.Fatalf("copied %q (msg %T), want the worktree path", copied, msg)
	}

	// The result reaches the status bar: success and failure.
	next, _ := m.Update(msg)
	if got := next.(Model).flashText; got != "Copied /w/feat-a" {
		t.Errorf("flash = %q after a copy", got)
	}
	writeErr = errors.New("no clipboard tool")
	next, _ = next.(Model).Update(pathCopiedMsg{path: "/w/feat-a", err: writeErr})
	if nm := next.(Model); !nm.flashErr {
		t.Errorf("a failed copy did not raise an error flash (flash %q)", nm.flashText)
	}
}
