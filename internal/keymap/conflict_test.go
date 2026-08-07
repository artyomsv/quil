package keymap

import (
	"strings"
	"testing"
)

func TestBuild_CrossTierIsReported(t *testing.T) {
	_, conflicts := Build(map[ActionID]string{
		"pane.mute":  "alt+z", // early
		"pane.close": "alt+z", // late — unreachable, early always wins
	})
	var found *Conflict
	for i := range conflicts {
		if conflicts[i].Kind == ConflictCrossTier {
			found = &conflicts[i]
		}
	}
	if found == nil {
		t.Fatalf("no cross-tier conflict: %+v", conflicts)
	}
	if found.Winner != "pane.mute" || found.Loser != "pane.close" {
		t.Errorf("winner/loser = %q/%q", found.Winner, found.Loser)
	}
}

func TestBuild_HardcodedCollision(t *testing.T) {
	// f8 and ctrl+alt+v are included: they are paste aliases handled outside
	// the registry, so binding another action to them silently loses.
	// shift+left/right/up/down, ctrl+shift+left/right, and
	// ctrl+alt+shift+left/right are the selection-extend chords claimed by
	// isSelectionExtendKey (internal/tui/model.go), unguarded ahead of the
	// late-tier lookup.
	for _, key := range []string{
		"f1", "ctrl+n", "alt+1", "alt+9", "f8", "ctrl+alt+v",
		"shift+left", "shift+right", "shift+up", "shift+down",
		"ctrl+shift+left", "ctrl+shift+right",
		"ctrl+alt+shift+left", "ctrl+alt+shift+right",
	} {
		t.Run(key, func(t *testing.T) {
			_, conflicts := Build(map[ActionID]string{"pane.close": key})
			var found bool
			for _, c := range conflicts {
				if c.Kind == ConflictHardcoded && c.Key == key {
					found = true
				}
			}
			if !found {
				t.Errorf("binding %q produced no hardcoded conflict: %+v", key, conflicts)
			}
		})
	}
}

func TestBuild_NoFalsePositives(t *testing.T) {
	// alt+0 is NOT intercepted — only alt+1..alt+9 are.
	_, conflicts := Build(map[ActionID]string{"pane.close": "alt+0"})
	if len(conflicts) != 0 {
		t.Errorf("alt+0 reported a conflict: %+v", conflicts)
	}
}

func TestBuild_ShippedDefaultsAreClean(t *testing.T) {
	specs := make(map[ActionID]string)
	for _, a := range Actions() {
		specs[a.ID] = a.Default
	}
	if _, conflicts := Build(specs); len(conflicts) != 0 {
		t.Errorf("shipped defaults conflict: %+v", conflicts)
	}
}

func TestConflict_StringIsActionable(t *testing.T) {
	c := Conflict{Kind: ConflictDuplicate, Key: "ctrl+w", Winner: "pane.close", Loser: "tab.close"}
	got := c.String()
	for _, want := range []string{"ctrl+w", "pane.close", "tab.close"} {
		if !strings.Contains(got, want) {
			t.Errorf("Conflict.String() = %q, missing %q", got, want)
		}
	}
}

// TestConflict_StringIncludesKindLabel pins that every branch of
// Conflict.String() actually renders the ConflictKind label, not just the
// key/action names the format string supplies on its own. Those names come
// from the format string regardless of what ConflictKind.String() returns,
// so a prior version of this suite would pass even if ConflictKind.String()
// returned "" for every case — this test asserts the label text itself.
func TestConflict_StringIncludesKindLabel(t *testing.T) {
	tests := []struct {
		name string
		c    Conflict
		want string
	}{
		{"duplicate", Conflict{Kind: ConflictDuplicate, Key: "ctrl+w", Winner: "pane.close", Loser: "tab.close"}, "duplicate binding"},
		{"cross-tier", Conflict{Kind: ConflictCrossTier, Key: "alt+z", Winner: "pane.mute", Loser: "pane.close"}, "cross-tier shadowing"},
		{"hardcoded", Conflict{Kind: ConflictHardcoded, Key: "f1", Loser: "pane.close"}, "collides with a built-in key"},
		{"malformed", Conflict{Kind: ConflictMalformed, Key: "ctrl+w", Loser: "pane.close", Detail: "bad spec"}, "unreadable binding"},
		{"unknown-action", Conflict{Kind: ConflictUnknownAction, Loser: "made.up"}, "unknown action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.c.String()
			if !strings.Contains(got, tt.want) {
				t.Errorf("Conflict.String() = %q, missing kind label %q", got, tt.want)
			}
		})
	}
}

// TestBuild_ShadowingOrderIsDeterministic pins detectShadowing's full sort
// (Key, then Kind, then Loser). Binding an early- and a late-tier action to
// the same hardcoded key produces three conflicts sharing one Key, forcing
// both the Kind leg (cross-tier sorts before hardcoded) and the Loser leg
// (the two hardcoded entries differ only there) — not just the Key leg every
// other test in this file exercises.
func TestBuild_ShadowingOrderIsDeterministic(t *testing.T) {
	_, conflicts := Build(map[ActionID]string{
		"pane.mute":  "f1", // early
		"pane.close": "f1", // late; f1 is also hardcoded (F1 -> Shortcuts)
	})
	want := []Conflict{
		{Kind: ConflictCrossTier, Key: "f1", Winner: "pane.mute", Loser: "pane.close"},
		{Kind: ConflictHardcoded, Key: "f1", Loser: "pane.close"},
		{Kind: ConflictHardcoded, Key: "f1", Loser: "pane.mute"},
	}
	if len(conflicts) != len(want) {
		t.Fatalf("got %d conflicts, want %d: %+v", len(conflicts), len(want), conflicts)
	}
	for i, w := range want {
		if conflicts[i] != w {
			t.Errorf("conflicts[%d] = %+v, want %+v", i, conflicts[i], w)
		}
	}
}
