//go:build !windows

package sbx

import "os/exec"

// hideWindow is a no-op everywhere but Windows: no other platform allocates a
// console for a child of a console-less parent.
func hideWindow(*exec.Cmd) {}
