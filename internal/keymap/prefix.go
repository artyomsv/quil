package keymap

import (
	"fmt"
	"strings"
)

// prefixVar is the token a preset writes where the user's prefix chord goes.
const prefixVar = "${prefix}"

// ExpandPrefix substitutes ${prefix} in every spec, BEFORE parsing. Doing it
// here rather than at match time means conflict detection and the prefix set
// see real chord sequences instead of templates — a literal "${prefix}" is not
// a chord, so a collision check run against unexpanded specs finds nothing.
//
// A binding that references an unusable prefix is DROPPED, with a conflict —
// never half-expanded. The alternative is worse than useless: "${prefix} c"
// with an empty prefix becomes the bare chord "c", which then eats every c
// before the pane sees it.
//
// A nil or empty layer returns nil, so "no preset selected" and "a preset that
// binds nothing" stay distinguishable to Resolve.
func ExpandPrefix(specs map[ActionID]string, prefix string) (map[ActionID]string, []Conflict) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make(map[ActionID]string, len(specs))
	var conflicts []Conflict

	canonical, err := validatePrefix(prefix)
	for id, spec := range specs {
		if !strings.Contains(spec, prefixVar) {
			out[id] = spec
			continue
		}
		if err != nil {
			conflicts = append(conflicts, Conflict{
				Kind: ConflictPrefixInvalid, Key: spec, Loser: id, Detail: err.Error(),
			})
			continue
		}
		out[id] = strings.ReplaceAll(spec, prefixVar, canonical)
	}
	return out, conflicts
}

// validatePrefix returns the canonical form of a prefix chord.
//
// Each rejection maps to a specific silent failure rather than to tidiness:
//
//   - unset, while ${prefix} is referenced: expanding to "" turns "${prefix} c"
//     into the bare chord c.
//   - a comma: expands into the ALTERNATIVES grammar, so "${prefix} c" with
//     prefix "ctrl+a, ctrl+b" becomes chord(ctrl+a) OR seq(ctrl+b, c) — a
//     plausible tmux prefix2 instinct that yields a keymap nobody wrote.
//   - a space: turns every two-step binding into a three-step one.
func validatePrefix(prefix string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", fmt.Errorf("prefix is unset but ${prefix} is used")
	}
	if strings.Contains(prefix, ",") {
		return "", fmt.Errorf("prefix %q contains a comma; it must be exactly one chord", prefix)
	}
	if strings.Contains(strings.TrimSpace(prefix), " ") {
		return "", fmt.Errorf("prefix %q contains a space; it must be exactly one chord", prefix)
	}
	c, err := ParseChord(prefix)
	if err != nil {
		return "", fmt.Errorf("prefix %q is not a valid chord: %w", prefix, err)
	}
	return c.String(), nil
}

// PrefixWarning reports a prefix that is legal but globally costly: an
// unmodified printable key is swallowed everywhere it is pressed. A warning
// rather than an error — it is the user's letter to spend. Mirrors the existing
// sanitizeRawKeys warning in internal/plugin.
//
// Returns "" when there is nothing to say.
func PrefixWarning(prefix string) string {
	c, err := ParseChord(prefix)
	if err != nil || c.Mods != 0 || len([]rune(c.Key)) != 1 {
		return ""
	}
	return fmt.Sprintf("prefix %q has no modifier: every press of it is swallowed before the pane sees it", prefix)
}
