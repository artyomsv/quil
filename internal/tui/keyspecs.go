package tui

import (
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/logger"
)

// keySpecsFromConfig forwards to config.KeySpecsFromConfig, which owns the
// legacy field-name-to-action-ID map now: MigrateBindings needs the same map to
// diff a user's config against the shipped defaults, and two copies would drift.
//
// Kept as a local alias because this package's tests and buildKeymap read it
// often enough that the qualifier is noise at every call site.
func keySpecsFromConfig(kb config.KeybindingsConfig) map[keymap.ActionID]string {
	return config.KeySpecsFromConfig(kb)
}

// buildKeymap resolves a config into the dispatch keymap.
//
// Two layers, not one. The registry's own defaults go underneath because the
// legacy [keybindings] table has no field for the twelve actions promoted out
// of handleKey's reserved-key switch (tab.switch_1..9, tab.next, tab.prev,
// system.shortcuts), nor for the four reorder actions that arrived after
// bindings.toml replaced it — building from the config alone would leave
// Alt+1..9 bound to nothing at all. The config layer on top still wins for
// every one of the 42 fields it does carry.
//
// Build handles a malformed spec per-action, so there is no whole-config
// fallback to do here.
func buildKeymap(kb config.KeybindingsConfig) (*keymap.Keymap, []keymap.Conflict) {
	km, conflicts := keymap.BuildLayered(keymap.DefaultLayer(), keySpecsFromConfig(kb))
	for _, c := range conflicts {
		logger.Warn("keybindings: %s", c)
	}
	return km, conflicts
}

// SetBindings applies a loaded bindings.toml, replacing whatever initKeymap
// built from the legacy config table.
//
// PURE — no disk access. ~46 tests build a Model directly without setting
// QUIL_HOME, and a read here would point every one of them at the real
// ~/.quil. cmd/quil/main.go does the load, following the SetRecentCWDs
// precedent.
func (m *Model) SetBindings(b config.Bindings) {
	base := keymap.DefaultLayer()

	var presetLayer map[keymap.ActionID]string
	prefix := b.Prefix
	if b.Preset != "" && b.Preset != keymap.DefaultPresetName {
		p, err := keymap.LoadPreset(b.Preset)
		if err != nil {
			// An unknown preset name keeps the defaults rather than leaving the
			// user with no keymap at all.
			logger.Warn("keybindings: %v; keeping the default preset", err)
		} else {
			presetLayer = p.Bindings
			// Same default-not-override rule for the timeout. bindings.toml
			// carries no way to say "0 means I chose off" apart from omitting
			// it, so a preset's value applies only when the user left it unset.
			if b.SequenceTimeout == 0 && p.Timeout != "" && p.Timeout != "0" {
				if d, err := time.ParseDuration(p.Timeout); err == nil {
					b.SequenceTimeout = d
				} else {
					logger.Warn("keybindings: preset %q has an unreadable sequence_timeout %q", p.Name, p.Timeout)
				}
			}
			// The preset's own prefix is the DEFAULT, not an override: a user
			// who writes only `preset = "tmux"` must get ctrl+b, or every one
			// of that preset's ${prefix} bindings expands against an empty
			// prefix and is dropped. Setting prefix in bindings.toml still wins,
			// which is how `set -g prefix C-a` is expressed.
			if prefix == "" {
				prefix = p.Prefix
			}
		}
	}
	if warning := keymap.PrefixWarning(prefix); warning != "" {
		logger.Warn("keybindings: %s", warning)
	}

	// Expand ${prefix} in each layer SEPARATELY and before any collision
	// analysis. BuildLayered decides who wins by comparing head chords, and a
	// literal "${prefix}" is not a chord — expanding after the merge would have
	// it compare templates and find no collisions at all.
	var conflicts []keymap.Conflict
	expand := func(layer map[keymap.ActionID]string) map[keymap.ActionID]string {
		out, cs := keymap.ExpandPrefix(layer, prefix)
		conflicts = append(conflicts, cs...)
		return out
	}

	km, buildConflicts := keymap.BuildLayered(expand(base), expand(presetLayer), expand(b.Overrides))
	conflicts = append(conflicts, buildConflicts...)

	m.keymap = km
	m.keyConflicts = conflicts
	m.seqTimeout = b.SequenceTimeout
	for _, c := range conflicts {
		logger.Warn("keybindings: %s", c)
	}
}

// isAction reports whether key is bound to the given action, searching both
// tiers. Used by the modal surfaces that sit outside handleKey's tier split
// and still need to recognise one specific action: the paste branches in
// dialog.go, the context menu, and the lazygit overlay. The reconnect screen
// is NOT one of them — it needs the bindings themselves, and reads them with
// Keymap.Keys.
//
// Searching BOTH tiers is deliberate and is why this is not a dispatch
// primitive: these surfaces run before handleKey's tier split, so "is this key
// bound to X" is the only question they can ask. The cost is that a modal can
// recognise an action whose handleKey arm the corresponding tier would never
// reach — harmless for the six call sites, since each asks about one action it
// then handles itself, but a caller using this to decide what handleKey WILL do
// would get the wrong answer for a chord claimed in the other tier.
//
// It answers FALSE for an action bound only to a multi-chord sequence, and that
// is correct rather than a gap: every caller is a mode that owns the keyboard,
// and the sequence machine is deliberately inert in all of them. Do not "fix"
// it by matching a sequence's head chord here — that would make the context
// menu quit on a bare prefix press.
//
// The cost, under a preset that binds these actions to sequences (tmux does),
// differs per caller and is worth stating exactly rather than in aggregate:
//
//   - Context menu: the "never swallow quit" passthrough stops applying. Esc
//     still closes the menu (ctxmenu.go), so nothing is stuck.
//   - Overlay: the same, AND Esc does NOT help — handleOverlayKey deliberately
//     forwards Esc to the running tool. A sequence-bound pane.toggle_lazygit
//     therefore cannot close the overlay it opened; the tool's own quit key
//     ("q" in lazygit) is the way out. Do not bind an overlay toggle to a
//     sequence without knowing that.
//   - Notes mode: handled rather than lost. handleKey's completed-sequence
//     block mirrors the notes block arm for arm, because that is the only
//     place a sequence can arrive.
//
// This is the known set, not a proof of completeness: any caller added here
// inherits the same blind spot.
func (m *Model) isAction(key string, id keymap.ActionID) bool {
	for _, tier := range []keymap.Tier{keymap.TierEarly, keymap.TierLate} {
		if got, ok := m.keymap.MatchTier(tier, key); ok && got == id {
			return true
		}
	}
	return false
}
