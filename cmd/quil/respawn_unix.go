//go:build !windows

package main

import "syscall"

// execSelf replaces this process with the freshly-installed quil. On success
// it never returns.
//
// Replacing rather than spawning a child is the point: the child-plus-wrapper
// shape left two quil processes on one terminal, and on macOS Terminal.app the
// replacement TUI came up with mouse reporting off — the wheel scrolled the
// host's own buffer in every tab until quil was quit and relaunched (#268).
// After exec there is one process on the terminal, exactly as on a fresh
// launch, and the shell's job control still sees the same pid.
//
// Deferred functions in launchTUI do not run, which is safe: Go opens every
// file and socket close-on-exec, and the log writer is unbuffered.
func execSelf(exe string, args, env []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), env)
}
