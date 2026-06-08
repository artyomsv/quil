package hookevents

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSafePaneID covers the path-traversal guard in Spool.Cleanup. The
// daemon's IPC handlers reject malformed PaneIDs upstream, but this
// defense-in-depth layer must independently refuse anything that could
// escape the spool directory.
func TestSafePaneID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"realistic pane id", "pane-a1b2c3d4", true},
		{"forward slash", "pane/../etc", false},
		{"backslash", "pane\\nope", false},
		{"parent dir alone", "..", false},
		{"current dir", ".", false},
		{"parent traversal embedded", "pane-..bogus", false},
		{"null byte", "pane-\x00.id", false},
		{"single dot", "pane-1.0", true}, // dot in name is fine
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := safePaneID(tt.input); got != tt.want {
				t.Errorf("safePaneID(%q): got %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestSpool_Cleanup_RejectsTraversalPaneID directly drives the Cleanup
// guard. A `../`-laden paneID must not result in an os.Remove call against
// a path outside the spool directory.
func TestSpool_Cleanup_RejectsTraversalPaneID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	spoolDir := filepath.Join(root, "events")
	siblingDir := filepath.Join(root, "sibling")
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Seed a victim file outside the spool dir that a path-traversal
	// attack would target.
	victim := filepath.Join(siblingDir, "secrets.jsonl")
	if err := os.WriteFile(victim, []byte("very important"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewSpool(spoolDir)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}

	// Naive concatenation would form: <spoolDir>/../sibling/secrets.jsonl
	// → after filepath.Join clean: <root>/sibling/secrets.jsonl. The
	// guard must refuse and the victim file must survive.
	s.Cleanup("../sibling/secrets")

	if _, err := os.Stat(victim); err != nil {
		t.Errorf("guard must prevent traversal-driven unlink; victim file vanished: %v", err)
	}
}
