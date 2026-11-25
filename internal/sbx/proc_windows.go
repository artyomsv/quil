//go:build windows

package sbx

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW, spelled out as internal/sandbox/proc_windows.go does.
const _CREATE_NO_WINDOW = 0x08000000

// stopTreeOnCancel is the default cancellation on Windows: only the command
// itself is killed when its context ends. Ending what it started would need
// a job object; Run's WaitDelay still returns on time when a descendant holds
// the output, but that descendant can outlive the cancelled step.
func stopTreeOnCancel(*exec.Cmd) {}

// hideWindow stops an sbx or host-script invocation from flashing a console
// window: the daemon owns no console, so Windows would allocate one per child.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= _CREATE_NO_WINDOW
}
