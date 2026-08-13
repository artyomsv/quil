//go:build !windows

package notify

// RaiseWindowFor is a no-op off Windows: nothing here can raise a terminal
// window, and the whole toast path is Windows-only anyway.
func RaiseWindowFor(pid int) {}
