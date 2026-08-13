package notify

import (
	"strings"
	"testing"
)

func TestActivateURI_RoundTrip(t *testing.T) {
	t.Parallel()
	got := BuildActivateURI("quil", 4321, "pane-0a1b2c3d")
	pid, paneID, err := ParseActivateURI("quil", got)
	if err != nil {
		t.Fatalf("ParseActivateURI(%q) error = %v", got, err)
	}
	if pid != 4321 {
		t.Errorf("pid = %d, want 4321", pid)
	}
	if paneID != "pane-0a1b2c3d" {
		t.Errorf("paneID = %q, want pane-0a1b2c3d", paneID)
	}
}

func TestParseActivateURI_Rejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		scheme string
		raw    string
	}{
		{"empty", "quil", ""},
		{"wrong scheme", "quil", "quil-dev://activate?pid=1&pane=pane-0a1b2c3d"},
		{"dev scheme against prod", "quil-dev", "quil://activate?pid=1&pane=pane-0a1b2c3d"},
		{"no pane", "quil", "quil://activate?pid=1"},
		{"no pid", "quil", "quil://activate?pane=pane-0a1b2c3d"},
		{"pid not a number", "quil", "quil://activate?pid=abc&pane=pane-0a1b2c3d"},
		{"pid negative", "quil", "quil://activate?pid=-5&pane=pane-0a1b2c3d"},
		{"pid zero", "quil", "quil://activate?pid=0&pane=pane-0a1b2c3d"},
		{"pane wrong prefix", "quil", "quil://activate?pid=1&pane=tab-0a1b2c3d"},
		{"pane too short", "quil", "quil://activate?pid=1&pane=pane-0a1b2c3"},
		{"pane too long", "quil", "quil://activate?pid=1&pane=pane-0a1b2c3d4"},
		{"pane uppercase hex", "quil", "quil://activate?pid=1&pane=pane-0A1B2C3D"},
		{"pane non-hex", "quil", "quil://activate?pid=1&pane=pane-0a1b2c3g"},
		{"path traversal", "quil", "quil://activate?pid=1&pane=..%2f..%2fetc"},
		{"wrong host", "quil", "quil://spawn?pid=1&pane=pane-0a1b2c3d"},
		{"not a uri", "quil", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := ParseActivateURI(tt.scheme, tt.raw); err == nil {
				t.Errorf("ParseActivateURI(%q, %q) = nil error, want rejection", tt.scheme, tt.raw)
			}
		})
	}
}

// The URI is registry-reachable by any local process, so a hostile value must
// not survive parsing. This asserts the ceiling the design depends on: the only
// thing that can come out is a well-formed pane ID.
func TestParseActivateURI_OnlyEmitsWellFormedPaneIDs(t *testing.T) {
	t.Parallel()
	hostile := []string{
		"quil://activate?pid=1&pane=pane-0a1b2c3d%00evil",
		"quil://activate?pid=1&pane=" + strings.Repeat("a", 5000),
		"quil://activate?pid=1&pane=pane-0a1b2c3d&pane=pane-11111111",
	}
	for _, raw := range hostile {
		_, paneID, err := ParseActivateURI("quil", raw)
		if err == nil && !ValidPaneID(paneID) {
			t.Errorf("ParseActivateURI(%q) accepted invalid pane %q", raw, paneID)
		}
	}
}

func TestValidPaneID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id   string
		want bool
	}{
		{"pane-0a1b2c3d", true},
		{"pane-00000000", true},
		{"pane-ffffffff", true},
		{"", false},
		{"pane-", false},
		{"pane-0a1b2c3", false},
		{"pane-0a1b2c3d0", false},
		{"pane-0A1B2C3D", false},
		{"pane-0a1b2c3g", false},
		{"tab-0a1b2c3d", false},
		{"../../etc", false},
		{"pane-0a1b2c3d\x00", false},
	}
	for _, tt := range tests {
		if got := ValidPaneID(tt.id); got != tt.want {
			t.Errorf("ValidPaneID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
