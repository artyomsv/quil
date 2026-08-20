//go:build !windows

package tui

// killIsGraceful reports whether stopping a process asks it to exit before
// forcing it.
//
// True everywhere except Windows, which has no graceful signal for an arbitrary
// process — TerminateProcess is the only option. The confirm text branches on
// this rather than promising a graceful stop the platform cannot deliver.
const killIsGraceful = true
