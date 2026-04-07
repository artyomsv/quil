package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/persist"
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

func TestNotesEditor_SetCursor_ClampsAndClearsSelection(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-cursor", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"hello", "world!"}
	ne.editor.Sel = &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 0},
		Cursor: EditorPos{Row: 1, Col: 6},
	}

	// Out-of-bounds row + col is clamped to the last line / line length.
	ne.SetCursor(99, 99)
	if ne.editor.CursorRow != 1 {
		t.Errorf("CursorRow = %d, want 1", ne.editor.CursorRow)
	}
	if ne.editor.CursorCol != 6 {
		t.Errorf("CursorCol = %d, want 6", ne.editor.CursorCol)
	}
	if ne.editor.Sel != nil {
		t.Error("SetCursor should clear any active selection")
	}

	// Negative coordinates clamp to (0, 0).
	ne.SetCursor(-5, -5)
	if ne.editor.CursorRow != 0 || ne.editor.CursorCol != 0 {
		t.Errorf("clamped negative = (%d, %d), want (0, 0)", ne.editor.CursorRow, ne.editor.CursorCol)
	}
}

func TestNotesEditor_BeginAndExtendSelection(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-sel-mouse", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"alpha", "beta", "gamma"}

	// Begin at (0, 1).
	ne.BeginSelection(0, 1)
	if ne.editor.Sel == nil {
		t.Fatal("Sel should be non-nil after BeginSelection")
	}
	if ne.editor.Sel.Anchor != (EditorPos{Row: 0, Col: 1}) {
		t.Errorf("Anchor = %+v, want {0, 1}", ne.editor.Sel.Anchor)
	}
	if ne.editor.Sel.Cursor != (EditorPos{Row: 0, Col: 1}) {
		t.Errorf("Cursor = %+v, want {0, 1}", ne.editor.Sel.Cursor)
	}

	// Extend to (2, 3).
	ne.ExtendSelection(2, 3)
	if ne.editor.Sel.Anchor != (EditorPos{Row: 0, Col: 1}) {
		t.Errorf("Anchor changed unexpectedly: %+v", ne.editor.Sel.Anchor)
	}
	if ne.editor.Sel.Cursor != (EditorPos{Row: 2, Col: 3}) {
		t.Errorf("Cursor = %+v, want {2, 3}", ne.editor.Sel.Cursor)
	}
	if ne.editor.CursorRow != 2 || ne.editor.CursorCol != 3 {
		t.Errorf("editor cursor = (%d, %d), want (2, 3)", ne.editor.CursorRow, ne.editor.CursorCol)
	}

	// Extracted text spans the three lines: "lpha\nbeta\ngam"
	got := editorExtractText(ne.editor.Lines, ne.editor.Sel)
	want := "lpha\nbeta\ngam"
	if got != want {
		t.Errorf("extracted = %q, want %q", got, want)
	}
}

func TestNotesEditor_ExtendSelection_OutOfBoundsClamps(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-clamp", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"x"}
	ne.BeginSelection(0, 0)
	ne.ExtendSelection(99, 99)
	if ne.editor.Sel.Cursor != (EditorPos{Row: 0, Col: 1}) {
		t.Errorf("clamped Cursor = %+v, want {0, 1}", ne.editor.Sel.Cursor)
	}
}

func TestModel_NotesEditorPosAt_ConvertsScreenCoordsAndScroll(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-coords", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"line0", "line1", "line2", "line3"}
	ne.editor.ScrollTop = 1

	m := Model{
		notesMode:     true,
		notesEditor:   ne,
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
		tabs:          []*TabModel{NewTabModel("t1", "Shell")},
	}

	// notesW = (100 - 0) * 2 / 5 = 40 → boxX0 = 60, boxX1 = 100, boxY0 = 1, boxY1 = 29
	// bodyX0 = 60 + 1 + 4 = 65, bodyY0 = 3, bodyX1 = 99, bodyY1 = 27
	// Click at body origin = (65, 3) → row = ScrollTop(1) + 0 = 1, col = 0
	row, col, ok := m.notesEditorPosAt(65, 3)
	if !ok {
		t.Fatal("notesEditorPosAt returned ok=false for in-box click")
	}
	if row != 1 || col != 0 {
		t.Errorf("body origin → (%d, %d), want (1, 0)", row, col)
	}

	// Click two rows down and three cols right.
	row, col, _ = m.notesEditorPosAt(68, 5)
	if row != 3 || col != 3 {
		t.Errorf("(68, 5) → (%d, %d), want (3, 3)", row, col)
	}

	// Click outside the box → ok=false.
	if _, _, ok := m.notesEditorPosAt(10, 10); ok {
		t.Error("click outside notes box should return ok=false")
	}

	// Click on the gutter (just inside the box but left of body) clamps to col 0.
	row, col, ok = m.notesEditorPosAt(61, 5)
	if !ok {
		t.Fatal("gutter click should still be inside box")
	}
	if col != 0 {
		t.Errorf("gutter click col = %d, want 0 (clamped)", col)
	}
}

