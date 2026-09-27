//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes a non-blocking exclusive flock. EWOULDBLOCK means another open
// file description holds it.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
