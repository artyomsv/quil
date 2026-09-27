package main

import (
	"errors"
	"syscall"
)

// Win32 values the Windows processProbe maps. Plain numbers rather than
// x/sys/windows constants so this file and its test build on the Linux CI.
const (
	win32ErrorAccessDenied     = syscall.Errno(5)
	win32ErrorInvalidParameter = syscall.Errno(87)
	win32StillActive           = 259
)

// openFailureMeansAlive answers processProbe when OpenProcess refused a
// handle. ERROR_INVALID_PARAMETER is the only refusal that proves there is no
// such process. Every other one — ERROR_ACCESS_DENIED above all, which a
// standard user gets for another account's or an elevated process — means a
// process may well be there, so it reads alive with an unknown identity.
//
// That direction is the safe one for every caller: waitForDaemonReady keeps
// polling until its own timeout instead of reporting a crash that did not
// happen, and the identity checks (isQuildName on an empty name) never treat
// a process they cannot name as quild. tasklist got this backwards: over ssh
// it prints "ERROR: Access denied" and exits 0, which read as "no such
// process", so a daemon `quil --stdio` had just spawned was reported dead at
// once.
func openFailureMeansAlive(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == win32ErrorInvalidParameter {
		return false
	}
	return true
}

// exitCodeMeansAlive answers processProbe once a handle opened. A handle to an
// EXITED process still opens while anything holds one, so liveness is the
// exit status, not the open succeeding. A process that exits with 259 itself
// reads alive; nothing quil runs does.
func exitCodeMeansAlive(code uint32) bool {
	return code == win32StillActive
}
