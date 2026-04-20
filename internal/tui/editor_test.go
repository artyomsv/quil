package tui

import "testing"

func TestTextEditor_ApproxBytes(t *testing.T) {
	ed := &TextEditor{Lines: []string{"hello", "world!"}}
	// "hello" (5) + "world!" (6) + 1 newline between them = 12
	if got := ed.ApproxBytes(); got != 12 {
		t.Errorf("ApproxBytes = %d, want 12", got)
	}

	ed.Lines = nil
	if got := ed.ApproxBytes(); got != 0 {
		t.Errorf("ApproxBytes(nil lines) = %d, want 0", got)
	}

	ed.Lines = []string{""}
	if got := ed.ApproxBytes(); got != 0 {
		t.Errorf("ApproxBytes(single empty line) = %d, want 0", got)
	}

	ed.Lines = []string{"single"}
	if got := ed.ApproxBytes(); got != 6 {
		t.Errorf("ApproxBytes(single line) = %d, want 6", got)
	}

	var nilEditor *TextEditor
	if got := nilEditor.ApproxBytes(); got != 0 {
		t.Errorf("nil receiver ApproxBytes = %d, want 0", got)
	}
}
