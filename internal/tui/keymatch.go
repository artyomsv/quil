package tui

import "strings"

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
// spec. Used by the shortcuts help dialog to render every binding for an
// action ("alt+f2 / alt+shift+r"). Whitespace-only entries are dropped.
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
