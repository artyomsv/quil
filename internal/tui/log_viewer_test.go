package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyEditor_SuppressesEdits(t *testing.T) {
	editor := NewTextEditor("hello\nworld\n", "/tmp/log.txt", 80, 10)
	editor.ReadOnly = true
	editor.Highlight = HighlightPlain

	originalLines := append([]string(nil), editor.Lines...)
	originalRow := editor.CursorRow
	originalCol := editor.CursorCol

	// Try several mutating keys; none should change the document.
	for _, key := range []string{"a", "tab", "space", "enter", "backspace", "delete"} {
		editor.HandleKey(key)
	}
	if !equalLines(editor.Lines, originalLines) {
		t.Errorf("ReadOnly editor mutated by typing: original=%v after=%v", originalLines, editor.Lines)
	}

	// Cursor movement should still work.
	editor.CursorRow = originalRow
	editor.CursorCol = originalCol
	editor.HandleKey("right")
	if editor.CursorCol != originalCol+1 {
		t.Errorf("cursor right didn't advance: col=%d", editor.CursorCol)
	}
}

func TestReadOnlyEditor_SaveIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	originalContent := "do not overwrite me\n"
	if err := os.WriteFile(path, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	editor := NewTextEditor("clobbered content\n", path, 80, 10)
	editor.ReadOnly = true

	saved, _, _ := editor.HandleKey("ctrl+s")
	if saved {
		t.Error("ReadOnly editor reported saved=true on Ctrl+S")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != originalContent {
		t.Errorf("file was overwritten by Ctrl+S in ReadOnly mode:\ngot:  %q\nwant: %q", string(got), originalContent)
	}
}

func TestReadOnlyEditor_InsertMultiLineNoOp(t *testing.T) {
	editor := NewTextEditor("a\nb\n", "/tmp/x", 80, 10)
	editor.ReadOnly = true

	originalLines := append([]string(nil), editor.Lines...)
	editor.InsertMultiLine("INJECTED\nCONTENT")
	if !equalLines(editor.Lines, originalLines) {
		t.Errorf("InsertMultiLine mutated ReadOnly editor: %v", editor.Lines)
	}
}

// editorWithLines builds a TextEditor with `n` lines named "line0", "line1", ...
// for testing cursor jump distances.
func editorWithLines(n int) *TextEditor {
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	content := strings.Join(lines, "\n")
	return NewTextEditor(content, "/tmp/x", 80, 10)
}

func TestEditor_AltDown_JumpsByPageSize(t *testing.T) {
	editor := editorWithLines(200)
	editor.PageSize = 40
	editor.CursorRow = 10

	editor.HandleKey("alt+down")
	if editor.CursorRow != 50 {
		t.Errorf("alt+down: cursor at %d, want 50 (10 + 40)", editor.CursorRow)
	}
}

func TestEditor_AltUp_JumpsByPageSize(t *testing.T) {
	editor := editorWithLines(200)
	editor.PageSize = 40
	editor.CursorRow = 100

	editor.HandleKey("alt+up")
	if editor.CursorRow != 60 {
		t.Errorf("alt+up: cursor at %d, want 60 (100 - 40)", editor.CursorRow)
	}
}

func TestEditor_AltDown_ClampsToLastLine(t *testing.T) {
	editor := editorWithLines(50)
	editor.PageSize = 40
	editor.CursorRow = 30

	editor.HandleKey("alt+down")
	if editor.CursorRow != 49 {
		t.Errorf("alt+down past end: cursor at %d, want 49 (last line)", editor.CursorRow)
	}
}

func TestEditor_AltUp_ClampsToFirstLine(t *testing.T) {
	editor := editorWithLines(50)
	editor.PageSize = 40
	editor.CursorRow = 10

	editor.HandleKey("alt+up")
	if editor.CursorRow != 0 {
		t.Errorf("alt+up past start: cursor at %d, want 0", editor.CursorRow)
	}
}

func TestEditor_AltDown_ZeroPageSizeUsesDefault(t *testing.T) {
	editor := editorWithLines(200)
	editor.PageSize = 0 // unset → should fall back to editorDefaultPageSize (40)
	editor.CursorRow = 0

	editor.HandleKey("alt+down")
	if editor.CursorRow != editorDefaultPageSize {
		t.Errorf("alt+down with PageSize=0: cursor at %d, want %d (default)",
			editor.CursorRow, editorDefaultPageSize)
	}
}

func TestEditor_AltDown_CustomPageSize(t *testing.T) {
	editor := editorWithLines(500)
	editor.PageSize = 100
	editor.CursorRow = 50

	editor.HandleKey("alt+down")
	if editor.CursorRow != 150 {
		t.Errorf("alt+down with PageSize=100: cursor at %d, want 150", editor.CursorRow)
	}
}

func TestEditor_AltUp_ReadOnlyAllowed(t *testing.T) {
	// Alt+Up/Down are pure navigation — must work even when ReadOnly is on.
	editor := editorWithLines(200)
	editor.ReadOnly = true
	editor.PageSize = 40
	editor.CursorRow = 100

	editor.HandleKey("alt+up")
	if editor.CursorRow != 60 {
		t.Errorf("alt+up in ReadOnly mode: cursor at %d, want 60", editor.CursorRow)
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
