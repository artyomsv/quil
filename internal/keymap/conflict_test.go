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
	for _, key := range []string{"f1", "ctrl+n", "alt+1", "alt+9", "f8", "ctrl+alt+v"} {
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
