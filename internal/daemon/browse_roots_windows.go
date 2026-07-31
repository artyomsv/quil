//go:build windows

package daemon

import "time"

// rootProbeTimeout bounds ONE drive-letter probe.
//
// Deliberately much shorter than the listing budget: this is a liveness check,
// not a listing, and a drive that cannot answer it in a second is exactly the
// drive this function exists to omit. Without a per-probe bound, a single
// disconnected mapping would spend the whole shared deadline before the
// directory the user actually asked for was ever read.
const rootProbeTimeout = time.Second

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
// wedge the picker for every later listing in the session. Each probe is
// therefore bounded, and the shared deadline caps the whole sweep, so 26
// unresponsive drives cost one budget rather than 26.
func filesystemRoots(deadline time.Time) []string {
	var roots []string
	for c := 'A'; c <= 'Z'; c++ {
		budget := time.Until(deadline)
		if budget <= 0 {
			// The sweep has spent the response's whole budget. Returning what
			// answered so far beats returning nothing: a partial drive list is
			// still navigable, and the read that follows will report the
			// timeout on its own.
			break
		}
		if budget > rootProbeTimeout {
			budget = rootProbeTimeout
		}
		root := string(c) + `:\`
		if isDir, answered := statIsDirWithin(root, budget); answered && isDir {
			roots = append(roots, root)
		}
	}
	return roots
}