func TestModel_NotesEditorPosAt_NotInNotesMode(t *testing.T) {
	t.Parallel()
	m := Model{
		notesMode:     false,
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
	}
	if _, _, ok := m.notesEditorPosAt(50, 10); ok {
		t.Error("notesEditorPosAt should return ok=false when notes mode is off")
	}
}

func TestNotesEditor_View_BorderColorReflectsFocus(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-view", "Build", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	focused := ne.View(40, 10, true)
	unfocused := ne.View(40, 10, false)
	if focused == "" || unfocused == "" {
		t.Fatal("View returned empty string")
	}
	// Focused border uses lipgloss color 63 (bright blue), unfocused uses
	// 240 (dim grey). The two renders must differ in at least one ANSI
	// colour code.
	if focused == unfocused {
		t.Error("focused and unfocused View output should differ (border colour)")
	}
}

func TestNotesEditor_FooterMentionsTabFocusCycle(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-footer", "Build", 60, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	// Editor focused: footer should mention "Tab pane" (Tab → switch focus to pane).
	rendered := ne.View(60, 10, true)
	if !strings.Contains(stripANSI(rendered), "Tab pane") {
		t.Errorf("editor-focused footer should mention 'Tab pane', got:\n%s", stripANSI(rendered))
	}
	// Pane focused: footer should mention "Tab notes" (Tab → switch back to editor).
	rendered = ne.View(60, 10, false)
	if !strings.Contains(stripANSI(rendered), "Tab notes") {
		t.Errorf("pane-focused footer should mention 'Tab notes', got:\n%s", stripANSI(rendered))
	}
}

// stripANSI removes simple ANSI escape sequences for substring assertions.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == 0x1b {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' || r == 'K' || r == 'H' || r == 'J' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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

func TestTextEditor_GutterWidth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		numLines int
		want     int
	}{
		{"empty document", 0, 4},     // max(3, 1) + 1
		{"single line", 1, 4},        // max(3, 1) + 1
		{"99 lines", 99, 4},          // max(3, 2) + 1
		{"100 lines", 100, 4},        // max(3, 3) + 1
		{"999 lines", 999, 4},        // max(3, 3) + 1
		{"1000 lines", 1000, 5},      // max(3, 4) + 1
		{"10000 lines", 10000, 6},    // max(3, 5) + 1
		{"100000 lines", 100000, 7},  // max(3, 6) + 1
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := make([]string, tt.numLines)
			ed := &TextEditor{Lines: lines}
			if got := ed.GutterWidth(); got != tt.want {
				t.Errorf("GutterWidth(%d lines) = %d, want %d", tt.numLines, got, tt.want)
			}
		})
	}
}

// --- Model-level tests for the notes mode integration ---

// newNotesTestModel builds a minimal Model wired up enough to exercise
// notes-mode helpers without needing a Bubble Tea program.
func newNotesTestModel(t *testing.T, ne *NotesEditor) *Model {
	t.Helper()
	m := &Model{
		notesMode:     true,
		notesEditor:   ne,
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: make(map[string]bool),
		tabs:          []*TabModel{NewTabModel("t1", "Shell")},
		cfg:           config.Default(),
	}
	return m
}

func TestModel_NotesKeyExempt_AllowsGlobalShortcuts(t *testing.T) {
	t.Parallel()
	m := Model{cfg: config.Default()}
	kb := m.cfg.Keybindings

	// Keys that MUST bypass the notes editor.
	exempt := []string{
		kb.ClosePane, kb.CloseTab, kb.SplitHorizontal, kb.SplitVertical,
		kb.NewTab, kb.RenameTab, kb.RenamePane, kb.CycleTabColor,
		kb.FocusPane,
		kb.NotificationToggle, kb.NotificationFocus, kb.GoBack,
		kb.JSONTransform, kb.QuickActions,
		"f1", "ctrl+n",
		"alt+1", "alt+2", "alt+3", "alt+4", "alt+5",
		"alt+6", "alt+7", "alt+8", "alt+9",
	}
	for _, key := range exempt {
		if !m.notesKeyExempt(key) {
			t.Errorf("notesKeyExempt(%q) = false, want true", key)
		}
	}

	// Keys that MUST be consumed by the editor (text input + Tab/Shift+Tab).
	consumed := []string{
		"a", "b", "z", "0", "9", " ",
		"enter", "backspace", "delete",
		"left", "right", "up", "down",
		"home", "end",
		kb.NextPane, // Tab — toggles focus, not exempt
		kb.PrevPane, // Shift+Tab — toggles focus, not exempt
	}
	for _, key := range consumed {
		if m.notesKeyExempt(key) {
			t.Errorf("notesKeyExempt(%q) = true, want false", key)
		}
	}
}

