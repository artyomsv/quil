//go:build !windows

package procscan

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Enumeration only. Linux reads /proc directly; Darwin has no /proc and shells
// out to ps, which is the same approach cmd/quil/procctl_unix.go already takes
// for its PID-reuse guard.

// Snapshot returns the current process table.
func Snapshot() ([]Process, error) {
	if runtime.GOOS == "linux" {
		return snapshotProc()
	}
	return snapshotPS()
}

// snapshotProc reads /proc/<pid>/{stat,cmdline}.
//
// Start times come from the stat file's field 22 (starttime, in clock ticks
// since boot) added to boot time. Best-effort: a process that disappears
// mid-walk is skipped rather than failing the sweep, which is ordinary on a busy
// machine.
func snapshotProc() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	boot := bootTime()

	var out []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		statBytes, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // exited between ReadDir and here
		}
		p := Process{PID: pid}

		// comm can contain spaces and parentheses, so parse from the LAST ')'.
		stat := string(statBytes)
		close := strings.LastIndexByte(stat, ')')
		if open := strings.IndexByte(stat, '('); open >= 0 && close > open {
			p.Name = stat[open+1 : close]
		}
		if close < 0 || close+2 > len(stat) {
			continue
		}
		fields := strings.Fields(stat[close+2:])
		// fields[0] is state; ppid is the next, and starttime is field 20 here
		// (22 in the 1-based whole-line numbering).
		if len(fields) > 1 {
			if v, err := strconv.Atoi(fields[1]); err == nil {
				p.PPID = v
			}
		}
		if len(fields) > 19 && !boot.IsZero() {
			if ticks, err := strconv.ParseInt(fields[19], 10, 64); err == nil {
				p.Start = boot.Add(time.Duration(ticks) * time.Second / clockTicksPerSecond)
			}
		}

		if raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); err == nil && len(raw) > 0 {
			p.Cmdline = strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
		} else {
			p.Cmdline = p.Name
		}
		out = append(out, p)
	}
	return out, nil
}

// clockTicksPerSecond is USER_HZ, 100 on every Linux this runs on. Reading it
// properly needs sysconf(_SC_CLK_TCK) via CGo, which this package deliberately
// avoids; the value only scales start times, and a wrong scale would have to be
// off by hours to change an orphan verdict.
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

// snapshotPS is the Darwin path. lstart is requested because ps reports elapsed
// time at second granularity, and two processes started in the same second
// would otherwise be indistinguishable for the parent-younger-than-child check.
func snapshotPS() ([]Process, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,lstart=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var procs []Process
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		p := Process{PID: pid, PPID: ppid}
		// lstart is five fields: "Wed Aug 20 12:00:00 2026".
		if t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[2:7], " "), time.Local); err == nil {
			p.Start = t
		}
		p.Cmdline = strings.Join(f[7:], " ")
		p.Name = filepath.Base(strings.Fields(p.Cmdline)[0])
		procs = append(procs, p)
	}
	return procs, nil
}
