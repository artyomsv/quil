//go:build !windows

package notify

// DetachOwnConsole is a no-op off Windows: nothing here allocates a console
// window to a process that was launched without a terminal.
func DetachOwnConsole() {}
