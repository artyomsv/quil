package tui

import (
	"testing"
)

func TestEditorSelNormalized(t *testing.T) {
	// Forward selection (anchor before cursor)
	sel := &EditorSel{
		Anchor: EditorPos{Row: 1, Col: 5},
		Cursor: EditorPos{Row: 3, Col: 10},
	}
	start, end := sel.Normalized()
	if start.Row != 1 || start.Col != 5 {
		t.Errorf("forward start: got (%d,%d), want (1,5)", start.Row, start.Col)
	}
	if end.Row != 3 || end.Col != 10 {
		t.Errorf("forward end: got (%d,%d), want (3,10)", end.Row, end.Col)
	}

	// Backward selection (cursor before anchor)
	sel2 := &EditorSel{
		Anchor: EditorPos{Row: 3, Col: 10},
		Cursor: EditorPos{Row: 1, Col: 5},
	}
	start2, end2 := sel2.Normalized()
	if start2.Row != 1 || start2.Col != 5 {
		t.Errorf("backward start: got (%d,%d), want (1,5)", start2.Row, start2.Col)
	}
	if end2.Row != 3 || end2.Col != 10 {
		t.Errorf("backward end: got (%d,%d), want (3,10)", end2.Row, end2.Col)
	}

	// Same line, backward
	sel3 := &EditorSel{
		Anchor: EditorPos{Row: 2, Col: 8},
		Cursor: EditorPos{Row: 2, Col: 3},
	}
	start3, end3 := sel3.Normalized()
	if start3.Col != 3 || end3.Col != 8 {
		t.Errorf("same-line backward: got cols (%d,%d), want (3,8)", start3.Col, end3.Col)
	}
}

func TestEditorSelIsEmpty(t *testing.T) {
	sel := &EditorSel{
		Anchor: EditorPos{Row: 1, Col: 5},
		Cursor: EditorPos{Row: 1, Col: 5},
	}
	if !sel.IsEmpty() {
		t.Error("same position should be empty")
	}

	sel.Cursor.Col = 6
	if sel.IsEmpty() {
		t.Error("different position should not be empty")
	}
}

func TestEditorSelColRange(t *testing.T) {
	// Multi-line selection: rows 1..3, start col 5, end col 10
	sel := &EditorSel{
		Anchor: EditorPos{Row: 1, Col: 5},
		Cursor: EditorPos{Row: 3, Col: 10},
	}

	// Before selection
	s, e := sel.ColRange(0, 20)
	if s != -1 || e != -1 {
		t.Errorf("row 0: got (%d,%d), want (-1,-1)", s, e)
	}

	// Start line
	s, e = sel.ColRange(1, 20)
	if s != 5 || e != 20 {
		t.Errorf("row 1 (start): got (%d,%d), want (5,20)", s, e)
	}

	// Middle line
	s, e = sel.ColRange(2, 15)
	if s != 0 || e != 15 {
		t.Errorf("row 2 (middle): got (%d,%d), want (0,15)", s, e)
	}

	// End line
	s, e = sel.ColRange(3, 20)
	if s != 0 || e != 10 {
		t.Errorf("row 3 (end): got (%d,%d), want (0,10)", s, e)
	}

	// After selection
	s, e = sel.ColRange(4, 20)
	if s != -1 || e != -1 {
		t.Errorf("row 4: got (%d,%d), want (-1,-1)", s, e)
	}

	// Single-line selection
	sel2 := &EditorSel{
		Anchor: EditorPos{Row: 2, Col: 3},
		Cursor: EditorPos{Row: 2, Col: 8},
	}
	s, e = sel2.ColRange(2, 20)
	if s != 3 || e != 8 {
		t.Errorf("single-line: got (%d,%d), want (3,8)", s, e)
	}
}

