package keymap

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func webAction(t *testing.T, w WebKeymap, id string) WebAction {
	t.Helper()
	for _, a := range w.Actions {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("action %q missing from the web keymap", id)
	return WebAction{}
}

func webBuiltin(t *testing.T, w WebKeymap, id string) WebBuiltin {
	t.Helper()
	for _, b := range w.Builtins {
		if b.ID == id {
			return b
		}
	}
	t.Fatalf("builtin %q missing", id)
	return WebBuiltin{}
}

// The table lives outside presets/, so it is never offered as a preset.
func TestWebFallbacks_AreNotAPreset(t *testing.T) {
	for _, n := range PresetNames() {
		if strings.Contains(n, "fallback") || strings.Contains(n, "web") {
			t.Errorf("PresetNames lists %q", n)
		}
	}
}

func TestWebFallbacks_TableIsSound(t *testing.T) {
	reserved := map[string]bool{}
	for _, r := range BrowserReserved {
		c, err := ParseChord(r)
		if err != nil {
			t.Fatalf("reserved %q does not parse: %v", r, err)
		}
		reserved[c.String()] = true
	}
	fb := WebFallbacks()
	if len(fb) == 0 {
		t.Fatal("the shipped table is empty")
	}
	seen := map[string]string{}
	for id, chord := range fb {
		if _, ok := Lookup(ActionID(id)); !ok && !strings.HasPrefix(id, "builtin.") {
			t.Errorf("%q is neither an action nor a builtin", id)
		}
		c, err := ParseChord(chord)
		if err != nil {
			t.Errorf("%s: %q does not parse: %v", id, chord, err)
			continue
		}
		if reserved[c.String()] {
			t.Errorf("%s falls back to %q, which the browser also keeps", id, chord)
		}
		if prev, dup := seen[c.String()]; dup {
			t.Errorf("%s and %s share fallback %q", id, prev, chord)
		}
		seen[c.String()] = id
	}
}

// Every fallback must be free in the shipped default keymap, or the browser's
// own default would report "web fallback unavailable".
func TestWebFallbacks_FreeInTheDefaultAndTmuxKeymaps(t *testing.T) {
	for _, preset := range []string{"", "tmux"} {
		km := FromSettings(Settings{Preset: preset}).Keymap
		for id, chord := range WebFallbacks() {
			if !km.chordFree(chord) {
				t.Errorf("preset %q: %s's fallback %q is already bound", preset, id, chord)
			}
		}
	}
}

func TestForWeb_DefaultSwapsReservedChordsForFallbacks(t *testing.T) {
	w := ForWeb(FromSettings(Settings{}))
	close := webAction(t, w, "pane.close")
	if !slices.Equal(close.Keys, []string{"alt+shift+c"}) || close.Fallback != "alt+shift+c" {
		t.Errorf("pane.close = %+v, want only the fallback", close)
	}
	if nt := webAction(t, w, "tab.new"); !slices.Equal(nt.Keys, []string{"alt+shift+t"}) {
		t.Errorf("tab.new keys = %v", nt.Keys)
	}
	if np := webBuiltin(t, w, "new_pane"); !slices.Equal(np.Keys, []string{"alt+shift+o"}) {
		t.Errorf("new_pane keys = %v", np.Keys)
	}
	if h := webBuiltin(t, w, "help"); !slices.Equal(h.Keys, []string{"f1"}) {
		t.Errorf("help keys = %v", h.Keys)
	}
	if r := webAction(t, w, "pane.rename"); !slices.Equal(r.Keys, []string{"alt+f2", "alt+shift+r"}) {
		t.Errorf("pane.rename keys = %v, want both alternatives", r.Keys)
	}
}

// A fallback never takes a chord the user bound to something else.
func TestForWeb_FallbackSkippedWhenItsChordIsBound(t *testing.T) {
	w := ForWeb(FromSettings(Settings{Overrides: map[ActionID]string{"pane.mute": "alt+shift+c"}}))
	close := webAction(t, w, "pane.close")
	if len(close.Keys) != 0 || !close.FallbackUnavailable || close.Fallback != "" {
		t.Errorf("pane.close = %+v, want no key and fallback_unavailable", close)
	}
	if m := webAction(t, w, "pane.mute"); !slices.Equal(m.Keys, []string{"alt+shift+c"}) {
		t.Errorf("pane.mute lost its chord: %v", m.Keys)
	}
}

// A builtin loses its chord to a bound action, as handleKey checks it last.
func TestForWeb_BuiltinLosesItsChordToAnAction(t *testing.T) {
	w := ForWeb(FromSettings(Settings{Overrides: map[ActionID]string{"pane.mute": "f1"}}))
	if h := webBuiltin(t, w, "help"); len(h.Keys) != 0 {
		t.Errorf("help keys = %v, want none: pane.mute holds f1", h.Keys)
	}
}

func TestForWeb_TmuxSequencesNeedNoFallback(t *testing.T) {
	w := ForWeb(FromSettings(Settings{Preset: "tmux"}))
	if w.Preset != "tmux" || w.Prefix != "ctrl+b" {
		t.Errorf("preset/prefix = %q/%q", w.Preset, w.Prefix)
	}
	if s := webAction(t, w, "pane.split_h"); !slices.Equal(s.Keys, []string{"ctrl+b %"}) || s.Tier != "late" {
		t.Errorf("pane.split_h = %+v", s)
	}
	if c := webAction(t, w, "pane.close"); c.Fallback != "" || !slices.Equal(c.Keys, []string{"ctrl+b x"}) {
		t.Errorf("pane.close = %+v", c)
	}
}

// The loser of a duplicate never fires in the TUI, so it gets no key here.
func TestForWeb_DuplicateLoserHasNoKey(t *testing.T) {
	w := ForWeb(FromSettings(Settings{Overrides: map[ActionID]string{"pane.mute": "alt+n"}}))
	if m := webAction(t, w, "pane.mute"); slices.Contains(m.Keys, "alt+n") {
		t.Errorf("pane.mute keeps alt+n although notification.toggle wins it: %v", m.Keys)
	}
	if n := webAction(t, w, "notification.toggle"); !slices.Equal(n.Keys, []string{"alt+n"}) {
		t.Errorf("notification.toggle = %v", n.Keys)
	}
}

// A late chord that an early action also claims never fires in the TUI.
func TestForWeb_CrossTierLoserHasNoKey(t *testing.T) {
	w := ForWeb(FromSettings(Settings{Overrides: map[ActionID]string{"pane.restart": "alt+n"}}))
	if r := webAction(t, w, "pane.restart"); slices.Contains(r.Keys, "alt+n") {
		t.Errorf("pane.restart keeps alt+n although the early notification.toggle wins it: %v", r.Keys)
	}
}

func TestForWeb_HiddenActionsAndShape(t *testing.T) {
	w := ForWeb(FromSettings(Settings{}))
	for _, a := range w.Actions {
		if a.ID == "json.transform" {
			t.Error("a hidden action is listed")
		}
		if a.Keys == nil {
			t.Errorf("%s: keys is nil; the page expects [] for none", a.ID)
		}
	}
	data, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"preset"`, `"prefix"`, `"timeout_ms"`, `"actions"`, `"builtins"`, `"conflicts"`, `"group_order"`, `"tier":"early"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("JSON lacks %s", field)
		}
	}
}
