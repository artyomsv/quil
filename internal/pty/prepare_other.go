//go:build !windows

package pty

// PrepareBundledConPTY is a no-op on platforms with a real PTY; only Windows
// needs the bundled ConPTY host.
func PrepareBundledConPTY(string) error { return nil }
