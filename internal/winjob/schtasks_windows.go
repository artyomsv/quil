//go:build windows

package winjob

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// schtasksPath is resolved from SystemRoot, never PATH: a writable PATH entry
// ahead of System32 must not decide what registers a logon task.
func schtasksPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "schtasks.exe")
}

// Schtasks runs schtasks.exe and returns its combined output. With lowered it
// runs under LoweredToken, so a task registered from a High (ssh or elevated)
// session is owned by the user and removable from a normal desktop shell
// (issue #236 probe 14).
func Schtasks(args []string, lowered bool) (string, error) {
	cmd := exec.Command(schtasksPath(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if lowered {
		tok, err := LoweredToken()
		if err != nil {
			return "", fmt.Errorf("lower token for schtasks: %w", err)
		}
		defer tok.Close()
		cmd.SysProcAttr.Token = syscall.Token(tok)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("schtasks %s: %w", args[0], err)
	}
	return string(out), nil
}

// TaskExists reports whether a task with this name is registered.
func TaskExists(name string) bool {
	_, err := Schtasks([]string{"/Query", "/TN", name}, false)
	return err == nil
}

// RunTask starts the named task now.
func RunTask(name string) error {
	_, err := Schtasks([]string{"/Run", "/TN", name}, false)
	return err
}