func TestEditorWordBoundary(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		startCol int
		dir      int
		want     int
	}{
		{"forward from start", "hello world", 0, 1, 5},
		{"forward past space", "hello world", 4, 1, 11},
		{"backward from end", "hello world", 11, -1, 6},
		{"backward past space", "hello world", 6, -1, 0},
		{"forward with punctuation", "foo.bar baz", 0, 1, 3},
		{"empty line", "", 0, 1, 0},
		{"single word", "hello", 0, 1, 5},
		{"forward at end", "hello", 5, 1, 5},
		{"backward at start", "hello", 0, -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editorWordBoundary(tt.line, tt.startCol, tt.dir)
			if got != tt.want {
				t.Errorf("editorWordBoundary(%q, %d, %d) = %d, want %d",
					tt.line, tt.startCol, tt.dir, got, tt.want)
			}
		})
	}
}

func TestEditorWordJump(t *testing.T) {
	lines := []string{
		"hello world",
		"foo bar baz",
		"end",
	}

	// Forward 1 word from start
	pos := editorWordJump(lines, EditorPos{Row: 0, Col: 0}, 1, 1)
	if pos.Row != 0 || pos.Col != 5 {
		t.Errorf("forward 1: got (%d,%d), want (0,5)", pos.Row, pos.Col)
	}

	// Forward 2 words from start
	pos = editorWordJump(lines, EditorPos{Row: 0, Col: 0}, 1, 2)
	if pos.Row != 0 || pos.Col != 11 {
		t.Errorf("forward 2: got (%d,%d), want (0,11)", pos.Row, pos.Col)
	}

	// Forward wrapping to next line
	pos = editorWordJump(lines, EditorPos{Row: 0, Col: 11}, 1, 1)
	if pos.Row != 1 {
		t.Errorf("forward wrap: got row %d, want 1", pos.Row)
	}

	// Backward 1 word from middle
	pos = editorWordJump(lines, EditorPos{Row: 1, Col: 7}, -1, 1)
	if pos.Row != 1 || pos.Col != 4 {
		t.Errorf("backward 1: got (%d,%d), want (1,4)", pos.Row, pos.Col)
	}

	// 3-word jump
	pos = editorWordJump(lines, EditorPos{Row: 1, Col: 0}, 1, 3)
	if pos.Row != 1 || pos.Col != 11 {
		t.Errorf("forward 3: got (%d,%d), want (1,11)", pos.Row, pos.Col)
	}
}

func TestEditorExtractText(t *testing.T) {
	lines := []string{
		"first line here",
		"second line",
		"third line end",
	}

	// Single-line extraction
	sel := &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 6},
		Cursor: EditorPos{Row: 0, Col: 10},
	}
	got := editorExtractText(lines, sel)
	if got != "line" {
		t.Errorf("single-line: got %q, want %q", got, "line")
	}

	// Multi-line extraction
	sel2 := &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 6},
		Cursor: EditorPos{Row: 2, Col: 5},
	}
	got2 := editorExtractText(lines, sel2)
	want2 := "line here\nsecond line\nthird"
	if got2 != want2 {
		t.Errorf("multi-line: got %q, want %q", got2, want2)
	}

	// Full document
	sel3 := &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 0},
		Cursor: EditorPos{Row: 2, Col: 14},
	}
	got3 := editorExtractText(lines, sel3)
	want3 := "first line here\nsecond line\nthird line end"
	if got3 != want3 {
		t.Errorf("full doc: got %q, want %q", got3, want3)
	}

	// Empty selection
	sel4 := &EditorSel{
		Anchor: EditorPos{Row: 1, Col: 3},
		Cursor: EditorPos{Row: 1, Col: 3},
	}
	got4 := editorExtractText(lines, sel4)
	if got4 != "" {
		t.Errorf("empty: got %q, want empty", got4)
	}

	// Nil selection
	got5 := editorExtractText(lines, nil)
	if got5 != "" {
		t.Errorf("nil: got %q, want empty", got5)
	}
}

