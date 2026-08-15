package keymap

import (
	"strings"
	"testing"
)

func TestBuild_MatchesByTier(t *testing.T) {
	km, conflicts := Build(map[ActionID]string{
		"pane.mute": "alt+m", "pane.close": "ctrl+w",
	})
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if id, ok := km.MatchTier(TierEarly, "alt+m"); !ok || id != "pane.mute" {
		t.Errorf("early alt+m = (%q,%v)", id, ok)
	}
	// The early tier must NOT see a late action, or a plugin raw_keys entry
	// that should beat it gets bypassed.
	if _, ok := km.MatchTier(TierEarly, "ctrl+w"); ok {
		t.Error("early tier matched a late action")
	}
	if id, ok := km.MatchTier(TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Errorf("late ctrl+w = (%q,%v)", id, ok)
	}
}

func TestMatchTier_CanonicalizesInput(t *testing.T) {
	km, _ := Build(map[ActionID]string{"pane.close": "ctrl+shift+w"})
	if _, ok := km.MatchTier(TierLate, "shift+ctrl+w"); !ok {
		t.Error("MatchTier did not canonicalize its input")
	}
}

func TestBuild_MultiBindingAlternatives(t *testing.T) {
	km, _ := Build(map[ActionID]string{"pane.rename": "alt+f2,alt+shift+r"})
	for _, key := range []string{"alt+f2", "alt+shift+r"} {
		if id, ok := km.MatchTier(TierLate, key); !ok || id != "pane.rename" {
			t.Errorf("late %q = (%q,%v)", key, id, ok)
		}
	}
}

func TestBuild_MultiStepSequenceNeverMatchesItsFirstChord(t *testing.T) {
	// Stage 1 has no prefix machine. A multi-step binding parses and stores
	// but must never fire off its opening chord alone.
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c"})
	if _, ok := km.MatchTier(TierLate, "ctrl+b"); ok {
		t.Error("a multi-step sequence matched on its first chord")
	}
}

// TestBuild_MultiStepSequenceIsReportedAsUnsupported is the other half of the
// test above, and the reason it exists is that the two together are the whole
// contract: the sequence must not dispatch AND must not be advertised as
// working. It stays in bindings, so Display renders "ctrl+b c" in F1 exactly
// like a binding that fires — the conflict row is the only thing that says
// otherwise, and this file's governing principle is that a silently dropped
// binding is worse than a loud one.
func TestBuild_MultiStepSequenceIsReportedAsUnsupported(t *testing.T) {
	km, conflicts := Build(map[ActionID]string{"tab.new": "ctrl+b c"})
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictUnsupportedSequence {
		t.Fatalf("conflicts = %+v, want one ConflictUnsupportedSequence", conflicts)
	}
	if got := conflicts[0].Key; got != "ctrl+b c" {
		t.Errorf("conflict Key = %q, want the whole sequence", got)
	}
	if got := conflicts[0].Loser; got != "tab.new" {
		t.Errorf("conflict Loser = %q, want tab.new", got)
	}
	// The user sees the message, not the struct: it has to name the sequence
	// and the action, or the F1 row says a key is broken without saying which.
	msg := conflicts[0].String()
	for _, want := range []string{"sequence not supported yet", `"ctrl+b c"`, "tab.new", "never fires"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Conflict.String() = %q, missing %q", msg, want)
		}
	}
	// And Display still offers it — which is exactly why the conflict is
	// needed. If this ever stops being true the conflict can go with it.
	if got := km.Display("tab.new"); got != "ctrl+b c" {
		t.Errorf("Display = %q, want the sequence still advertised", got)
	}
}

// TestBuild_SingleChordSpecsProduceNoSequenceConflict is the control: the
// unsupported-sequence report must fire on multi-step specs only. Without it a
// `continue` that reported every binding would still pass the test above.
func TestBuild_SingleChordSpecsProduceNoSequenceConflict(t *testing.T) {
	_, conflicts := Build(map[ActionID]string{"pane.rename": "alt+f2,alt+shift+r"})
	for _, c := range conflicts {
		if c.Kind == ConflictUnsupportedSequence {
			t.Errorf("a comma-separated alternative was reported as a sequence: %+v", c)
		}
	}
}

