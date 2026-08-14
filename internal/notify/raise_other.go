//go:build !windows

package notify

// RaiseWindowFor is a no-op off Windows: nothing here can raise a terminal
// window, and the whole toast path is Windows-only anyway. It reports success
// rather than an "unsupported" error — no caller off Windows ever reaches it
// (activation arrives over a Windows-only pipe), and a stub that manufactures
// a failure would put a wrong line in a log on the platform CI runs.
func RaiseWindowFor(pid int) error { return nil }
