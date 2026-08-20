//go:build darwin

package proctree

import (
	"context"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Darwin has no /proc, so enumeration shells out to ps — the same approach
// internal/memreport/procrss_darwin.go already takes, and with the same bounds:
// a 2 s timeout and a capped read. The removed attempt used a bare
// exec.Command(...).Output() with neither, putting an unbounded blocking child
// on the collector goroutine.
//
// Exec ONLY. Every parser lives in table_ps.go with no build tag, because CI is
// Linux and would otherwise never compile a line of this platform's logic.

// psTimeout bounds every ps invocation. A hung child must not wedge the
// collector.
const psTimeout = 2 * time.Second

// psMaxOutput caps stdout so unexpected output cannot grow without bound.
const psMaxOutput = 1 << 20

func runPS(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ps", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, psMaxOutput))
	if err := cmd.Wait(); err != nil {
		return "", err
	}
	if readErr != nil {
		return "", readErr
	}
	return string(out), nil
}

// readTable returns the current process table.
//
// lstart is NOT optional. It is the only source of a start time on this
// platform, and Start gates both Build's splice rejection and every kill — so
// omitting it would not degrade the feature gracefully, it would make every
// Darwin kill refuse forever.
func readTable() ([]ProcessEntry, error) {
	out, err := runPS("-axo", "pid=,ppid=,lstart=,comm=")
	if err != nil {
		return nil, err
	}
	return parsePSTable(out), nil
}

// readCPU returns the kernel's own CPU percentage per PID.
//
// This is NOT usage over our sample window: pcpu is a decaying average computed
// by the kernel. proc_pidinfo would give a cumulative counter to delta like the
// other platforms, but it is a libproc call — this repo has no CGo, x/sys/unix
// exposes no wrapper, and Darwin reaches libc through cgo_import_dynamic
// trampolines rather than raw syscalls, so calling it means hand-written
// assembly for a platform CI cannot run. The dialog footnotes the difference
// instead of hiding it.
func readCPU(pids []int) CPUReading {
	if len(pids) == 0 {
		return CPUReading{Supported: true, Instant: map[int]float64{}}
	}
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}
	out, err := runPS("-o", "pid=,pcpu=", "-p", strings.Join(parts, ","))
	if err != nil {
		return CPUReading{Supported: false}
	}
	return CPUReading{Supported: true, Instant: parsePSPercent(out)}
}

// cpuIsSampled reports whether this platform's CPU figure is a delta across our
// own sample window. False here, which the dialog surfaces as a footnote.
const cpuIsSampled = false

// tableHasStarts is true: lstart comes back in the same ps call as PPID, so
// there is no second pass on this platform.
const tableHasStarts = true

// enrichStarts is a no-op here: readTable already filled Start.
func enrichStarts(table []ProcessEntry, _ []int) []ProcessEntry { return table }
