package binswap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// The pid-file lock Quil's own apply uses (cmd/quil/update_apply.go): a
// file created O_EXCL holding the holder's pid, with an abandoned-lock
// takeover. Two `quil` processes starting together both pass verification
// and both swap, and each one's cleanup sweep deletes backups by name, so
// the exclusion lives on disk. The takeover's judge-then-unlink is NOT
// exclusive between two reclaimers, which is why the plugin updater's
// per-target lock is the OS lock in flock.go instead (LockFile, WaitLock);
// this one is kept as it was for the apply path, out of that change's scope.

// ErrLockHeld is TryLock's and LockFile's answer when another holder has
// the lock.
var ErrLockHeld = errors.New("lock held by another process")

// AcquireLock takes the lock at path, returning a release func and whether
// it was taken (TryLock, with every failure read as "not taken").
func AcquireLock(path string, stale time.Duration) (release func(), ok bool) {
	release, err := TryLock(path, stale)
	if err != nil {
		return func() {}, false
	}
	return release, true
}

// TryLock takes the lock at path once. ErrLockHeld means a live holder has
// it; any other error — the directory is not writable, say — is returned as
// it is, so a waiter can fail fast instead of polling an EACCES as if it
// were a holder. A lock found abandoned (LockAbandoned) is claimed; if
// another process wins that race the second O_EXCL fails and the caller
// backs off rather than looping. release removes only OUR lock: if the
// holder outran the staleness window another process may hold the path by
// then, and removing it by name would hand a third process a lock while the
// second is mid-swap — precisely the interleaving this exists to prevent.
func TryLock(path string, stale time.Duration) (release func(), err error) {
	self := strconv.Itoa(os.Getpid())
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintln(f, self)
			f.Close()
			return func() {
				if data, rErr := os.ReadFile(path); rErr == nil &&
					strings.TrimSpace(string(data)) == self {
					removeLock(path)
				}
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if !LockAbandoned(path, stale) {
			return nil, ErrLockHeld
		}
		os.Remove(path)
	}
	return nil, ErrLockHeld
}

// lockRemoveRetries × lockRemoveRetryDelay bounds removeLock's wait on a
// sharing violation.
const (
	lockRemoveRetries    = 20
	lockRemoveRetryDelay = 10 * time.Millisecond
)

// removeLock deletes the lock file, retrying briefly when the delete is
// refused: on Windows a waiter reading the file (LockAbandoned's ReadFile,
// open for as long as one read takes) denies DELETE for that instant, and a
// release that gave up there would leave the lock on disk, read as held by
// every waiter until it aged out. A file already gone is done.
func removeLock(path string) {
	for i := 0; i < lockRemoveRetries; i++ {
		err := os.Remove(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(lockRemoveRetryDelay)
	}
}

// LockAbandoned reports whether the lock at path can be taken over. The
// recorded pid makes this mostly a fact rather than a guess: a lock whose
// holder is gone is reclaimable immediately, without waiting out the age
// window. The age cap stays as the backstop for the two cases liveness
// cannot answer — an unparseable lock file, and a pid since recycled by an
// unrelated long-running process, which would otherwise read as "held"
// forever. A live holder older than the cap is therefore still treated as
// abandoned, and the ownership check in release keeps the takeover safe if
// it somehow does run that long.
func LockAbandoned(path string, stale time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false // cannot judge it — leave it alone
	}
	if data, rErr := os.ReadFile(path); rErr == nil {
		if pid, pErr := strconv.Atoi(strings.TrimSpace(string(data))); pErr == nil && pid > 0 {
			if !processAlive(pid) {
				return true
			}
		}
	}
	return time.Since(info.ModTime()) > stale
}

// LockHeld reports whether another process holds a live lock at path. It
// fails CLOSED on anything but "no lock": a lock that cannot even be
// stat'd is no proof that no swap is running.
func LockHeld(path string, stale time.Duration) bool {
	if _, err := os.Stat(path); err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	return !LockAbandoned(path, stale)
}

// lockPollInterval is how often WaitLock retries a held lock.
const lockPollInterval = 250 * time.Millisecond

// WaitLock is LockFile (the OS lock) that waits, up to maxWait or ctx's
// end, for the holder to finish. Only ErrLockHeld is waited on; any other
// failure (an unwritable directory) is returned at once. onWait runs once,
// the first time it has to wait, so the caller can say so. The error names
// the path when the wait ran out. There is no staleness to judge: a dead
// holder's lock is already gone.
func WaitLock(ctx context.Context, path string, maxWait time.Duration, onWait func()) (release func(), err error) {
	deadline := time.Now().Add(maxWait)
	waited := false
	for {
		release, err := LockFile(path)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, ErrLockHeld) {
			return nil, err
		}
		if !waited {
			waited = true
			if onWait != nil {
				onWait()
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another update still holds %s after %v", path, maxWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}
}