func TestModel_ExitNotesModeInPlace_ResetsAllFlagsAndRevertsFocus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-exit", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	// Make the editor dirty so Close() actually flushes content.
	ne.HandleKey("h")
	ne.HandleKey("i")

	m := newNotesTestModel(t, ne)
	m.notesPaneFocused = true
	m.notesEnteredFocus = true
	m.notesAnchorRow = 5
	m.notesAnchorCol = 3
	// Simulate "we owned the focus toggle" — populate two leaves on the
	// only tab so ToggleFocus is not a no-op, then enable focus mode.
	tab := m.tabs[0]
	tab.Root = NewLeaf(NewPaneModel("p1", 1024))
	tab.Root.SplitLeaf("p1", SplitVertical)
	tab.Root.FillPlaceholder(NewPaneModel("p2", 1024))
	tab.ActivePane = "p1"
	tab.ToggleFocus() // focusMode = true

	m.exitNotesModeInPlace()

	if m.notesMode {
		t.Error("notesMode should be false")
	}
	if m.notesEditor != nil {
		t.Error("notesEditor should be nil")
	}
	if m.notesPaneFocused {
		t.Error("notesPaneFocused should be false")
	}
	if m.notesEnteredFocus {
		t.Error("notesEnteredFocus should be false")
	}
	if m.notesAnchorRow != 0 || m.notesAnchorCol != 0 {
		t.Errorf("anchor = (%d, %d), want (0, 0)", m.notesAnchorRow, m.notesAnchorCol)
	}
	if tab.FocusMode() {
		t.Error("focus mode should be reverted because we owned it")
	}
	// Persisted content should reflect the dirty edits.
	got, err := persist.LoadNotes(dir, "pane-exit")
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != "hi\n" {
		t.Errorf("persisted = %q, want %q", got, "hi\n")
	}
}

func TestModel_ExitNotesModeInPlace_DoesNotRevertUserFocus(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-userfocus", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m := newNotesTestModel(t, ne)
	// User entered focus mode themselves; we did NOT own the toggle.
	m.notesEnteredFocus = false

	tab := m.tabs[0]
	tab.Root = NewLeaf(NewPaneModel("p1", 1024))
	tab.Root.SplitLeaf("p1", SplitVertical)
	tab.Root.FillPlaceholder(NewPaneModel("p2", 1024))
	tab.ActivePane = "p1"
	tab.ToggleFocus()

	m.exitNotesModeInPlace()

	if !tab.FocusMode() {
		t.Error("focus mode should NOT be reverted when notesEnteredFocus is false")
	}
}

func TestModel_SwitchTab_FlushesNotesMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-switch", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("z")

	m := newNotesTestModel(t, ne)
	// Two tabs so switchTab(1) is valid.
	m.tabs = append(m.tabs, NewTabModel("t2", "Build"))
	m.activeTab = 0

	m.switchTab(1)

	if m.notesMode {
		t.Error("notesMode should be false after switchTab")
	}
	if m.notesEditor != nil {
		t.Error("notesEditor should be nil after switchTab")
	}
	if m.activeTab != 1 {
		t.Errorf("activeTab = %d, want 1", m.activeTab)
	}
	got, _ := persist.LoadNotes(dir, "pane-switch")
	if got != "z\n" {
		t.Errorf("persisted on switch = %q, want %q", got, "z\n")
	}
}

func TestModel_ToggleNotesMode_SinglePaneTab_NoFocusRevert(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Notification.SidebarWidth = 30
	cfg.Notification.MaxEvents = 50
	pane := NewPaneModel("pane-only", 1024)
	tab := NewTabModel("t1", "Shell")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID
	m := Model{
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: make(map[string]bool),
		tabs:          []*TabModel{tab},
		activeTab:     0,
		cfg:           cfg,
	}

	mAfterEnter, _ := m.toggleNotesMode()
	m = mAfterEnter.(Model)
	if !m.notesMode {
		t.Fatal("notesMode should be true after toggle")
	}
	// Single-pane tab → ToggleFocus is a no-op → notesEnteredFocus stays false.
	if m.notesEnteredFocus {
		t.Error("notesEnteredFocus should be false on a single-pane tab")
	}
	if tab.FocusMode() {
		t.Error("tab should not be in focus mode on a single-pane tab")
	}

	mAfterExit, _ := m.exitNotesMode()
	m = mAfterExit.(Model)
	if m.notesMode {
		t.Error("notesMode should be false after exit")
	}
	if tab.FocusMode() {
		t.Error("focus mode should not be on after exit")
	}
}

