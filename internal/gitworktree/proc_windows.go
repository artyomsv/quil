//go:build windows

package gitworktree

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW. Not in syscall's constant set, so it is spelled out here
// the same way gitinfo/proc_windows.go and cmd/quil/proc_windows.go spell
// their flags.
const _CREATE_NO_WINDOW = 0x08000000

// hideWindow stops a git invocation from flashing a console window.
//
// The daemon is spawned DETACHED_PROCESS, so it owns no console — and a
// Windows child started from a console-less parent gets a BRAND NEW console
// allocated, which is a real window that appears and vanishes. `git worktree
// add` is user-initiated rather than a ticker, so this costs one flash per
// pane created rather than a stream of them, but a window flashing over the
// dialog the user is still looking at is the more startling of the two.
//
// Duplicated from gitinfo rather than shared: a package that exists to keep
// WRITES out of the read-only one would defeat itself by importing it, and the
// alternative — a third package holding four lines — buys nothing. Any future
// exec added here needs the same treatment.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= _CREATE_NO_WINDOW
}
