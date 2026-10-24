package binswap

import (
	"errors"
	"os"
)

// LockFile takes an exclusive OS lock on the file at path, creating it when
// it does not exist, and returns the release. ErrLockHeld means another
// holder — process or goroutine — has it; any other error is the open's or
// the lock call's, as it is.
//
// This is the lock `quild plugin-update` serialises one target's updaters
// with (toolupdate.Run via WaitLock). Unlike the pid-file lock above
// (AcquireLock, Quil's own apply lock), ownership is the OS's: flock on Unix,
// LockFileEx on Windows, both dropped by the kernel when the holder's handle
// closes or its process dies — so there is no abandoned-file logic, nothing
// to reclaim, and the pathname is NEVER unlinked. Unlinking is what made the
// pid-file takeover non-exclusive: two waiters could both judge one dead
// holder abandoned, and the second's unlink removed the first's fresh lock.
// The file stays on disk after release; it is empty and harmless, and
// RemoveBackups and toolupdate's staging sweep never match its name.
//
// Exclusive within one process too: flock locks belong to the OPEN FILE
// DESCRIPTION, so two os.OpenFile calls in one process are two descriptions
// that exclude each other (a dup of one fd would share, which nothing here
// does); LockFileEx byte-range locks belong to the HANDLE, so two handles in
// one process exclude each other the same way. TestLockFile_ExcludesWithinProcess
// pins both.
func LockFile(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockHandle(f); err != nil {
		f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, ErrLockHeld
		}
		return nil, err
	}
	return func() {
		unlockHandle(f)
		f.Close()
	}, nil
}

// errLockBusy is the platform lock call's "someone else holds it" answer,
// mapped to ErrLockHeld by LockFile.
var errLockBusy = errors.New("lock busy")