func TestModel_NotesMouseRightClick_CopiesAndClearsEditorSelection(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-right", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"alpha", "beta"}
	ne.BeginSelection(0, 0)
	ne.ExtendSelection(1, 4)

	m := newNotesTestModel(t, ne)

	// Synthesize a right-click MouseClickMsg via Update.
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseRight})
	mAfter := updated.(Model)

	if mAfter.notesEditor.HasSelection() {
		t.Error("editor selection should be cleared after right-click copy")
	}
	// The pane selection (m.selection) should be untouched (it was nil).
	if mAfter.selection != nil {
		t.Error("pane selection should remain nil")
	}
}

func TestNotesEditor_BackwardSelection_ExtractText(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-backward", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.editor.Lines = []string{"alpha", "beta", "gamma"}

	// Anchor at the END, extend BACK to the start. The Normalized()
	// path inside editorExtractText should still produce the in-order
	// text "alpha\nbeta\ngam".
	ne.BeginSelection(2, 3)
	ne.ExtendSelection(0, 0)

	got := editorExtractText(ne.editor.Lines, ne.editor.Sel)
	want := "alpha\nbeta\ngam"
	if got != want {
		t.Errorf("backward selection extracted = %q, want %q", got, want)
	}
}

func TestModel_NotesPanelWidth_NoSidebar(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-w", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m := newNotesTestModel(t, ne)
	// Notifications hidden by default → no sidebar contribution.
	notesW, sidebarW := m.notesPanelWidth()
	if sidebarW != 0 {
		t.Errorf("sidebarW = %d, want 0", sidebarW)
	}
	// (100 - 0) * 2 / 5 = 40, exceeds the 30 floor.
	if notesW != 40 {
		t.Errorf("notesW = %d, want 40", notesW)
	}
}

func TestModel_NotesPanelWidth_WithSidebar(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-w-side", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m := newNotesTestModel(t, ne)
	m.notifications.visible = true

	notesW, sidebarW := m.notesPanelWidth()
	if sidebarW != 30 {
		t.Errorf("sidebarW = %d, want 30", sidebarW)
	}
	// (100 - 30) * 2 / 5 = 28, below 30 → clamped to 30.
	if notesW != 30 {
		t.Errorf("notesW = %d, want 30 (clamp)", notesW)
	}
}

func TestModel_NotesPanelWidth_TooNarrow_ReturnsZero(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-narrow", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m := newNotesTestModel(t, ne)
	m.width = 60 // 60 - 0 - 30 = 30, below minTermWidth(40)
	notesW, _ := m.notesPanelWidth()
	if notesW != 0 {
		t.Errorf("notesW = %d, want 0 (terminal too narrow)", notesW)
	}
}

func TestModel_ApplyWorkspaceState_BoundPanePruned_ExitsNotes(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-gone", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m := newNotesTestModel(t, ne)
	// The bound pane is "pane-gone". Daemon now says only "pane-other"
	// exists.
	state := WorkspaceStateMsg{
		Tabs: []TabInfo{{
			ID:    "t1",
			Name:  "Shell",
			Panes: []string{"pane-other"},
		}},
		Panes: []PaneInfo{
			{ID: "pane-other", Name: "Other"},
		},
		ActiveTab: "t1",
	}
	m.applyWorkspaceState(state)
	if m.notesMode {
		t.Error("notesMode should be false after bound pane was pruned")
	}
	if m.notesEditor != nil {
		t.Error("notesEditor should be nil after bound pane was pruned")
	}
}

func TestModel_ApplyWorkspaceState_BoundPaneNotActive_ResyncsActive(t *testing.T) {
	t.Parallel()
	ne, err := NewNotesEditor(t.TempDir(), "pane-bound", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m := newNotesTestModel(t, ne)
	// Pre-populate the tab with two panes; bound is "pane-bound" but
	// the tab's ActivePane is "pane-other".
	tab := m.tabs[0]
	tab.Root = NewLeaf(NewPaneModel("pane-bound", 1024))
	tab.Root.SplitLeaf("pane-bound", SplitVertical)
	tab.Root.FillPlaceholder(NewPaneModel("pane-other", 1024))
	tab.ActivePane = "pane-other"

	state := WorkspaceStateMsg{
		Tabs: []TabInfo{{
			ID:    "t1",
			Name:  "Shell",
			Panes: []string{"pane-bound", "pane-other"},
		}},
		Panes: []PaneInfo{
			{ID: "pane-bound", Name: "Bound"},
			{ID: "pane-other", Name: "Other"},
		},
		ActiveTab: "t1",
	}
	m.applyWorkspaceState(state)

	if !m.notesMode {
		t.Error("notesMode should still be true after re-sync")
	}
	if m.tabs[0].ActivePane != "pane-bound" {
		t.Errorf("ActivePane = %q after re-sync, want %q", m.tabs[0].ActivePane, "pane-bound")
	}
}
