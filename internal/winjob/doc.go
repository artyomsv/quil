// Package winjob decides how a quil daemon is started from inside a Windows
// job that kills its members when it closes — the job Win32-OpenSSH puts every
// ssh session into — and holds the Task Scheduler and token plumbing that
// decision needs.
//
// Same split as internal/notify: every file with logic is platform-neutral so
// Linux CI compiles and tests it; the //go:build windows files hold syscalls
// only, and other.go answers "not in a job" / "unsupported" everywhere else.
//
// Measured on Windows 10 22H2 with OpenSSH Server (issue #236): an admin
// account over ssh gets a HIGH token, so a daemon started there as-is would be
// elevated and drivable by any Medium desktop process through its socket. The
// decision therefore never spawns from inside such a job with the unlowered
// token.
package winjob
