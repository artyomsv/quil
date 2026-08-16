package keymap

import "testing"

// mustChords parses a single-alternative spec into its chords.
func mustChords(t *testing.T, spec string) []Chord {
	t.Helper()
	seqs, err := ParseSpec(spec)
	if err != nil {
		t.Fatalf("ParseSpec(%q): %v", spec, err)
	}
	if len(seqs) != 1 {
		t.Fatalf("ParseSpec(%q) returned %d alternatives, want 1", spec, len(seqs))
	}
	return seqs[0]
}

func TestMatchSeq_PartialThenExact(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c"})

	if id, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchPartial {
		t.Errorf("MatchSeq(ctrl+b) = (%q, %v), want MatchPartial", id, kind)
	}
	id, kind := km.MatchSeq(mustChords(t, "ctrl+b c"))
	if kind != MatchExact || id != "tab.new" {
		t.Errorf("MatchSeq(ctrl+b c) = (%q, %v), want (tab.new, MatchExact)", id, kind)
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b z")); kind != MatchNone {
		t.Errorf("MatchSeq(ctrl+b z) = %v, want MatchNone", kind)
	}
}

// A single-chord binding must never report Partial. This is the property that
// makes the sequence machine safe to merge: if it ever fails, every existing
// binding starts swallowing its own keypress instead of firing.
func TestMatchSeq_SingleChordIsNeverPartial(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+t"})
	if id, kind := km.MatchSeq(mustChords(t, "ctrl+t")); kind != MatchExact || id != "tab.new" {
		t.Fatalf("MatchSeq(ctrl+t) = (%q, %v), want (tab.new, MatchExact)", id, kind)
	}
}

// Tier-agnostic: pane.close is TierLate, and its opening chord is in neither
// tier's chord map. A tier-scoped probe answers MatchNone here, the key falls
// through to the PTY, and the sequence can never complete however many times
// the second chord is pressed.
func TestMatchSeq_IsTierAgnostic(t *testing.T) {
	if a, _ := Lookup("pane.close"); a.Tier != TierLate {
		t.Fatalf("fixture assumes pane.close is TierLate, got %v", a.Tier)
	}
	km, _ := Build(map[ActionID]string{"pane.close": "ctrl+b x"})
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchPartial {
		t.Errorf("a late-tier sequence head must report MatchPartial, got %v", kind)
	}
}

// Two sequences sharing a head: both complete, and the shared prefix stays
// partial rather than resolving to whichever was inserted first.
func TestMatchSeq_SharedPrefix(t *testing.T) {
	km, _ := Build(map[ActionID]string{
		"tab.new":    "ctrl+b c",
		"pane.close": "ctrl+b x",
	})
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchPartial {
		t.Errorf("a shared head must stay MatchPartial, got %v", kind)
	}
	if id, kind := km.MatchSeq(mustChords(t, "ctrl+b c")); kind != MatchExact || id != "tab.new" {
		t.Errorf("MatchSeq(ctrl+b c) = (%q, %v), want (tab.new, MatchExact)", id, kind)
	}
	if id, kind := km.MatchSeq(mustChords(t, "ctrl+b x")); kind != MatchExact || id != "pane.close" {
		t.Errorf("MatchSeq(ctrl+b x) = (%q, %v), want (pane.close, MatchExact)", id, kind)
	}
}

func TestMatchSeq_EmptyPendingIsNone(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c"})
	if _, kind := km.MatchSeq(nil); kind != MatchNone {
		t.Errorf("MatchSeq(nil) = %v, want MatchNone", kind)
	}
}

// TUI tests build Model literals with no keymap and drive handleKey, so every
// lookup on this type has to survive a nil receiver.
func TestMatchSeq_NilKeymapIsNone(t *testing.T) {
	var km *Keymap
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchNone {
		t.Errorf("nil Keymap must answer MatchNone, got %v", kind)
	}
}

// Three chords, to prove the prefix set records EVERY proper prefix rather
// than only the first chord.
func TestMatchSeq_ThreeStepSequence(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c d"})
	for _, head := range []string{"ctrl+b", "ctrl+b c"} {
		if _, kind := km.MatchSeq(mustChords(t, head)); kind != MatchPartial {
			t.Errorf("MatchSeq(%q) = %v, want MatchPartial", head, kind)
		}
	}
	if id, kind := km.MatchSeq(mustChords(t, "ctrl+b c d")); kind != MatchExact || id != "tab.new" {
		t.Errorf("MatchSeq(ctrl+b c d) = (%q, %v), want (tab.new, MatchExact)", id, kind)
	}
}