func TestBuild_MalformedSpecFallsBackPerAction(t *testing.T) {
	km, conflicts := Build(map[ActionID]string{
		"app.quit":   "super+", // malformed
		"pane.close": "ctrl+w", // must survive untouched
	})
	var malformed bool
	for _, c := range conflicts {
		if c.Kind == ConflictMalformed && c.Loser == "app.quit" {
			malformed = true
		}
	}
	if !malformed {
		t.Errorf("no ConflictMalformed for app.quit: %+v", conflicts)
	}
	// app.quit falls back to ITS default, not to nothing.
	if id, ok := km.MatchTier(TierLate, "ctrl+q"); !ok || id != "app.quit" {
		t.Error("app.quit did not fall back to its default binding")
	}
	// The unrelated binding is untouched — this is the whole point.
	if id, ok := km.MatchTier(TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Error("a malformed spec discarded an unrelated binding")
	}
}

func TestBuild_DuplicateWinnerFollowsLegacyCaseOrder(t *testing.T) {
	// tab.rename (Order 2700) precedes app.redraw (Order 3000) in the legacy
	// switch, so tab.rename must win. An alphabetical ID sort flips this.
	km, conflicts := Build(map[ActionID]string{
		"tab.rename": "ctrl+y", "app.redraw": "ctrl+y",
	})
	if id, _ := km.MatchTier(TierLate, "ctrl+y"); id != "tab.rename" {
		t.Errorf("ctrl+y = %q, want tab.rename (lower Order wins)", id)
	}
	if len(conflicts) != 1 || conflicts[0].Winner != "tab.rename" || conflicts[0].Loser != "app.redraw" {
		t.Errorf("conflicts = %+v", conflicts)
	}
}

func TestKeymap_NilReceiverIsSafe(t *testing.T) {
	// ~26 TUI test files build Model literals with no cfg and drive handleKey.
	// Every accessor must answer "nothing" rather than panic.
	var km *Keymap
	if _, ok := km.MatchTier(TierLate, "ctrl+q"); ok {
		t.Error("nil MatchTier reported a match")
	}
	if km.Bindings("app.quit") != nil {
		t.Error("nil Bindings returned non-nil")
	}
	if km.Display("app.quit") != "" {
		t.Error("nil Display returned non-empty")
	}
	if km.Keys("app.quit") != nil {
		t.Error("nil Keys returned non-nil")
	}
}

func TestDisplayAndKeys(t *testing.T) {
	km, _ := Build(map[ActionID]string{"pane.rename": "alt+f2,alt+shift+r"})
	if got, want := km.Display("pane.rename"), "alt+f2 / alt+shift+r"; got != want {
		t.Errorf("Display = %q, want %q", got, want)
	}
	if got := km.Keys("pane.rename"); len(got) != 2 || got[0] != "alt+f2" {
		t.Errorf("Keys = %v", got)
	}
	if km.Display("pane.next") != "" {
		t.Error("unbound action has a display string")
	}
}

func TestBuild_UnknownActionIDIsIgnoredWithAConflict(t *testing.T) {
	_, conflicts := Build(map[ActionID]string{"nope.nope": "ctrl+z"})
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictUnknownAction {
		t.Fatalf("conflicts = %+v, want one ConflictUnknownAction", conflicts)
	}
	// The chord rides along so the message can point at a line. Unreachable
	// while keySpecsFromConfig emits 41 literal IDs; Stage 3's bindings.toml
	// makes a typo'd ID the most common failure there is, and an ID alone does
	// not say which line of the file to look at.
	if got := conflicts[0].Key; got != "ctrl+z" {
		t.Errorf("conflict Key = %q, want the offending spec %q", got, "ctrl+z")
	}
	if msg := conflicts[0].String(); !strings.Contains(msg, "ctrl+z") {
		t.Errorf("Conflict.String() = %q, never names the chord", msg)
	}
}

func TestBuild_UnknownActionConflictsAreSortedByID(t *testing.T) {
	// Two or more unregistered IDs must come back in a deterministic order —
	// not merely "two conflicts exist" — or the list reshuffles between
	// process launches, since map iteration is randomized.
	_, conflicts := Build(map[ActionID]string{
		"zzz.unknown": "ctrl+z", "aaa.unknown": "ctrl+a", "mmm.unknown": "ctrl+m",
	})
	if len(conflicts) != 3 {
		t.Fatalf("conflicts = %+v, want 3", conflicts)
	}
	want := []ActionID{"aaa.unknown", "mmm.unknown", "zzz.unknown"}
	for i, id := range want {
		if conflicts[i].Kind != ConflictUnknownAction || conflicts[i].Loser != id {
			t.Errorf("conflicts[%d] = %+v, want Loser %q", i, conflicts[i], id)
		}
	}
}
