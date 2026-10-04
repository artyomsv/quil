package keymap

import (
	"strings"
	"testing"
	"time"
)

func seqOf(t *testing.T, spec string) []Chord {
	t.Helper()
	seqs, err := ParseSpec(spec)
	if err != nil || len(seqs) != 1 {
		t.Fatalf("ParseSpec(%q) = %v, %v", spec, seqs, err)
	}
	return seqs[0]
}

func TestFromSettings_DefaultIsTheRegistry(t *testing.T) {
	r := FromSettings(Settings{})
	if r.Preset != DefaultPresetName {
		t.Errorf("Preset = %q, want %q", r.Preset, DefaultPresetName)
	}
	if id, ok := r.Keymap.MatchTier(TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Errorf("ctrl+w = (%q,%v), want pane.close", id, ok)
	}
}

func TestFromSettings_TmuxTakesItsOwnPrefixAndTimeout(t *testing.T) {
	r := FromSettings(Settings{Preset: "tmux"})
	if r.Prefix != "ctrl+b" {
		t.Errorf("Prefix = %q, want the preset's ctrl+b", r.Prefix)
	}
	if id, kind := r.Keymap.MatchSeq(seqOf(t, "ctrl+b %")); kind != MatchExact || id != "pane.split_h" {
		t.Errorf("ctrl+b %% = (%q,%v), want pane.split_h exact", id, kind)
	}
	if _, ok := r.Keymap.MatchTier(TierLate, "ctrl+w"); ok {
		t.Error("tmux replaces pane.close; ctrl+w must be unbound")
	}
}

func TestFromSettings_UserPrefixWins(t *testing.T) {
	r := FromSettings(Settings{Preset: "tmux", Prefix: "ctrl+a"})
	if id, kind := r.Keymap.MatchSeq(seqOf(t, "ctrl+a c")); kind != MatchExact || id != "tab.new" {
		t.Errorf("ctrl+a c = (%q,%v), want tab.new", id, kind)
	}
}

func TestFromSettings_UnknownPresetWarnsAndKeepsDefaults(t *testing.T) {
	r := FromSettings(Settings{Preset: "no-such"})
	if r.Preset != DefaultPresetName || len(r.Warnings) == 0 {
		t.Errorf("Preset=%q warnings=%v, want default + a warning", r.Preset, r.Warnings)
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "no-such") {
		t.Errorf("warning does not name the preset: %v", r.Warnings)
	}
}

func TestFromSettings_UserTimeoutBeatsThePresets(t *testing.T) {
	r := FromSettings(Settings{Preset: "tmux", Timeout: 700 * time.Millisecond})
	if r.Timeout != 700*time.Millisecond {
		t.Errorf("Timeout = %v, want the user's 700ms", r.Timeout)
	}
}

func TestFromSettings_OverrideReclaimsThePrefixAndReportsIt(t *testing.T) {
	r := FromSettings(Settings{Preset: "tmux", Overrides: map[ActionID]string{"pane.rename": "ctrl+b"}})
	if id, ok := r.Keymap.MatchTier(TierLate, "ctrl+b"); !ok || id != "pane.rename" {
		t.Errorf("ctrl+b = (%q,%v), want pane.rename", id, ok)
	}
	found := false
	for _, c := range r.Conflicts {
		if c.Kind == ConflictShadowed {
			found = true
		}
	}
	if !found {
		t.Errorf("no shadowing conflict reported: %v", r.Conflicts)
	}
}

func TestValidatePrefix(t *testing.T) {
	if got, err := ValidatePrefix("Ctrl+A"); err != nil || got != "ctrl+A" && got != "ctrl+a" {
		// single-rune base keys keep their case (chord.go:60); "Ctrl+A" stays "ctrl+A"
		t.Errorf("ValidatePrefix(Ctrl+A) = %q, %v", got, err)
	}
	for _, bad := range []string{"", "ctrl+a, ctrl+b", "ctrl+a b", "ctrl+"} {
		if _, err := ValidatePrefix(bad); err == nil {
			t.Errorf("ValidatePrefix(%q) accepted", bad)
		}
	}
}
