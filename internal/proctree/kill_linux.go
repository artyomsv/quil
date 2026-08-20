//go:build linux

package proctree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// errIdentityChanged means the PID is now held by a different process than the
// one the caller intended to signal.
var errIdentityChanged = errors.New("proctree: process identity changed")

// DefaultKillOps returns this platform's signalling primitives.
func DefaultKillOps() KillOps {
	return KillOps{
		Term:  func(pid int, start time.Time) error { return signalPinned(pid, start, unix.SIGTERM) },
		Kill:  func(pid int, start time.Time) error { return signalPinned(pid, start, unix.SIGKILL) },
		Alive: aliveWithStart,
		Sleep: time.Sleep,
	}
}

// signalPinned sends sig to the process identified by BOTH pid and start.
//
// pidfd_open pins the identity: once the descriptor exists it refers to that
// process and nothing else, so a PID recycled afterwards cannot be signalled
// through it. The start-time check runs AFTER the open to catch a recycle that
// happened before it — the two together leave no window.
//
// Falls back to a verify-then-kill on kernels without pidfd (pre-5.3), where a
// window of microseconds remains between the check and the signal. That path is
// exercised in tests rather than assumed unreachable.
func signalPinned(pid int, start time.Time, sig unix.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("proctree: refusing to signal pid %d", pid)
	}

	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			return signalUnpinned(pid, start, sig)
		}
		return fmt.Errorf("pidfd_open %d: %w", pid, err)
	}
	defer func() {
		// Nothing actionable if this fails; the fd dies with the process.
		_ = unix.Close(fd)
	}()

	if !identityMatches(pid, start) {
		return errIdentityChanged
	}
	if err := unix.PidfdSendSignal(fd, sig, nil, 0); err != nil {
		return fmt.Errorf("pidfd_send_signal %d: %w", pid, err)
	}
	return nil
}

// signalUnpinned is the pre-5.3 fallback: verify, then signal the number.
func signalUnpinned(pid int, start time.Time, sig unix.Signal) error {
	if !identityMatches(pid, start) {
		return errIdentityChanged
	}
	if err := unix.Kill(pid, sig); err != nil {
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	return nil
}

// identityMatches reports whether pid currently names the process that started
// at start.
//
// An unknown start time on either side is NOT a match. Refusing costs a kill
// the user can retry; accepting signals a process nobody asked about.
func identityMatches(pid int, start time.Time) bool {
	cur, ok := processStart(pid)
	if !ok {
		return false
	}
	return sameProcess(cur, start)
}

// aliveWithStart reports whether the process with this PID AND this start time
// is still running.
//
// A ZOMBIE is not running. /proc/<pid>/stat survives a process's death until
// its parent reaps it, complete with the original start time, so an identity
// check alone answers "alive" for something that has already exited — and a
// subtree sweep creates exactly that state, because it kills parents and their
// children together and a parent that exits first cannot reap anything. Left
// unhandled, Escalate SIGKILLs corpses and reports them as forced, and a
// long-lived zombie under a pane renders as a live process the dialog offers to
// stop. Windows has the equivalent check via GetExitCodeProcess.
func aliveWithStart(pid int, start time.Time) bool {
	if !identityMatches(pid, start) {
		return false
	}
	return !isZombie(pid)
}

// isZombie reports whether a process is in state Z.
//
// The state is the first field after comm's closing parenthesis, which is why
// this parses from the LAST ')' like every other reader of this file: comm can
// contain both spaces and parentheses.
func isZombie(pid int) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		// Gone entirely; the identity check above already answered that case.
		return false
	}
	stat := string(raw)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 || end+2 > len(stat) {
		return false
	}
	fields := strings.Fields(stat[end+2:])
	return len(fields) > 0 && fields[0] == "Z"
}

// processStart reads one process's start time from /proc.
func processStart(pid int) (time.Time, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, false
	}
	e, ok := parseStat(pid, string(raw), bootTime())
	if !ok || e.Start.IsZero() {
		return time.Time{}, false
	}
	return e.Start, true
}
