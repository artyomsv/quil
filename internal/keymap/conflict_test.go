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
		{"unknown-action", Conflict{Kind: ConflictUnknownAction, Key: "ctrl+z", Loser: "made.up"}, "unknown action"},
		{"unsupported-sequence", Conflict{Kind: ConflictUnsupportedSequence, Key: "ctrl+b c", Loser: "tab.new"}, "sequence not supported yet"},
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

// TestConflict_HardcodedNamesTheRealWinner is the regression for a message
// that was backwards for 13 of the 21 hardcoded keys: it said "Quil intercepts
// it first" for every one of them, while f1, ctrl+n, alt+1..9 and the f8 /
// ctrl+alt+v paste aliases are checked only once BOTH tier switches have
// declined the key — there the bound action wins and the built-in dies.
// internal/tui's TestHandleKey_PasteAliasesLoseToLateActions pins that
// dispatch order from the other side.
//
// Each case asserts the winner is named as winning AND that the loser is not,
// because a message that mentions both names passes a substring check in
// either direction.
func TestConflict_HardcodedNamesTheRealWinner(t *testing.T) {
	tests := []struct {
		name   string
		action ActionID
		key    string
		winner string
		loser  string
	}{
		// After both tier switches: the action wins whichever tier it is on.
		{"late action on a paste alias", "pane.restart", "f8", "pane.restart", "paste"},
		{"early action on f1", "pane.mute", "f1", "pane.mute", "help"},
		{"late action on alt+1", "pane.close", "alt+1", "pane.close", "tab 1"},
		{"early action on ctrl+n", "pane.mute", "ctrl+n", "pane.mute", "new pane"},
		// Between the tiers: isSelectionExtendKey runs after the early switch
		// and before the late one, so the tier decides.
		{"early action on a selection chord", "pane.mute", "shift+left", "pane.mute", "text selection"},
		{"late action on a selection chord", "pane.close", "ctrl+shift+right", "text selection", "pane.close"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, conflicts := Build(map[ActionID]string{tt.action: tt.key})
			var got string
			for _, c := range conflicts {
				if c.Kind == ConflictHardcoded && c.Key == tt.key {
					got = c.String()
				}
			}
			if got == "" {
				t.Fatalf("no hardcoded conflict for %q: %+v", tt.key, conflicts)
			}
			if !strings.Contains(got, tt.winner+" wins") {
				t.Errorf("Conflict.String() = %q, does not say %q wins", got, tt.winner)
			}
			if strings.Contains(got, tt.loser+" wins") {
				t.Errorf("Conflict.String() = %q, says the losing side %q wins", got, tt.loser)
			}
			if !strings.Contains(got, tt.loser) {
				t.Errorf("Conflict.String() = %q, never names the losing side %q", got, tt.loser)
			}
		})
	}
}

// TestConflict_HardcodedUnknownKeyClaimsNoWinner: a Conflict built outside
// detectShadowing has no dispatch position to derive from, so the message must
// stop at what it knows rather than guess a direction.
func TestConflict_HardcodedUnknownKeyClaimsNoWinner(t *testing.T) {
	got := Conflict{Kind: ConflictHardcoded, Key: "ctrl+alt+z", Loser: "pane.close"}.String()
	if strings.Contains(got, "wins") || strings.Contains(got, "never fires") {
		t.Errorf("Conflict.String() = %q, asserts an outcome it cannot know", got)
	}
	for _, want := range []string{"ctrl+alt+z", "pane.close"} {
		if !strings.Contains(got, want) {
			t.Errorf("Conflict.String() = %q, missing %q", got, want)
		}
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
