// Package memreport collects per-pane memory snapshots for the daemon and
// exposes a human-readable formatter used by the TUI, status bar, and MCP
// tools.
package memreport

import "fmt"

// HumanBytes renders a byte count using the largest unit whose integer part
// is non-zero. One decimal place for values ≥ 1 KB, no fractional part for
// raw bytes. Output is ASCII-safe (no multibyte characters).
func HumanBytes(n uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.1f TB", float64(n)/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
