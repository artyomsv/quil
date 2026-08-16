package keymap

import "testing"

func TestResolve_AbsenceInherits(t *testing.T) {
	got := Resolve(
		map[ActionID]string{"tab.new": "ctrl+t", "pane.close": "ctrl+w"},
		map[ActionID]string{"tab.new": "ctrl+b c"},
	)
	if got["pane.close"] != "ctrl+w" {
		t.Errorf("an action the higher layer omits must inherit, got %q", got["pane.close"])
	}
	if got["tab.new"] != "ctrl+b c" {
		t.Errorf("a present binding must replace, got %q", got["tab.new"])
	}
}

// "" is a VALUE — an explicit unbind — not an absence.
func TestResolve_EmptyStringUnbinds(t *testing.T) {
	got := Resolve(
		map[ActionID]string{"project.picker": "alt+p"},
		map[ActionID]string{"project.picker": ""},
	)
	binding, present := got["project.picker"]
	if !present {
		t.Fatal("an explicit unbind must survive as a key, or the lower layer leaks back in")
	}
	if binding != "" {
		t.Errorf("got %q, want an empty binding", binding)
	}
}

// Each layer replaces wholesale; "" does not propagate upward. A preset that
// unbinds an action must not veto the user's own override of it.
func TestResolve_UserOverrideBeatsPresetUnbind(t *testing.T) {
	got := Resolve(
		map[ActionID]string{"project.picker": "alt+p"}, // default
		map[ActionID]string{"project.picker": ""},      // preset unbinds
		map[ActionID]string{"project.picker": "alt+j"}, // user reclaims
	)
	if got["project.picker"] != "alt+j" {
		t.Errorf("the user layer must win, got %q", got["project.picker"])
	}
}

func TestResolve_NoLayersIsEmpty(t *testing.T) {
	if got := Resolve(); len(got) != 0 {
		t.Errorf("Resolve() with no layers = %v, want empty", got)
	}
}

func TestResolve_NilLayerIsSkipped(t *testing.T) {
	got := Resolve(map[ActionID]string{"tab.new": "ctrl+t"}, nil)
	if got["tab.new"] != "ctrl+t" {
		t.Errorf("a nil layer must be inert, got %q", got["tab.new"])
	}
}

// A user override must be able to reclaim the prefix key as a plain chord.
// A length-based rule would let the preset veto it, inverting the layering.
func TestBuildLayered_UserOverrideReclaimsPrefixChord(t *testing.T) {
	km, _ := BuildLayered(
		map[ActionID]string{},                        // base
		map[ActionID]string{"tab.new": "ctrl+b c"},   // preset
		map[ActionID]string{"pane.rename": "ctrl+b"}, // user
	)
	if id, ok := km.MatchTier(TierLate, "ctrl+b"); !ok || id != "pane.rename" {
		t.Errorf("the user's chord must survive, got (%q, %v)", id, ok)
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b c")); kind == MatchExact {
		t.Error("the preset sequence must lose to the higher layer's chord")
	}
	// The loser must not linger in the prefix set either: if it did, ctrl+b
	// would still report Partial and the surviving chord would never fire.
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchExact {
		t.Errorf("ctrl+b must resolve as a chord, got %v", kind)
	}
}

// The same collision inside ONE layer resolves by length instead: the shorter
// is refused, because both sides tie on layer.
func TestBuildLayered_IntraLayerRefusesTheShorter(t *testing.T) {
	km, _ := BuildLayered(
		map[ActionID]string{},
		map[ActionID]string{},
		map[ActionID]string{"pane.rename": "ctrl+b", "tab.new": "ctrl+b c"},
	)
	if _, ok := km.MatchTier(TierLate, "ctrl+b"); ok {
		t.Error("within one layer the shorter binding must be refused")
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b c")); kind != MatchExact {
		t.Error("the longer sequence must survive within one layer")
	}
}

// An ordinary cross-layer rebind is NOT shadowing. Two actions claiming the
// same chord is a duplicate, resolved by registry Order during insertion —
// reporting it as "shadowed by a longer sequence" would fire on every routine
// rebind and say something false about why.
func TestBuildLayered_EqualLengthCollisionIsNotShadowing(t *testing.T) {
	_, conflicts := BuildLayered(
		map[ActionID]string{"tab.new": "ctrl+t"},
		nil,
		map[ActionID]string{"pane.close": "ctrl+t"},
	)
	for _, c := range conflicts {
		if c.Kind == ConflictShadowed {
			t.Errorf("an equal-length chord collision must not be ConflictShadowed: %s", c)
		}
	}
}

// A malformed spec must still reach Build's per-action fallback. Resolving
// shadowing on parsed sequences is what preserves this: a string-level prune
// would have rewritten the spec and hidden it.
func TestBuildLayered_MalformedSpecStillFallsBack(t *testing.T) {
	km, conflicts := BuildLayered(
		map[ActionID]string{"tab.new": "ctrl+"}, // dangling modifier
	)
	var found bool
	for _, c := range conflicts {
		if c.Kind == ConflictMalformed && c.Loser == "tab.new" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a ConflictMalformed for tab.new, got %+v", conflicts)
	}
	if got := km.Display("tab.new"); got != "ctrl+t" {
		t.Errorf("Display(tab.new) = %q, want the shipped default after fallback", got)
	}
}

// Drop granularity is per-alternative: a binding colliding on one alternative
// keeps the others rather than losing everything.
func TestBuildLayered_DropsOnlyTheCollidingAlternative(t *testing.T) {
	km, _ := BuildLayered(
		map[ActionID]string{},
		map[ActionID]string{"tab.new": "ctrl+b c"},
		map[ActionID]string{"pane.rename": "ctrl+b, alt+shift+r"},
	)
	keys := km.Keys("pane.rename")
	if len(keys) != 2 {
		t.Fatalf("Keys(pane.rename) = %v, want both alternatives kept", keys)
	}
}
