//go:build !windows

package binswap

import (
	"errors"
	"os"
	"syscall"
)

// lockHandle takes a non-blocking exclusive flock on f; EWOULDBLOCK (EAGAIN
// on some systems) is errLockBusy. The lock is on the open file description
// and is released when f closes or the process exits.
func lockHandle(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errLockBusy
	}
	return err
}

func unlockHandle(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
