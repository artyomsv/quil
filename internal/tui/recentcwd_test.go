package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPushRecentCWD(t *testing.T) {
	// Every dir here is a bare token that filepath.Clean maps to itself, so
	// the expected slices are literal (no per-platform massaging needed).
	tests := []struct {
		name  string
		list  []string
		dir   string
		limit int
		want  []string
	}{
		{"empty dir is no-op", []string{"a"}, "", 5, []string{"a"}},
		{"whitespace dir is no-op", []string{"a"}, "  ", 5, []string{"a"}},
		{"prepend new", []string{"a", "b"}, "c", 5, []string{"c", "a", "b"}},
		{"dedup moves to front", []string{"a", "b", "c"}, "c", 5, []string{"c", "a", "b"}},
		{"cap truncates oldest", []string{"a", "b", "c"}, "d", 3, []string{"d", "a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pushRecentCWD(tt.list, tt.dir, tt.limit); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pushRecentCWD(%v, %q, %d) = %v, want %v", tt.list, tt.dir, tt.limit, got, tt.want)
			}
		})
	}
}

func TestPushRecentCWD_DoesNotMutateInput(t *testing.T) {
	in := []string{"a", "b"}
	_ = pushRecentCWD(in, "c", 5)
	if !reflect.DeepEqual(in, []string{"a", "b"}) {
		t.Errorf("input mutated: %v", in)
	}
}

func TestPathEqualCase(t *testing.T) {
	tests := []struct {
		name            string
		a, b            string
		caseInsensitive bool
		want            bool
	}{
		{"sensitive equal", "/a/b", "/a/b", false, true},
		{"sensitive differ by case", "/a/B", "/a/b", false, false},
		{"insensitive equal by case", `C:\Foo`, `c:\foo`, true, true},
		{"insensitive still differ by content", `C:\Foo`, `C:\Bar`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathEqualCase(tt.a, tt.b, tt.caseInsensitive); got != tt.want {
				t.Errorf("pathEqualCase(%q, %q, %v) = %v, want %v", tt.a, tt.b, tt.caseInsensitive, got, tt.want)
			}
		})
	}
}

func TestLoadSaveRecentCWDs_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent-cwds.json")
	want := []string{"/a", "/b"}
	if err := SaveRecentCWDs(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadRecentCWDs(path); !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip = %v, want %v", got, want)
	}
}

func TestLoadRecentCWDs_MissingFile(t *testing.T) {
	if got := LoadRecentCWDs(filepath.Join(t.TempDir(), "nope.json")); len(got) != 0 {
		t.Errorf("missing file = %v, want empty", got)
	}
}

func TestLoadRecentCWDs_CorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent-cwds.json")
	if err := os.WriteFile(path, []byte("{ not valid json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := LoadRecentCWDs(path); got != nil {
		t.Errorf("corrupt file = %v, want nil", got)
	}
}

func TestLoadRecentCWDs_CapsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent-cwds.json")
	// Persist more than the cap, bypassing pushRecentCWD's own cap.
	big := []string{"/1", "/2", "/3", "/4", "/5", "/6", "/7"}
	if err := SaveRecentCWDs(path, big); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadRecentCWDs(path); len(got) != recentCWDMax {
		t.Errorf("loaded %d entries, want cap %d", len(got), recentCWDMax)
	}
}

func TestSaveRecentCWDs_WriteError(t *testing.T) {
	// The destination path is itself a directory, so the final rename fails
	// even though the parent exists (SaveRecentCWDs now MkdirAll's the parent).
	// Verifies the error propagates rather than being swallowed.
	dest := filepath.Join(t.TempDir(), "recent")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := SaveRecentCWDs(dest, []string{"/a"}); err == nil {
		t.Error("expected error when destination is a directory, got nil")
	}
}
