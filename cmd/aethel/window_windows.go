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
)

const swMaximize = 3 // SW_MAXIMIZE

type rect struct {
	Left, Top, Right, Bottom int32
}

func restoreWindowSizePlatform(ws *windowState) {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
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
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
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
