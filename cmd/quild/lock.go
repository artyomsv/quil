package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// errLockHeld means another quild holds this QUIL_HOME's startup lock — it is
// starting or already running.
var errLockHeld = errors.New("another quild holds the startup lock")

// startupLockWait bounds how long a starting daemon waits for the lock. Long
// enough to cover a predecessor that is still shutting down (quil restart, an
// upgrade), short enough that a redundant spawn does not linger.
const startupLockWait = 10 * time.Second

// acquireStartupLock takes an exclusive OS lock on <dir>/quild.lock, retrying
// every poll until wait elapses, and returns the function that releases it.
//
// It closes the race the healthy-socket probe cannot: two daemons that start
// together both see no healthy peer, both listen, and the second's listener
// removes the first's socket — orphaning a daemon that keeps a full set of
// live panes (measured 2026-09-26: two ssh `daemon start` runs at once gave two
// daemons with eight pane processes each). The lock is held for the process's
// whole life and the OS drops it on exit or crash, so a stale lock cannot
// block a later start. The file is never deleted: removing a lock file races
// the next opener.
func acquireStartupLock(dir string, wait, poll time.Duration) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "quild.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open startup lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		locked, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
		}
		if locked {
			return func() { f.Close() }, nil // closing the handle releases the lock
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, errLockHeld
		}
		time.Sleep(poll)
	}
}
