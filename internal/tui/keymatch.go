package tui

import "strings"

// DEPRECATED for dispatch: handleKey resolves actions through Model.keymap,
// and every modal surface outside handleKey (dialogs, context menu, lazygit
// overlay, palette, reconnect screen) resolves through Model.isAction now
// too. Three call sites remain, and only the first two are Stage 2's to
// remove: notesKeyExempt and the notes-mode key split inside handleKey (both
// still fed by the raw config keybindings the notes block reads directly —
// Stage 2 replaces this with the prefix state machine). The third, Update's
// reconnect resume-key check, matches against the hardcoded
// reconnectResumeKey constant rather than a config value and stays even
// after Stage 2. Do not add new call sites.
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
// through Keymap.Display. Kept as the test-side oracle those two migrations
// are checked against (TestShortcutsList_CoversEveryProjectBinding,
// TestPalette_ProjectRowsCarryTheirShortcuts): kbDisplay parses the same
// raw config string Keymap.Display resolves through the registry, so the two
// must keep agreeing.
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
