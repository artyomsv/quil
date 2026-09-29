package tui

import "github.com/artyomsv/quil/internal/textsafe"

// sanitizeRemoteText is textsafe.Strip under the name every render site in
// this package already uses. The rule and its rationale live in
// internal/textsafe, because the daemon validates group names with the same
// rule and cannot import this package.
func sanitizeRemoteText(s string) string {
	return textsafe.Strip(s)
}
