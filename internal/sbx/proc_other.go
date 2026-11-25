//go:build !windows

package sbx

import (
	"errors"
	"os/exec"
	"syscall"
)

// hideWindow is a no-op everywhere but Windows: no other platform allocates a
// console for a child of a console-less parent.
func hideWindow(*exec.Cmd) {}

// stopTreeOnCancel runs the command in its own process group and, when its
// context ends, kills the whole group rather than only the command: a step
// that ran out of time must not leave what it started running behind it. A
// command that finishes is left alone, along with anything it deliberately
// left running.
func stopTreeOnCancel(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		// The group id is the command's pid (Setpgid). A group already gone
		// is not an error.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}
