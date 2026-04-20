//go:build darwin

package memreport

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// procRSSBatch invokes `ps -o pid=,rss= -p <pid,pid,...>` with a 2 s
// timeout. RSS is reported in kilobytes; we convert to bytes. PIDs missing
// from the output are omitted from the returned map.
func procRSSBatch(pids []int) map[int]uint64 {
	if len(pids) == 0 {
		return map[int]uint64{}
	}
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ps", "-o", "pid=,rss=", "-p", strings.Join(parts, ","))
	output, err := cmd.Output()
	if err != nil {
		return map[int]uint64{}
	}

	out := make(map[int]uint64, len(pids))
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		kb, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[pid] = kb * 1024
	}
	return out
}
