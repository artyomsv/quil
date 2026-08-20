//go:build windows

package tui

// killIsGraceful is false here: Windows offers no graceful stop for an
// arbitrary process, so the confirm says the process is terminated outright.
const killIsGraceful = false
