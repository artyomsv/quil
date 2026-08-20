//go:build linux

package proctree

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Enumeration only — no classification, and deliberately no /proc/<pid>/cmdline
// read. The data model carries an image name, not a command line, so reading
// cmdline would be one extra file open per process per tick for a value nothing
// renders.

// readTable returns the current process table.
func readTable() ([]ProcessEntry, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	boot := bootTime()

	out := make([]ProcessEntry, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a PID directory
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // exited between ReadDir and here
		}
		p, ok := parseStat(pid, string(raw), boot)
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// parseStat pulls PID, PPID, comm and start time out of one /proc/<pid>/stat.
//
// Split out from readTable so it is testable on its own: the field offsets and
// the tick arithmetic are the parts that can silently be wrong, and neither
// needs a real /proc to exercise.
func parseStat(pid int, stat string, boot time.Time) (ProcessEntry, bool) {
	p := ProcessEntry{PID: pid}

	// comm is wrapped in parentheses and may itself contain spaces AND
	// parentheses, so it is parsed from the LAST ')' rather than by splitting.
	end := strings.LastIndexByte(stat, ')')
	if open := strings.IndexByte(stat, '('); open >= 0 && end > open {
		p.Name = stat[open+1 : end]
	}
	if end < 0 || end+2 > len(stat) {
		return ProcessEntry{}, false
	}

	// After the closing paren: fields[0] is state, fields[1] is ppid, and
	// starttime is fields[19] here (field 22 in the 1-based whole-line
	// numbering used by proc(5)).
	fields := strings.Fields(stat[end+2:])
	if len(fields) > 1 {
		if v, err := strconv.Atoi(fields[1]); err == nil {
			p.PPID = v
		}
	}
	if len(fields) > 19 && !boot.IsZero() {
		if ticks, err := strconv.ParseInt(fields[19], 10, 64); err == nil {
			p.Start = boot.Add(ticksToDuration(ticks))
		}
	}
	return p, true
}

// ticksToDuration converts USER_HZ ticks to a Duration without overflowing.
//
// The obvious form, `time.Duration(ticks) * time.Second / clockTicksPerSecond`,
// computes ticks x 1e9 first. At 100 ticks per second that is uptime x 1e11
// nanoseconds, which passes int64's ~9.2e18 ceiling at roughly 2.9 YEARS of
// uptime — after which start times wrap to nonsense and every process looks
// older or newer than it is. On a machine that has been up that long, Build's
// splice rejection and the kill path's identity check both start lying.
//
// Dividing first keeps the product small; the remainder term preserves
// sub-second precision, which matters because the kill path compares start
// times with a one-second tolerance.
func ticksToDuration(ticks int64) time.Duration {
	whole := ticks / clockTicksPerSecond
	rest := ticks % clockTicksPerSecond
	return time.Duration(whole)*time.Second + time.Duration(rest)*(time.Second/clockTicksPerSecond)
}

// clockTicksPerSecond is USER_HZ, 100 on every Linux this runs on. Reading it
// properly needs sysconf(_SC_CLK_TCK) via CGo, which this package deliberately
// avoids.
const clockTicksPerSecond = 100

func bootTime() time.Time {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(secs, 0)
	}
	return time.Time{}
}
