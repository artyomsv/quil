//go:build windows

package gitinfo

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW. Not in syscall's constant set, so it is spelled out here
// the same way cmd/quil/proc_windows.go spells DETACHED_PROCESS.
const _CREATE_NO_WINDOW = 0x08000000

// hideWindow stops a git invocation from flashing a console window.
//
// The daemon is itself spawned DETACHED_PROCESS, so it owns no console — and a
// Windows child process started from a console-less parent gets a BRAND NEW
// console allocated for it, which is a real window that appears and vanishes.
// With a probe per checkout every few seconds that is a stream of flashing CMD
// windows over whatever the user is doing.
//
// This is a process-creation flag, not a consequence of shelling out to git:
// the same commands run silently once the flag is set, and on Unix no window
// exists to suppress in the first place (see proc_other.go, where this is a
// no-op). Any future exec in this package needs the same treatment.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= _CREATE_NO_WINDOW
}
