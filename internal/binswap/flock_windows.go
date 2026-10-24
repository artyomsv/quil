//go:build windows

package binswap

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockHandle takes an exclusive, fail-immediately LockFileEx on the first
// byte of f; ERROR_LOCK_VIOLATION is errLockBusy. The lock belongs to the
// handle and is released when it closes or the process exits.
func lockHandle(f *os.File) error {
	var ov windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockBusy
	}
	return err
}

func unlockHandle(f *os.File) {
	var ov windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}
