package tui

import (
	"fmt"
	"strings"
)

// modelStatusSegment formats the status-bar segment for a pane's last
// completed AI turn, e.g. "opus-4.8 · 612k ctx". Empty when the pane has no
// recorded model (non-AI panes, or an AI pane before its first turn).
//
// Deliberately raw tokens, no percentage: neither Claude nor OpenCode
// transcripts record the model's max context window, and a Claude session can
// run at 200k or 1M with no way to tell from the data — a denominator would
// be a guess. Raw tokens are always accurate and provider-agnostic.
func modelStatusSegment(model string, tokens int64) string {
	if model == "" {
		return ""
	}
	name := strings.TrimPrefix(model, "claude-")
	if tokens <= 0 {
		return name
	}
	return fmt.Sprintf("%s · %s ctx", name, humanTokens(tokens))
}

// humanTokens renders a token count compactly: 950, 12k, 612k, 1.2M.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		v := float64(n) / 1_000_000
		if v >= 10 {
			return fmt.Sprintf("%.0fM", v)
		}
		return trimTrailingZero(fmt.Sprintf("%.1fM", v))
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// trimTrailingZero turns "1.0M" into "1M" while leaving "1.2M" alone.
func trimTrailingZero(s string) string {
	return strings.Replace(s, ".0", "", 1)
}