func TestDeleteSelection(t *testing.T) {
	// Same-line deletion
	e := &TextEditor{
		Lines:     []string{"hello world"},
		CursorRow: 0,
		CursorCol: 5,
		Sel: &EditorSel{
			Anchor: EditorPos{Row: 0, Col: 2},
			Cursor: EditorPos{Row: 0, Col: 7},
		},
	}
	e.deleteSelection()
	if e.Lines[0] != "heorld" {
		t.Errorf("same-line delete: got %q, want %q", e.Lines[0], "heorld")
	}
	if e.CursorRow != 0 || e.CursorCol != 2 {
		t.Errorf("cursor after same-line: (%d,%d), want (0,2)", e.CursorRow, e.CursorCol)
	}
	if e.Sel != nil {
		t.Error("selection should be nil after delete")
	}

	// Multi-line deletion
	e2 := &TextEditor{
		Lines:     []string{"first line", "second line", "third line"},
		CursorRow: 0,
		CursorCol: 0,
		Sel: &EditorSel{
			Anchor: EditorPos{Row: 0, Col: 6},
			Cursor: EditorPos{Row: 2, Col: 6},
		},
	}
	e2.deleteSelection()
	if len(e2.Lines) != 1 {
		t.Fatalf("multi-line delete: got %d lines, want 1", len(e2.Lines))
	}
	if e2.Lines[0] != "first line" {
		t.Errorf("multi-line merge: got %q, want %q", e2.Lines[0], "first line")
	}
	if e2.CursorRow != 0 || e2.CursorCol != 6 {
		t.Errorf("cursor after multi-line: (%d,%d), want (0,6)", e2.CursorRow, e2.CursorCol)
	}
}

func TestInsertMultiLine(t *testing.T) {
	// Single-line insert
	e := &TextEditor{
		Lines:     []string{"hello world"},
		CursorRow: 0,
		CursorCol: 5,
	}
	e.InsertMultiLine(" beautiful")
	if e.Lines[0] != "hello beautiful world" {
		t.Errorf("single-line: got %q, want %q", e.Lines[0], "hello beautiful world")
	}

	// Multi-line insert
	e2 := &TextEditor{
		Lines:     []string{"start end"},
		CursorRow: 0,
		CursorCol: 6,
	}
	e2.InsertMultiLine("line1\nline2\nline3")
	if len(e2.Lines) != 3 {
		t.Fatalf("multi-line: got %d lines, want 3", len(e2.Lines))
	}
	if e2.Lines[0] != "start line1" {
		t.Errorf("line 0: got %q, want %q", e2.Lines[0], "start line1")
	}
	if e2.Lines[1] != "line2" {
		t.Errorf("line 1: got %q, want %q", e2.Lines[1], "line2")
	}
	if e2.Lines[2] != "line3end" {
		t.Errorf("line 2: got %q, want %q", e2.Lines[2], "line3end")
	}
	if e2.CursorRow != 2 || e2.CursorCol != 5 {
		t.Errorf("cursor: (%d,%d), want (2,5)", e2.CursorRow, e2.CursorCol)
	}

	// Insert with active selection (should delete selection first)
	e3 := &TextEditor{
		Lines:     []string{"hello world"},
		CursorRow: 0,
		CursorCol: 5,
		Sel: &EditorSel{
			Anchor: EditorPos{Row: 0, Col: 0},
			Cursor: EditorPos{Row: 0, Col: 5},
		},
	}
	e3.InsertMultiLine("hi")
	if e3.Lines[0] != "hi world" {
		t.Errorf("insert with selection: got %q, want %q", e3.Lines[0], "hi world")
	}
}

func TestSelectAll(t *testing.T) {
	e := &TextEditor{
		Lines:     []string{"first", "second", "third"},
		CursorRow: 0,
		CursorCol: 0,
	}
	e.selectAll()

	if e.Sel == nil {
		t.Fatal("selection should not be nil after selectAll")
	}
	if e.Sel.Anchor.Row != 0 || e.Sel.Anchor.Col != 0 {
		t.Errorf("anchor: (%d,%d), want (0,0)", e.Sel.Anchor.Row, e.Sel.Anchor.Col)
	}
	if e.Sel.Cursor.Row != 2 || e.Sel.Cursor.Col != 5 {
		t.Errorf("cursor: (%d,%d), want (2,5)", e.Sel.Cursor.Row, e.Sel.Cursor.Col)
	}
}

