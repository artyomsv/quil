//go:build linux

package memreport

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procRSSBatch reads /proc/<pid>/status for each pid and returns a map from
// pid to resident set size in bytes. PIDs whose status cannot be read (e.g.,
// the process exited between the pane's ExitCode check and the RSS read)
// are omitted from the map — callers treat missing entries as zero.
func procRSSBatch(pids []int) map[int]uint64 {
	out := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		if rss, ok := readVmRSS(pid); ok {
			out[pid] = rss
		}
	}
	return out
}

// readVmRSS parses the VmRSS line of /proc/<pid>/status. VmRSS is reported
// in kilobytes per `man 5 proc`.
func readVmRSS(pid int) (uint64, bool) {
	path := fmt.Sprintf("/proc/%d/status", pid)
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		// Format: "VmRSS:\t    1234 kB"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
