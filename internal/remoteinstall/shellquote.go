// Package remoteinstall provisions the quil binaries on a remote host over an
// existing ssh connection: probe what the host is, fetch the matching release
// locally, verify it, and stream it into a small POSIX install script.
//
// The laptop pushes rather than the server pulling, so a host with no route to
// GitHub can still be provisioned, and the installed version matches the TUI by
// construction rather than by pinning a variable.
package remoteinstall

import "strings"

// ShellSingleQuote wraps s so a POSIX shell reads it as exactly one literal
// word.
//
// Required because ssh joins its command arguments with spaces and applies no
// quoting of its own: whatever reaches the far side is re-split by the remote
// shell. Single quotes suppress every form of expansion — variables, command
// substitution, globbing, backslashes — so the only character needing care is
// ' itself, which is closed, escaped and reopened.
//
// Every value interpolated into a remote command string goes through here.
// That includes values that look trustworthy: the install directory is derived
// from the remote's own $HOME, which can contain a space or an apostrophe.
func ShellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
