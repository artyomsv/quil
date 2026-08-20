//go:build windows

package proctree

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// errIdentityChanged means the PID is now held by a different process than the
// one the caller intended to signal.
var errIdentityChanged = errors.New("proctree: process identity changed")

// Windows has no graceful tier for an arbitrary process: TerminateProcess is
// the only portable option, and it gives the target no chance to clean up. The
// confirm dialog says so on this platform rather than promising a graceful stop
// that cannot happen.
//
// The identity window IS closed here, and more completely than anywhere else:
// the handle is opened first, the creation time is verified ON THAT HANDLE, and
// the same handle is terminated. A PID recycled after the open cannot be
// reached through it. This matters more on Windows than elsewhere because it
// recycles PIDs aggressively.

const processTerminateAcc = 0x0001

// DefaultKillOps returns this platform's signalling primitives.
func DefaultKillOps() KillOps {
	return KillOps{
		Term:  terminatePinned,
		Kill:  terminatePinned,
		Alive: aliveWithStart,
		Sleep: time.Sleep,
	}
}

// terminatePinned verifies and terminates through one handle.
//
// Term and Kill are the same call: there is no graceful signal to send first,
// so the sweep's grace period simply finds nothing left to escalate.
func terminatePinned(pid int, start time.Time) error {
	if pid <= 0 {
		return fmt.Errorf("proctree: refusing to terminate pid %d", pid)
	}
	h, err := windows.OpenProcess(processTerminateAcc|processQueryLimitedInfoAcc, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess %d: %w", pid, err)
	}
	defer func() {
		// Nothing actionable on failure; the handle dies with the process.
		_ = windows.CloseHandle(h)
	}()

	cur, ok := handleStart(h)
	if !ok || !sameProcess(cur, start) {
		return errIdentityChanged
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("TerminateProcess %d: %w", pid, err)
	}
	return nil
}

func aliveWithStart(pid int, start time.Time) bool {
	h, err := windows.OpenProcess(processQueryLimitedInfoAcc, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	// A handle to an EXITED process still opens and still reports its creation
	// time, so liveness must be asked separately rather than inferred from the
	// open succeeding.
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	if code != stillActive {
		return false
	}
	cur, ok := handleStart(h)
	return ok && sameProcess(cur, start)
}
