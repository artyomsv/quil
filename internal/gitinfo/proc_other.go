//go:build !windows

package gitinfo

import "os/exec"

// hideWindow is a no-op everywhere but Windows. A Unix process has no console
// window to create, so a subprocess is invisible by construction — there is
// nothing here to suppress and no per-platform cost to running git this way.
//
// The Windows arm is not about git: it is about a console-less parent causing
// the OS to allocate a new console for each child. See proc_windows.go.
func hideWindow(cmd *exec.Cmd) {}
