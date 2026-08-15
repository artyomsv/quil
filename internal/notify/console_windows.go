//go:build windows

package notify

import "golang.org/x/sys/windows"

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procShowWindow  = user32.NewProc("ShowWindow")
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleW = kernel32.NewProc("GetConsoleWindow")
	procFreeConsole = kernel32.NewProc("FreeConsole")
)

const swHide = 0

// DetachOwnConsole hides and releases the console window Windows allocates to
// this process, and must be the FIRST thing an activation handler does.
//
// `quil activate` is a console binary, so launching it for a toast click
// allocates it a real, visible, focusable console WINDOW. It appears in front
// of the user, takes the foreground, and is destroyed when the handler exits —
// leaving focus on a window that no longer exists, which is why typing straight
// after a click reached neither the pane nor the application the user came
// from.
//
// This only SHORTENS the window's life. The console is created during process
// startup, before any Go code runs, so it has already been painted; the flicker
// survives. Removing it entirely is why cmd/quil-activate exists as a separate
// `-H windowsgui` binary, and why `quil notify setup` registers that instead.
// This path remains for installs whose registry still names `quil activate`.
//
// After this the process has no stdout or stderr. Nothing in the activation
// path prints; it reports through notify-activate.log precisely because it has
// no terminal to report to.
func DetachOwnConsole() {
	if hwnd, _, _ := procGetConsoleW.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
	procFreeConsole.Call()
}
