package binswap

import (
	"errors"
	"syscall"
)

// Win32 values the Windows processAlive maps. Plain numbers rather than
// x/sys/windows constants so this file and its test build on the Linux CI
// (cmd/quil/procprobe.go's reasoning).
const (
	win32ErrorInvalidParameter = syscall.Errno(87)
	win32StillActive           = 259
)

// openFailureMeansAlive answers processAlive when OpenProcess refused a
// handle. ERROR_INVALID_PARAMETER is the only refusal that proves there is
// no such process; every other one — ERROR_ACCESS_DENIED above all, which a
// standard user gets for another account's process — means a process may
// well be there, so it reads alive. That is the safe direction for a lock:
// a holder we cannot see is a holder.
func openFailureMeansAlive(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == win32ErrorInvalidParameter {
		return false
	}
	return true
}

// exitCodeMeansAlive answers processAlive once a handle opened. A handle to
// an EXITED process still opens while anything holds one, so liveness is
// the exit status, not the open succeeding.
func exitCodeMeansAlive(code uint32) bool {
	return code == win32StillActive
}
