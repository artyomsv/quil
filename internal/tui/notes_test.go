package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/aethel/internal/persist"
)

func TestNewNotesEditor_LoadsExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-load"
	want := "existing content\nline two\n"
	if err := persist.SaveNotes(dir, paneID, want); err != nil {
		t.Fatalf("seed notes: %v", err)
	}

	ne, err := NewNotesEditor(dir, paneID, "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	if got := ne.Content(); got != want {
		t.Errorf("loaded content mismatch:\n got: %q\nwant: %q", got, want)
	}
	if ne.Dirty() {
		t.Error("editor should not be dirty immediately after load")
	}
}

func TestNewNotesEditor_MissingFile_StartsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-fresh", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	if got := ne.Content(); got != "" {
		t.Errorf("fresh editor content = %q, want empty", got)
	}
	if ne.Dirty() {
		t.Error("fresh editor should not be dirty")
	}
}

func TestNewNotesEditor_RequiresPaneID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := NewNotesEditor(dir, "", "Name", 40, 10); err == nil {
		t.Error("expected error for empty pane ID")
	}
}

func TestNotesEditor_UsesPlainHighlight(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-plain", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	if got := ne.editor.Highlight; got != HighlightPlain {
		t.Errorf("editor.Highlight = %v, want HighlightPlain", got)
	}
}

func TestNotesEditor_Save_CleanNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-clean", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	if err := ne.Save(); err != nil {
		t.Errorf("Save on clean editor should not error: %v", err)
	}
	// No file should have been created.
	path, _ := persist.NotesPath(dir, "pane-clean")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no notes file for a clean save, stat err: %v", err)
	}
}

func TestNotesEditor_HandleKey_InsertMarksDirtyAndPersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-type", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}

	// Simulate typing "abc" via successive character key presses.
	for _, key := range []string{"a", "b", "c"} {
		if action, _ := ne.HandleKey(key); action != notesActionNone {
			t.Errorf("HandleKey %q action = %v, want none", key, action)
		}
	}
	if !ne.Dirty() {
		t.Error("editor should be dirty after typing")
	}

	// Ctrl+S saves; the action stays None (the editor remains open).
	action, _ := ne.HandleKey("ctrl+s")
	if action != notesActionNone {
		t.Errorf("ctrl+s action = %v, want none", action)
	}
	if ne.Dirty() {
		t.Error("editor should be clean after save")
	}

	// File on disk should contain the typed content with a trailing newline.
	path, _ := persist.NotesPath(dir, "pane-type")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if got := string(data); got != "abc\n" {
		t.Errorf("saved content = %q, want %q", got, "abc\n")
	}
}

// Regression test for the save loop bug: after a successful save, a
// non-mutating cursor move must NOT re-mark the wrapper dirty.
func TestNotesEditor_NoDirtyAfterSavePlusCursorMove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-dirty-bug", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("h")
	ne.HandleKey("i")
	if _, _ = ne.HandleKey("ctrl+s"); ne.Dirty() {
		t.Fatal("editor should be clean after save")
	}
	// Now press a non-mutating navigation key. The wrapper should stay clean.
	for _, key := range []string{"left", "right", "home", "end", "up", "down"} {
		ne.HandleKey(key)
		if ne.Dirty() {
			t.Errorf("editor should still be clean after %q (save loop regression)", key)
			return
		}
	}
}

func TestNotesEditor_EscExits(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-esc", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	action, _ := ne.HandleKey("esc")
	if action != notesActionExit {
		t.Errorf("esc action = %v, want exit", action)
	}
}

func TestNotesEditor_EscClearsSelectionFirst(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-sel", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"hello"}
	ne.editor.selectAll()

	action, _ := ne.HandleKey("esc")
	if action != notesActionNone {
		t.Errorf("first esc with selection action = %v, want none", action)
	}
	if ne.editor.Sel != nil {
		t.Error("first esc should clear selection")
	}

	action, _ = ne.HandleKey("esc")
	if action != notesActionExit {
		t.Errorf("second esc action = %v, want exit", action)
	}
}

