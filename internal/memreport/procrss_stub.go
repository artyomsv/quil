//go:build !linux && !darwin && !windows

package memreport

// procRSSBatch is a no-op on platforms without a dedicated implementation.
// The daemon reports 0 PTY RSS for every pane on such platforms.
func procRSSBatch(pids []int) map[int]uint64 {
	return map[int]uint64{}
}
