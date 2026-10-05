package keymap

import (
	_ "embed"
	"fmt"

	"github.com/BurntSushi/toml"
)

//go:embed webfallback.toml
var webFallbackData []byte

// BrowserReserved are chords a browser never delivers to a page.
var BrowserReserved = []string{"ctrl+w", "ctrl+t", "ctrl+n", "ctrl+tab", "ctrl+shift+t", "ctrl+shift+n", "ctrl+shift+w"}

var webFallbacks = func() map[string]string {
	var f struct {
		Fallbacks map[string]string `toml:"fallbacks"`
	}
	if err := toml.Unmarshal(webFallbackData, &f); err != nil {
		panic(fmt.Sprintf("keymap: webfallback.toml: %v", err)) // shipped file; TestWebFallbacks_TableIsSound pins it
	}
	return f.Fallbacks
}()

// WebFallbacks returns a copy of the shipped table: action id or
// builtin.<name> → chord.
func WebFallbacks() map[string]string {
	out := make(map[string]string, len(webFallbacks))
	for k, v := range webFallbacks {
		out[k] = v
	}
	return out
}

// webBuiltins are the keys handleKey checks after both tiers (hardcodedKeys,
// afterBothTiers) that the browser serves too.
var webBuiltins = []struct{ id, label, chord string }{
	{"help", "Key list", "f1"},
	{"new_pane", "New pane", "ctrl+n"},
}

// WebAction is one registry action as the browser dispatches it.
type WebAction struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	Group               string   `json:"group"`
	Tier                string   `json:"tier"`
	Keys                []string `json:"keys"`
	Fallback            string   `json:"fallback,omitempty"`
	FallbackUnavailable bool     `json:"fallback_unavailable,omitempty"`
}

// WebBuiltin is one hardcoded TUI key the browser serves too.
type WebBuiltin struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	Keys                []string `json:"keys"`
	Fallback            string   `json:"fallback,omitempty"`
	FallbackUnavailable bool     `json:"fallback_unavailable,omitempty"`
}

// WebKeymap is the resolved keymap as the browser needs it: every binding
// that wins dispatch in the TUI, with browser-reserved chords swapped for
// their fallbacks.
type WebKeymap struct {
	Preset     string       `json:"preset"`
	Prefix     string       `json:"prefix"`
	TimeoutMs  int64        `json:"timeout_ms"`
	GroupOrder []string     `json:"group_order"`
	Actions    []WebAction  `json:"actions"`
	Builtins   []WebBuiltin `json:"builtins"`
	Conflicts  []string     `json:"conflicts"`
}

// winning lists an action's bindings that actually dispatch: a duplicate's
// loser and a late chord shadowed by an early one are left out.
func (k *Keymap) winning(a Action) []string {
	out := []string{}
	for _, seq := range k.Bindings(a.ID) {
		s := seq.String()
		if len(seq) > 1 {
			if id, kind := k.MatchSeq(seq); kind == MatchExact && id == a.ID {
				out = append(out, s)
			}
			continue
		}
		if id, ok := k.MatchTier(a.Tier, s); !ok || id != a.ID {
			continue
		}
		if a.Tier == TierLate {
			if _, early := k.MatchTier(TierEarly, s); early {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// chordFree reports whether a single chord is unbound in every sense the
// dispatcher knows: no action, no sequence head, no hardcoded key.
func (k *Keymap) chordFree(chord string) bool {
	c, err := ParseChord(chord)
	if err != nil {
		return false
	}
	if !k.chordFreeOfActions(chord) {
		return false
	}
	_, hard := hardcodedKeys[c.String()]
	return !hard
}

// chordFreeOfActions is chordFree without the hardcoded-key check, for the
// builtins whose own chord IS a hardcoded key.
func (k *Keymap) chordFreeOfActions(chord string) bool {
	c, err := ParseChord(chord)
	if err != nil {
		return false
	}
	_, kind := k.MatchSeq([]Chord{c})
	return kind == MatchNone
}

func isReserved(key string) bool {
	for _, r := range BrowserReserved {
		if c, err := ParseChord(r); err == nil && c.String() == key {
			return true
		}
	}
	return false
}

// swap removes browser-reserved single chords from keys and, when one was
// removed, adds the table's fallback if it is free and not handed out yet.
func (k *Keymap) swap(tableKey string, keys []string, used map[string]bool) (out []string, fallback string, unavailable bool) {
	out = []string{}
	hit := false
	for _, s := range keys {
		if isReserved(s) {
			hit = true
			continue
		}
		out = append(out, s)
	}
	if !hit {
		return out, "", false
	}
	fb, ok := webFallbacks[tableKey]
	if !ok || used[fb] || !k.chordFree(fb) {
		return out, "", true
	}
	used[fb] = true
	return append(out, fb), fb, false
}

// ForWeb turns a resolved keymap into the browser's view of it.
func ForWeb(r Resolved) WebKeymap {
	w := WebKeymap{
		Preset:     r.Preset,
		Prefix:     r.Prefix,
		TimeoutMs:  r.Timeout.Milliseconds(),
		GroupOrder: append([]string(nil), GroupOrder...),
		Actions:    []WebAction{},
		Builtins:   []WebBuiltin{},
		Conflicts:  []string{},
	}
	used := map[string]bool{}
	for _, a := range registry {
		if a.Hidden {
			continue
		}
		tier := "late"
		if a.Tier == TierEarly {
			tier = "early"
		}
		keys, fb, na := r.Keymap.swap(string(a.ID), r.Keymap.winning(a), used)
		w.Actions = append(w.Actions, WebAction{ID: string(a.ID), Label: a.Label, Group: a.Group, Tier: tier, Keys: keys, Fallback: fb, FallbackUnavailable: na})
	}
	for _, b := range webBuiltins {
		keys := []string{}
		// handleKey checks these after both tiers, so an action bound to the
		// same chord wins it and the builtin loses its key.
		if r.Keymap.chordFreeOfActions(b.chord) {
			keys = append(keys, b.chord)
		}
		keys, fb, na := r.Keymap.swap("builtin."+b.id, keys, used)
		w.Builtins = append(w.Builtins, WebBuiltin{ID: b.id, Label: b.label, Keys: keys, Fallback: fb, FallbackUnavailable: na})
	}
	for _, c := range r.Conflicts {
		w.Conflicts = append(w.Conflicts, c.String())
	}
	return w
}
