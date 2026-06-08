//go:build !windows

package tui

// fixupConsoleGrid is a no-op outside Windows — only legacy conhost fails
// to grow its cell grid when the window is enlarged (see consolefix.go).
func fixupConsoleGrid() {}
