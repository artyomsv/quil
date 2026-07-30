//go:build windows

package daemon

import "os"

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
func filesystemRoots() []string {
	var roots []string
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Stat(root); err == nil {
			roots = append(roots, root)
		}
	}
	return roots
}
