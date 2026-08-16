package tui

import "strings"

// DEPRECATED for dispatch: handleKey resolves actions through Model.keymap.
// The modal surfaces that compare a key against a specific action — the paste
// branches in dialog.go, the context menu, the lazygit overlay — go through
// Model.isAction (keyspecs.go); those are its only six call sites. The other
// two registry readers do not compare at all: the palette renders each row's
// shortcut with Keymap.Display, and the reconnect screen asks Keymap.Keys for
// the quit bindings and hands them to isFreezeEscape.
//
// ONE kbMatches call site remains: Update's reconnect resume-key check, which
// matches against the hardcoded reconnectResumeKey constant rather than a
// config value and so has nothing to resolve through the registry. The notes
// readers that used to be here — notesKeyExempt and the notes-mode key split
// inside handleKey — now go through Model.isAction, which is what lets the
// binding source move off cfg.Keybindings without those two silently starting
// to compare against empty strings.
//
// handleKey itself no longer reads cfg.Keybindings at all.
//
// Do not add new call sites.
//
// kbMatches reports whether key matches configured, where configured is
// either a single binding ("alt+f2") or a comma-separated list of
// alternatives ("alt+f2,alt+shift+r"). Whitespace around each entry is
// trimmed. Returns false on empty key or empty configured.
func kbMatches(key, configured string) bool {
	if key == "" || configured == "" {
		return false
	}
	if !strings.Contains(configured, ",") {
		return key == configured
	}
	for _, b := range strings.Split(configured, ",") {
		if strings.TrimSpace(b) == key {
			return true
		}
	}
	return false
}

// kbBindings returns the individual bindings parsed out of a configured
// spec ("alt+f2 / alt+shift+r"). Whitespace-only entries are dropped.
//
// No production caller — the shortcuts dialog and the palette now render
// through Keymap.Display. Both survive as test-side oracles over the raw
// config string, which the registry answers about independently:
// TestPalette_ProjectRowsCarryTheirShortcuts compares every project row's
// detail against kbDisplay of the same config field, and
// TestModel_NotesKeyExempt_AllowsGlobalShortcuts expands each multi-binding
// field with kbBindings so it can press the alternatives one at a time.
// (TestShortcutsList_CoversEveryProjectBinding was the third; it was replaced
// by TestShortcutsList_CoversEveryBoundVisibleAction, which asks the registry
// rather than the config and so needs no oracle.)
func kbBindings(configured string) []string {
	if configured == "" {
		return nil
	}
	if !strings.Contains(configured, ",") {
		return []string{configured}
	}
	parts := strings.Split(configured, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// kbDisplay formats a configured binding for help text — joins multiple
// bindings with " / " for readability.
func kbDisplay(configured string) string {
	bindings := kbBindings(configured)
	return strings.Join(bindings, " / ")
}
