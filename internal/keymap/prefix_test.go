package keymap

import (
	"strings"
	"testing"
)

func TestExpandPrefix_Expands(t *testing.T) {
	got, conflicts := ExpandPrefix(map[ActionID]string{"tab.new": "${prefix} c"}, "ctrl+b")
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	if got["tab.new"] != "ctrl+b c" {
		t.Errorf("got %q, want %q", got["tab.new"], "ctrl+b c")
	}
}

// Modifiers are reordered and folded to lowercase; a one-rune base key keeps
// its case, because ParseChord deliberately does not fold it — bubbletea's
// ESC-prefix Meta decoding reports Option+Shift+M as {Code:'M', Mod:ModAlt},
// so folding would hand every shifted Meta letter to the lowercase binding.
func TestExpandPrefix_Canonicalizes(t *testing.T) {
	got, _ := ExpandPrefix(map[ActionID]string{"tab.new": "${prefix} c"}, "Shift+Ctrl+A")
	if got["tab.new"] != "ctrl+shift+A c" {
		t.Errorf("the prefix must be canonicalized before expansion, got %q", got["tab.new"])
	}
}

func TestExpandPrefix_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"unset while referenced", ""},
		{"comma", "ctrl+a, ctrl+b"},
		{"space", "ctrl+a b"},
		{"not a chord", "ctrl+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conflicts := ExpandPrefix(map[ActionID]string{"tab.new": "${prefix} c"}, tt.prefix)
			var found bool
			for _, c := range conflicts {
				if c.Kind == ConflictPrefixInvalid {
					found = true
				}
			}
			if !found {
				t.Errorf("want a ConflictPrefixInvalid, got %v", conflicts)
			}
			if _, present := got["tab.new"]; present {
				t.Error("a binding referencing an invalid prefix must be dropped, not half-expanded")
			}
		})
	}
}

// A spec that never mentions ${prefix} is untouched, and an unset prefix is not
// an error for it.
func TestExpandPrefix_UnreferencedIsInert(t *testing.T) {
	got, conflicts := ExpandPrefix(map[ActionID]string{"tab.new": "ctrl+t"}, "")
	if len(conflicts) != 0 {
		t.Fatalf("an unreferenced prefix must not conflict: %v", conflicts)
	}
	if got["tab.new"] != "ctrl+t" {
		t.Errorf("got %q, want it untouched", got["tab.new"])
	}
}

func TestExpandPrefix_EmptyLayerStaysNil(t *testing.T) {
	got, _ := ExpandPrefix(nil, "ctrl+b")
	if got != nil {
		t.Errorf("a nil layer must stay nil so Resolve can tell it from an empty preset, got %v", got)
	}
}

func TestPrefixWarning(t *testing.T) {
	if got := PrefixWarning("a"); got == "" {
		t.Error("an unmodified printable prefix must warn — it is swallowed globally")
	}
	if got := PrefixWarning("ctrl+b"); got != "" {
		t.Errorf("a modified prefix must not warn, got %q", got)
	}
	if got := PrefixWarning("f2"); got != "" {
		t.Errorf("a named key is not a swallowed letter, got %q", got)
	}
	if got := PrefixWarning(""); got != "" {
		t.Errorf("an unset prefix is validatePrefix's problem, not a warning, got %q", got)
	}
}

// The comma key is only reachable through the alias: "," is the alternatives
// separator, so a spec containing one splits before any chord parsing happens.
// tmux binds rename-window to prefix-then-comma, so this is load-bearing for
// the preset rather than a completeness exercise.
func TestParseSpec_CommaKeyViaAlias(t *testing.T) {
	seqs, err := ParseSpec("ctrl+b comma")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if len(seqs) != 1 || len(seqs[0]) != 2 {
		t.Fatalf("got %v, want one two-chord sequence", seqs)
	}
	if got := seqs[0][1].Key; got != "," {
		t.Errorf("base key = %q, want the literal comma", got)
	}
	// The canonical rendering must be the bare symbol, because that is what
	// bubbletea reports for a real press.
	if got := seqs[0].String(); got != "ctrl+b ," {
		t.Errorf("Sequence.String() = %q, want %q", got, "ctrl+b ,")
	}
}

// The literal form stays broken, and that is worth pinning: it is why the alias
// exists, and a future parser change that "fixes" it would silently give two
// spellings with different meanings.
func TestParseSpec_LiteralCommaStillSplits(t *testing.T) {
	_, err := ParseSpec("ctrl+b ,")
	if err == nil {
		t.Fatal("a literal comma must still be read as the alternatives separator")
	}
	if !strings.Contains(err.Error(), "empty alternative") {
		t.Errorf("err = %v, want an empty-alternative complaint", err)
	}
}
