package config

import (
	"os"

	"github.com/artyomsv/quil/internal/keymap"
)

// LegacyKeymap resolves the legacy [keybindings] table in config.toml: the
// registry defaults underneath, the table on top. It is the keymap a client
// keeps when no usable bindings.toml exists — the TUI at start and quil web
// per request both build it here, so the two cannot drift.
func LegacyKeymap(kb KeybindingsConfig) (*keymap.Keymap, []keymap.Conflict) {
	return keymap.BuildLayered(keymap.DefaultLayer(), KeySpecsFromConfig(kb))
}

// ActiveBindings says which keymap a client applies: bindings.toml when it
// exists and reads (legacy false), else the legacy table (legacy true). err
// is the read error of an unreadable file, for the caller to report.
//
// A MISSING file means the legacy table, not the defaults LoadBindings
// answers with: the TUI migrates [keybindings] into bindings.toml at start,
// so a missing file is a migration that could not write (read-only
// QUIL_HOME, full disk) or one that has not run yet (a headless host where
// only quil web runs). Either way the user's [keybindings] are what apply.
func ActiveBindings() (b Bindings, legacy bool, err error) {
	b, err = LoadBindings()
	if err != nil {
		return b, true, err
	}
	if _, statErr := os.Stat(BindingsPath()); statErr != nil {
		return b, true, nil
	}
	return b, false, nil
}
