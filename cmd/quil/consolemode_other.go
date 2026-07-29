//go:build !windows

package main

// Console-mode save/restore is a Windows-only concern. See
// consolemode_windows.go for what it fixes.
//
// Unix terminals need nothing here. ssh restores termios on exit, and even when
// it is killed before doing so, the shell resets the line discipline for the next
// prompt — and, unlike the Windows console, an LF on a terminal in cooked mode
// still returns to column 0 via ONLCR.

func saveConsoleMode() {}

func restoreConsoleMode() {}
