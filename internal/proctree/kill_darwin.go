//go:build darwin

package proctree

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// errIdentityChanged means the PID is now held by a different process than the
// one the caller intended to signal.
var errIdentityChanged = errors.New("proctree: process identity changed")

// Darwin has no primitive that pins a process the way pidfd_open or a Windows
// handle does. Verification and signalling are therefore separate steps, with a
// residual window between them of TENS OF MILLISECONDS -- not microseconds, as
// an earlier version of this comment claimed: identityMatches forks ps, so the
// gap spans a whole process spawn rather than a few instructions.
//
// Documented rather than hidden: it is the one platform where the kill path's
// identity guarantee is best-effort, and the sweep's own re-check before
// escalation is what keeps that window from widening to the whole grace period.

// DefaultKillOps returns this platform's signalling primitives.
func DefaultKillOps() KillOps {
	return KillOps{
		Term:  func(pid int, start time.Time) error { return signalVerified(pid, start, unix.SIGTERM) },
		Kill:  func(pid int, start time.Time) error { return signalVerified(pid, start, unix.SIGKILL) },
		Alive: aliveWithStart,
		Sleep: time.Sleep,
	}
}

func signalVerified(pid int, start time.Time, sig unix.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("proctree: refusing to signal pid %d", pid)
	}
	if !identityMatches(pid, start) {
		return errIdentityChanged
	}
	if err := unix.Kill(pid, sig); err != nil {
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	return nil
}

// identityMatches reports whether pid currently names the process that started
// at start. An unknown start time on either side is NOT a match.
func identityMatches(pid int, start time.Time) bool {
	cur, ok := processStart(pid)
	if !ok {
		return false
	}
	return sameProcess(cur, start)
}

func aliveWithStart(pid int, start time.Time) bool { return identityMatches(pid, start) }

// processStart reads one process's start time via ps.
func processStart(pid int) (time.Time, bool) {
	out, err := runPS("-o", "lstart=", "-p", strconv.Itoa(pid))
	if err != nil {
		return time.Time{}, false
	}
	return parsePSStart(out)
}
