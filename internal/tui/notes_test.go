package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/aethel/internal/persist"
)

func TestNewNotesEditor_LoadsExistingFile(t *testing.T) {
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
	if got := ne.editor.Content(); got != want {
		t.Errorf("loaded content mismatch:\n got: %q\nwant: %q", got, want)
	}
	if ne.Dirty() {
		t.Error("editor should not be dirty immediately after load")
	}
}

func TestNewNotesEditor_MissingFile_StartsEmpty(t *testing.T) {
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-fresh", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	if got := ne.editor.Content(); got != "" {
		t.Errorf("fresh editor content = %q, want empty", got)
	}
	if ne.Dirty() {
		t.Error("fresh editor should not be dirty")
	}
}

func TestNewNotesEditor_RequiresPaneID(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewNotesEditor(dir, "", "Name", 40, 10); err == nil {
		t.Error("expected error for empty pane ID")
	}
}

func TestNotesEditor_UsesPlainHighlight(t *testing.T) {
	ne, err := NewNotesEditor(t.TempDir(), "pane-plain", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	if got := ne.editor.Highlight; got != "plain" {
		t.Errorf("editor.Highlight = %q, want %q", got, "plain")
	}
}

func TestNotesEditor_Save_CleanNoop(t *testing.T) {
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

	// Ctrl+S saves and returns the saved action.
	action, _ := ne.HandleKey("ctrl+s")
	if action != notesActionSaved {
		t.Errorf("ctrl+s action = %v, want saved", action)
	}
	if ne.Dirty() {
		t.Error("editor should be clean after save")
	}

	// File on disk should contain the typed content.
	path, _ := persist.NotesPath(dir, "pane-type")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if got := string(data); got != "abc" {
		t.Errorf("saved content = %q, want %q", got, "abc")
	}
}

func TestNotesEditor_EscExits(t *testing.T) {
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
	if got != "xy" {
		t.Errorf("persisted = %q, want %q", got, "xy")
	}
}

func TestNotesEditor_MaybeAutoSave_RespectsDebounceWindow(t *testing.T) {
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-debounce", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("z")
	// Fresh edit — within the debounce window, nothing should happen.
	if ne.MaybeAutoSave() {
		t.Error("MaybeAutoSave should not save within debounce window")
	}
	// Simulate elapsed debounce window and retry.
	ne.lastEditAt = time.Now().Add(-notesDebounceWindow - time.Second)
	if !ne.MaybeAutoSave() {
		t.Error("MaybeAutoSave should save once debounce window elapses")
	}
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

func TestNotesEditor_PaneID_And_SaveErr_NilSafe(t *testing.T) {
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
	if err := ne.Close(); err != nil {
		t.Errorf("nil Close should return nil, got %v", err)
	}
}