func TestHandleKeySelectionFlow(t *testing.T) {
	e := &TextEditor{
		Lines:      []string{"hello world test"},
		CursorRow:  0,
		CursorCol:  5,
		ViewHeight: 10,
		ViewWidth:  80,
	}

	// Shift+Right should start selection
	e.HandleKey("shift+right")
	if e.Sel == nil {
		t.Fatal("selection should be initialized after shift+right")
	}
	if e.Sel.Anchor.Col != 5 {
		t.Errorf("anchor col: %d, want 5", e.Sel.Anchor.Col)
	}
	if e.Sel.Cursor.Col != 6 {
		t.Errorf("cursor col: %d, want 6", e.Sel.Cursor.Col)
	}

	// More shift+right extends
	e.HandleKey("shift+right")
	e.HandleKey("shift+right")
	if e.Sel.Cursor.Col != 8 {
		t.Errorf("after 3 shift+right: cursor col %d, want 8", e.Sel.Cursor.Col)
	}

	// Esc clears selection (does not close editor)
	_, closed, _ := e.HandleKey("esc")
	if closed {
		t.Error("esc with selection should not close editor")
	}
	if e.Sel != nil {
		t.Error("selection should be nil after esc")
	}

	// Movement key clears selection
	e.HandleKey("shift+right")
	e.HandleKey("shift+right")
	e.HandleKey("right") // plain movement
	if e.Sel != nil {
		t.Error("selection should be cleared by plain movement")
	}

	// Character input with selection replaces it
	e.CursorCol = 0
	e.HandleKey("shift+right")
	e.HandleKey("shift+right")
	e.HandleKey("shift+right")
	e.HandleKey("shift+right")
	e.HandleKey("shift+right") // select "hello"
	e.HandleKey("X")
	if e.Lines[0] != "X world test" {
		t.Errorf("replace with char: got %q, want %q", e.Lines[0], "X world test")
	}
}

func TestHandleKeyCtrlA(t *testing.T) {
	e := &TextEditor{
		Lines:      []string{"line one", "line two"},
		CursorRow:  0,
		CursorCol:  0,
		ViewHeight: 10,
		ViewWidth:  80,
	}

	e.HandleKey("ctrl+a")
	if e.Sel == nil {
		t.Fatal("ctrl+a should create selection")
	}
	text := editorExtractText(e.Lines, e.Sel)
	want := "line one\nline two"
	if text != want {
		t.Errorf("ctrl+a text: got %q, want %q", text, want)
	}
}

func TestEditorParagraphJump(t *testing.T) {
	lines := []string{
		"first paragraph",  // 0
		"still first",      // 1
		"",                 // 2
		"second paragraph", // 3
		"still second",     // 4
		"",                 // 5
		"",                 // 6
		"third paragraph",  // 7
	}

	// Jump down from row 0 → should land on first blank line (row 2)
	got := editorParagraphJump(lines, 0, 1)
	if got != 2 {
		t.Errorf("down from 0: got %d, want 2", got)
	}

	// Jump down from row 2 (blank) → skip blanks, then find next blank (row 5)
	got = editorParagraphJump(lines, 2, 1)
	if got != 5 {
		t.Errorf("down from 2: got %d, want 5", got)
	}

	// Jump down from row 5 (blank) → skip blanks (5,6), scan content (7), hit end
	got = editorParagraphJump(lines, 5, 1)
	if got != 7 {
		t.Errorf("down from 5: got %d, want 7 (last line)", got)
	}

	// Jump up from row 7 → should land on blank line (row 6)
	got = editorParagraphJump(lines, 7, -1)
	if got != 6 {
		t.Errorf("up from 7: got %d, want 6", got)
	}

	// Jump up from row 4 → should land on blank line (row 2)
	got = editorParagraphJump(lines, 4, -1)
	if got != 2 {
		t.Errorf("up from 4: got %d, want 2", got)
	}

	// Jump up from row 0 → should stay at 0 (document start)
	got = editorParagraphJump(lines, 0, -1)
	if got != 0 {
		t.Errorf("up from 0: got %d, want 0", got)
	}

	// Jump down from last line → should stay at last line
	got = editorParagraphJump(lines, 7, 1)
	if got != 7 {
		t.Errorf("down from 7: got %d, want 7", got)
	}
}
