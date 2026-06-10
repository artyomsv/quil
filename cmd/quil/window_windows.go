//go:build windows

package main

import (
	"log"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	user32           = syscall.NewLazyDLL("user32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	getWindowRect    = user32.NewProc("GetWindowRect")
	moveWindow       = user32.NewProc("MoveWindow")
	showWindow       = user32.NewProc("ShowWindow")
	isZoomed         = user32.NewProc("IsZoomed")
	getClassNameW    = user32.NewProc("GetClassNameW")
)

// realConsoleClassName is the window class of a genuine legacy conhost window.
// A maximizable/movable console window only exists under conhost; under any
// ConPTY host (Windows Terminal, VS Code, IDE terminals) GetConsoleWindow
// returns a hidden compatibility window of class "PseudoConsoleWindow" instead.
const realConsoleClassName = "ConsoleWindowClass"

// isRealConsoleClass reports whether a console window class belongs to a real
// conhost window (safe to move/maximize/persist) rather than a ConPTY ghost.
//
// This is the discriminator the previous fix got wrong: it gated on
// IsWindowVisible, assuming the ConPTY ghost is invisible. It is NOT — the
// ghost has WS_VISIBLE set (IsWindowVisible returns true) while sitting at a
// zero rect, so the gate never fired in a real Windows Terminal session and
// ShowWindow(SW_MAXIMIZE) still detonated the invisible full-screen window
// that swallows mouse input desktop-wide. The window CLASS is the reliable
// signal: conhost is "ConsoleWindowClass", the ConPTY ghost is
// "PseudoConsoleWindow".
func isRealConsoleClass(class string) bool {
	return class == realConsoleClassName
}

// windowClassName returns the Win32 class name of hwnd.
func windowClassName(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := getClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

// realConsoleWindow returns the console window handle only when it is a genuine
// conhost window. Returns 0 under a ConPTY host (Windows Terminal, VS Code,
// IDE terminals), where the host owns window geometry and the "console window"
// is a hidden PseudoConsoleWindow that must never be moved or maximized.
func realConsoleWindow() uintptr {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return 0
	}
	if !isRealConsoleClass(windowClassName(hwnd)) {
		return 0
	}
	return hwnd
}

const swMaximize = 3 // SW_MAXIMIZE

type rect struct {
	Left, Top, Right, Bottom int32
}

func restoreWindowSizePlatform(ws *windowState) {
	hwnd := realConsoleWindow()
	if hwnd == 0 {
		log.Printf("window restore skipped: not a real conhost window (ConPTY host manages its own size)")
		return
	}

	if ws.Maximized {
		showWindow.Call(hwnd, swMaximize)
		log.Printf("restored window: maximized")
		return
	}

	if ws.PixelWidth < 200 || ws.PixelHeight < 100 ||
		ws.PixelWidth > 32767 || ws.PixelHeight > 32767 {
		return
	}

	// Get current position to preserve X/Y
	var r rect
	ret, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return // GetWindowRect failed
	}

	ret, _, err := moveWindow.Call(
		hwnd,
		uintptr(int64(r.Left)),
		uintptr(int64(r.Top)),
		uintptr(ws.PixelWidth),
		uintptr(ws.PixelHeight),
		1, // repaint
	)
	if ret == 0 {
		log.Printf("MoveWindow failed: %v", err)
	} else {
		log.Printf("restored window size: %dx%d pixels", ws.PixelWidth, ws.PixelHeight)
	}
}

func saveWindowSizePlatform(ws *windowState) {
	hwnd := realConsoleWindow()
	if hwnd == 0 {
		// Keep whatever pixel/maximized values the caller pre-filled from the
		// previous session — under a ConPTY host there is no real window whose
		// geometry we could read, and the ghost's IsZoomed/GetWindowRect would
		// poison the persisted values.
		return
	}

	// Check if maximized
	ret, _, _ := isZoomed.Call(hwnd)
	ws.Maximized = ret != 0

	var r rect
	ret, _, _ = getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret != 0 {
		ws.PixelWidth = int(r.Right - r.Left)
		ws.PixelHeight = int(r.Bottom - r.Top)
	}
}
