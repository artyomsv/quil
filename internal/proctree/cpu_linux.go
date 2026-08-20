//go:build linux

package proctree

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tableHasStarts reports whether readTable already supplies start times.
//
// True here: /proc/<pid>/stat carries starttime in the same read as PPID, so
// one pass is enough. Windows has no such call and needs a second pass — see
// table_windows.go.
const tableHasStarts = true

// cpuIsSampled reports whether this platform's CPU figure is a delta computed
// across our own sample window (true) or a kernel-computed average (false).
const cpuIsSampled = true

// readCPU returns cumulative CPU time per PID, for the Sampler to delta.
//
// Only the PIDs asked for are read — one file open each — so the cost is bound
// to a pane's descendants rather than to the size of the machine's process
// table.
func readCPU(pids []int) CPUReading {
	out := make(map[int]time.Duration, len(pids))
	for _, pid := range pids {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			continue // exited between enumeration and here
		}
		if d, ok := parseCPUTicks(string(raw)); ok {
			out[pid] = d
		}
	}
	return CPUReading{Cumulative: out, Supported: true}
}

// parseCPUTicks sums utime and stime from one /proc/<pid>/stat.
//
// proc(5) numbers these fields 14 and 15 on the whole line; after the closing
// parenthesis of comm they are indices 11 and 12, since the line's first three
// fields (pid, comm, state) become index 0 at "state".
func parseCPUTicks(stat string) (time.Duration, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 || end+2 > len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) <= 12 {
		return 0, false
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	// Same overflow-safe conversion as start times: the naive form multiplies
	// by 1e9 before dividing and wraps on a long-running process.
	return ticksToDuration(utime + stime), true
}

// enrichStarts is a no-op here: readTable already filled Start.
func enrichStarts(table []ProcessEntry, _ []int) []ProcessEntry { return table }
