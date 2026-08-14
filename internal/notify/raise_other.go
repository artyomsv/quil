//go:build !windows

package notify

import (
	"errors"
	"time"
)

// RaiseWindowFor is a no-op off Windows: nothing here can raise a terminal
// window, and the whole toast path is Windows-only anyway. It reports success
// rather than an "unsupported" error — no caller off Windows ever reaches it
// (activation arrives over a Windows-only pipe), and a stub that manufactures
// a failure would put a wrong line in a log on the platform CI runs.
func RaiseWindowFor(pid int) (string, error) { return "noop", nil }

// DetachOwnConsole is a no-op off Windows: nothing here allocates a console
// window to a process that was launched without a terminal.
func DetachOwnConsole() {}

// ForegroundLockTimeout has no meaning off Windows — the foreground lock is a
// Win32 policy. Reported as an error rather than zero, because zero is a real
// value there ("apps may take focus") and would be a lie here.
func ForegroundLockTimeout() (time.Duration, error) {
	return 0, errors.New("notify: foreground lock timeout is Windows-only")
}
