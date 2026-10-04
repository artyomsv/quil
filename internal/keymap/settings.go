package keymap

import (
	"fmt"
	"time"
)

// Settings is a bindings.toml in this package's own types. internal/config
// owns the file; this package owns what it means.
type Settings struct {
	Preset    string
	Prefix    string
	Timeout   time.Duration
	Overrides map[ActionID]string
}

// Resolved is Settings turned into a dispatch keymap, plus what was actually
// applied: an unknown preset falls back to the default and says so.
type Resolved struct {
	Keymap    *Keymap
	Conflicts []Conflict
	Preset    string
	Prefix    string
	Timeout   time.Duration
	Warnings  []string
}

// FromSettings layers the registry defaults, the selected preset and the
// user's overrides, expanding ${prefix} in each layer before any collision
// analysis. It is the ONE resolution both the TUI and quil web use, so the
// browser cannot drift from the terminal. Pure: no I/O, no logging.
func FromSettings(s Settings) Resolved {
	out := Resolved{Preset: DefaultPresetName, Timeout: s.Timeout}
	var presetLayer map[ActionID]string
	prefix := s.Prefix
	if s.Preset != "" && s.Preset != DefaultPresetName {
		p, err := LoadPreset(s.Preset)
		if err != nil {
			// An unknown preset keeps the defaults rather than leaving the
			// user with no keymap at all.
			out.Warnings = append(out.Warnings, fmt.Sprintf("%v; keeping the default preset", err))
		} else {
			out.Preset = p.Name
			presetLayer = p.Bindings
			// A preset's timeout is the DEFAULT, not an override: it applies
			// only when the user left sequence_timeout unset.
			if out.Timeout == 0 && p.Timeout != "" && p.Timeout != "0" {
				if d, err := time.ParseDuration(p.Timeout); err == nil {
					out.Timeout = d
				} else {
					out.Warnings = append(out.Warnings, fmt.Sprintf("preset %q has an unreadable sequence_timeout %q", p.Name, p.Timeout))
				}
			}
			// Same rule for the prefix: `preset = "tmux"` alone must get
			// ctrl+b, or every ${prefix} binding expands against "" and drops.
			if prefix == "" {
				prefix = p.Prefix
			}
		}
	}
	if w := PrefixWarning(prefix); w != "" {
		out.Warnings = append(out.Warnings, w)
	}
	expand := func(layer map[ActionID]string) map[ActionID]string {
		e, cs := ExpandPrefix(layer, prefix)
		out.Conflicts = append(out.Conflicts, cs...)
		return e
	}
	km, built := BuildLayered(expand(DefaultLayer()), expand(presetLayer), expand(s.Overrides))
	out.Keymap = km
	out.Conflicts = append(out.Conflicts, built...)
	out.Prefix = prefix
	return out
}
