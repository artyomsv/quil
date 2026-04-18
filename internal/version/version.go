// Package version is the single source of truth for the running binary's
// version string. Both quil (TUI) and quild (daemon) call SetCurrent from
// their main.version ldflag sink during startup; runtime code reads via
// Current(). Parsed() and Compare() provide proper semver ordering so
// comparisons work across multi-digit components ("1.10.0" > "1.9.0").
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// fallback is returned by Current() when SetCurrent has not been called.
// "dev" matches the existing main.go sentinel for unstamped local builds.
const fallback = "dev"

var current = fallback

// SetCurrent stores the binary's version. Call exactly once from main()
// early in startup, before any code path reads Current(). Whitespace is
// trimmed. An empty string is coerced to the fallback so Current() never
// returns "" (which would complicate version-response IPC payloads).
func SetCurrent(v string) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		current = fallback
		return
	}
	current = trimmed
}

// Current returns the version set by SetCurrent, or the "dev" fallback
// for local builds where main.version was never overridden via ldflags.
func Current() string { return current }

// IsRelease reports whether Current() parses as a proper semver (i.e.,
// this is a release build, not "dev" or an unstamped binary). Callers
// use this to decide whether version-negotiation logic should engage
// or be skipped.
func IsRelease() bool {
	_, _, _, err := Parsed(current)
	return err == nil
}

// Parsed extracts the numeric major.minor.patch components from a
// semver-like string. Accepts "1.2.3", "1.2.3-rc1", "v1.2.3". Leading
// "v" and any pre-release or build suffix after the first "-" or "+"
// are stripped before parsing.
func Parsed(v string) (major, minor, patch int, err error) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	for _, sep := range []string{"-", "+"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("not semver: %q", v)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		if p == "" {
			return 0, 0, 0, fmt.Errorf("empty component in %q", v)
		}
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return 0, 0, 0, fmt.Errorf("not numeric %q in %q", p, v)
		}
		if n < 0 {
			return 0, 0, 0, fmt.Errorf("negative component %d in %q", n, v)
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], nil
}

// Compare returns -1 if a<b, 0 if a==b, +1 if a>b by semver ordering.
// Pre-release and build suffixes are ignored: "1.2.3-rc1" compares equal
// to "1.2.3". Returns a non-nil error when either side is unparseable so
// callers can decide how to interpret (typically: skip the check or treat
// as mismatch depending on context).
func Compare(a, b string) (int, error) {
	am, an, ap, err := Parsed(a)
	if err != nil {
		return 0, err
	}
	bm, bn, bp, err := Parsed(b)
	if err != nil {
		return 0, err
	}
	switch {
	case am != bm:
		return sign(am - bm), nil
	case an != bn:
		return sign(an - bn), nil
	case ap != bp:
		return sign(ap - bp), nil
	}
	return 0, nil
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