func TestNotesEditor_Close_FlushesDirty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-close", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("x")
	ne.HandleKey("y")
	if err := ne.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ne.Dirty() {
		t.Error("editor should be clean after Close")
	}
	got, err := persist.LoadNotes(dir, "pane-close")
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != "xy\n" {
		t.Errorf("persisted = %q, want %q", got, "xy\n")
	}
}

func TestNotesEditor_ContentSurvivesCloseAndReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-roundtrip"
	original, err := NewNotesEditor(dir, paneID, "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	for _, key := range []string{"f", "o", "o"} {
		original.HandleKey(key)
	}
	if err := original.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewNotesEditor(dir, paneID, "Build", 40, 10)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Content(); got != "foo\n" {
		t.Errorf("reopened content = %q, want %q", got, "foo\n")
	}
}

func TestNotesEditor_MaybeAutoSave_RespectsDebounceWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-debounce", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("z")
	// Fresh edit — within the debounce window, no save.
	ne.MaybeAutoSave()
	if !ne.Dirty() {
		t.Error("MaybeAutoSave should not save within debounce window")
	}
	// Simulate elapsed debounce window and retry.
	ne.lastEditAt = time.Now().Add(-notesDebounceWindow - time.Second)
	ne.MaybeAutoSave()
	if ne.Dirty() {
		t.Error("editor should be clean after debounce save")
	}

	// Confirm the file exists on disk.
	path, _ := persist.NotesPath(dir, "pane-debounce")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected saved file, stat err: %v", err)
	}
	// Sanity: the directory only contains the single notes file, not a stray tmp.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("expected 1 entry in notes dir, got %d", len(entries))
	}
}

func TestNotesEditor_HandlePaste_MarksDirty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-paste", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandlePaste("hello\nworld")
	if !ne.Dirty() {
		t.Error("editor should be dirty after paste")
	}
	if got := ne.Content(); !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("editor content after paste = %q, expected both lines", got)
	}
	if err := ne.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	saved, _ := persist.LoadNotes(dir, "pane-paste")
	if !strings.HasSuffix(saved, "\n") {
		t.Errorf("saved file should end with newline, got %q", saved)
	}
}

func TestNotesEditor_HandlePaste_EmptyNoop(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-empty-paste", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandlePaste("")
	if ne.Dirty() {
		t.Error("empty paste should not mark dirty")
	}
}

func TestNotesEditor_SaveErr_PopulatedOnFailure(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	// Create a regular file where we expect a directory; SaveNotes will
	// fail to create the dir and the wrapper should record the error.
	notesPath := filepath.Join(parent, "blocked")
	if err := os.WriteFile(notesPath, []byte("not a dir"), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ne, err := NewNotesEditor(notesPath, "pane-err", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("a")
	if err := ne.Save(); err == nil {
		t.Error("Save should have failed because notes dir is a regular file")
	}
	if !ne.Dirty() {
		t.Error("editor should remain dirty after a failed save")
	}
	if ne.SaveErr() == "" {
		t.Error("SaveErr should be populated after a failed save")
	}
}

func TestNotesEditor_PaneID_And_SaveErr_NilSafe(t *testing.T) {
	t.Parallel()
	var ne *NotesEditor
	if got := ne.PaneID(); got != "" {
		t.Errorf("nil PaneID = %q, want empty", got)
	}
	if got := ne.SaveErr(); got != "" {
		t.Errorf("nil SaveErr = %q, want empty", got)
	}
	if ne.Dirty() {
		t.Error("nil Dirty should be false")
	}
	if got := ne.Content(); got != "" {
		t.Errorf("nil Content = %q, want empty", got)
	}
	if err := ne.Close(); err != nil {
		t.Errorf("nil Close should return nil, got %v", err)
	}
	// MaybeAutoSave on nil should not panic.
	ne.MaybeAutoSave()
}

func TestTextEditor_HighlightPlain_ReturnsLineUnchanged(t *testing.T) {
	t.Parallel()
	plain := &TextEditor{Highlight: HighlightPlain}
	in := `# heading [section] key = "value"`
	if got := plain.highlight(in); got != in {
		t.Errorf("plain highlight = %q, want unchanged %q", got, in)
	}

	tomlEd := &TextEditor{Highlight: HighlightTOML}
	if got := tomlEd.highlight(in); got == in {
		t.Errorf("toml highlight should add ANSI codes, got %q", got)
	}
}
