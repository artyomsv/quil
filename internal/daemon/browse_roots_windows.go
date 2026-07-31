//go:build windows

package daemon

import "time"

// filesystemRoots lists the drive letters that respond.
//
// Windows has no single root, so "up" from `C:\` means the drive list. This is
// the daemon's own set: the browser used to build it in the TUI process, which
// against a remote host enumerates the wrong machine's drives — or, against a
// Linux daemon, invents drives that do not exist.
//
// Stat rather than GetLogicalDrives: an empty optical drive or a disconnected
// network mapping still appears in the bitmask but cannot be listed, so it
// would offer a row that fails on selection.
//
// That stat is the hazard, and it is why this takes a deadline. A mapped
// network drive whose server has gone away parks os.Stat in the kernel for as
// long as the redirector's own timeout, which no context can shorten — and this
// runs while the browse single-flight slot is held, so an unreachable Z: would
// wedge the picker for every later listing in the session. sweepRoots bounds
// both halves of that: each probe individually, and the number of probes the
// sweep may abandon.
func filesystemRoots(deadline time.Time) []string {
	return sweepRoots(driveLetters(), deadline, statIsDirWithin)
}

// driveLetters is the candidate set: every letter, in order, because a drive
// map is arbitrary and there is no cheaper way to learn which exist that does
// not reintroduce the GetLogicalDrives problem described above.
func driveLetters() []string {
	out := make([]string, 0, 26)
	for c := 'A'; c <= 'Z'; c++ {
		out = append(out, string(c)+`:\`)
	}
	return out
}
