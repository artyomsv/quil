//go:build !windows

package binswap

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid is a live process. EPERM means it exists
// and belongs to someone else, which is alive for a lock's purposes.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
