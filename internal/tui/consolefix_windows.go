//go:build windows

package tui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/artyomsv/quil/internal/logger"
)

var (
	fixKernel32                    = syscall.NewLazyDLL("kernel32.dll")
	fixUser32                      = syscall.NewLazyDLL("user32.dll")
	procFixGetConsoleWindow        = fixKernel32.NewProc("GetConsoleWindow")
	procFixGetClientRect           = fixUser32.NewProc("GetClientRect")
	procFixGetCurrentConsoleFont   = fixKernel32.NewProc("GetCurrentConsoleFont")
	procFixGetConsoleFontSize      = fixKernel32.NewProc("GetConsoleFontSize")
	procFixSetConsoleScreenBufSize = fixKernel32.NewProc("SetConsoleScreenBufferSize")
	procFixSetConsoleWindowInfo    = fixKernel32.NewProc("SetConsoleWindowInfo")
	procFixLargestWindowSize       = fixKernel32.NewProc("GetLargestConsoleWindowSize")
	procFixIsZoomed                = fixUser32.NewProc("IsZoomed")
)

type fixRect struct{ Left, Top, Right, Bottom int32 }

type fixConsoleFontInfo struct {
	Font     uint32
	FontSize windows.Coord
}

// activeConsoleOut opens CONOUT$ — a handle to the console's currently
// ACTIVE screen buffer (the alt screen while Bubble Tea is running).
func activeConsoleOut() (windows.Handle, func(), bool) {
	name, err := windows.UTF16PtrFromString("CONOUT$")
	if err != nil {
		return 0, nil, false
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, nil, false
	}
	return h, func() { _ = windows.CloseHandle(h) }, true
}

// fixupConsoleGrid grows the console screen buffer + window to match the
// window's client pixel area. Legacy conhost never grows the grid itself
// when the window is enlarged/maximized (it paints dead space and keeps
// reporting the stale cell size), so the app has to do it. Safe no-op when
// the grid already fits, when any metric is unavailable, or when the window
// shrank (conhost handles shrinks natively). Windows Terminal never needs
// this — its grid always follows the window — and the grow branch simply
// never triggers there.
func fixupConsoleGrid() {
	hwnd, _, _ := procFixGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	var rc fixRect
	if ret, _, _ := procFixGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); ret == 0 {
		return
	}

	// CONOUT$ resolves to the console's ACTIVE screen buffer. This matters:
	// Bubble Tea runs in the VT alternate screen, which conhost implements
	// as a separate screen buffer — the visible window belongs to IT, while
	// os.Stdout still refers to the original main buffer. Resize calls on
	// the main buffer "succeed" but change nothing on screen (observed:
	// fixup logged success every second while the grid stayed 117x30).
	h, closeOut, ok := activeConsoleOut()
	if !ok {
		return
	}
	defer closeOut()

	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(h, &info); err != nil {
		return
	}

	// Font cell size in pixels: GetCurrentConsoleFont gives the font index,
	// GetConsoleFontSize resolves it to pixels (the COORD comes back packed
	// in the return value: X in the low word, Y in the high word).
	var cfi fixConsoleFontInfo
	if ret, _, _ := procFixGetCurrentConsoleFont.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&cfi))); ret == 0 {
		return
	}
	fsRaw, _, _ := procFixGetConsoleFontSize.Call(uintptr(h), uintptr(cfi.Font))
	fontW := int(int16(fsRaw & 0xffff))
	fontH := int(int16(fsRaw >> 16))

	curCols := int(info.Window.Right - info.Window.Left + 1)
	curRows := int(info.Window.Bottom - info.Window.Top + 1)

	cols, rows, grow := consoleGridTarget(int(rc.Right), int(rc.Bottom), fontW, fontH, curCols, curRows)
	if !grow {
		return
	}

	// Diagnostics: conhost has been observed accepting the resize once and
	// silently reverting/ignoring afterwards — capture every metric that
	// could explain a clamp.
	largestRaw, _, _ := procFixLargestWindowSize.Call(uintptr(h))
	largestW := int(int16(largestRaw & 0xffff))
	largestH := int(int16(largestRaw >> 16))
	zoomed, _, _ := procFixIsZoomed.Call(hwnd)
	logger.Debug("console grid fixup: cur=%dx%d target=%dx%d client=%dx%dpx font=%dx%d buf=%dx%d largest=%dx%d zoomed=%v",
		curCols, curRows, cols, rows, rc.Right, rc.Bottom, fontW, fontH, info.Size.X, info.Size.Y, largestW, largestH, zoomed != 0)

	// conhost refuses windows larger than GetLargestConsoleWindowSize.
	if largestW > 0 && cols > largestW {
		cols = largestW
	}
	if largestH > 0 && rows > largestH {
		rows = largestH
	}
	if cols <= curCols && rows <= curRows {
		return
	}

	// The buffer must always be at least as large as the window: grow the
	// buffer first, then the window rect. Never shrink an existing buffer
	// axis (the main buffer may carry scrollback history).
	bufW := cols
	if int(info.Size.X) > bufW {
		bufW = int(info.Size.X)
	}
	bufH := rows
	if int(info.Size.Y) > bufH {
		bufH = int(info.Size.Y)
	}
	bufCoord := uintptr(uint32(uint16(bufW)) | uint32(uint16(bufH))<<16)
	if ret, _, err := procFixSetConsoleScreenBufSize.Call(uintptr(h), bufCoord); ret == 0 {
		logger.Debug("console grid fixup: SetConsoleScreenBufferSize(%dx%d): %v", bufW, bufH, err)
		return
	}
	sr := windows.SmallRect{Left: 0, Top: 0, Right: int16(cols - 1), Bottom: int16(rows - 1)}
	if ret, _, err := procFixSetConsoleWindowInfo.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&sr))); ret == 0 {
		logger.Debug("console grid fixup: SetConsoleWindowInfo(%dx%d): %v", cols, rows, err)
		return
	}

	// Mirror the growth onto the MAIN screen buffer (os.Stdout handle is
	// not used — open it via the std handle API). conhost consults the
	// main buffer's dimensions when it rebuilds/validates the alt buffer,
	// and a stale small main buffer has been observed snapping the visible
	// grid back within a second.
	if hStd, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil {
		var mainInfo windows.ConsoleScreenBufferInfo
		if windows.GetConsoleScreenBufferInfo(hStd, &mainInfo) == nil {
			mw, mh := bufW, bufH
			if int(mainInfo.Size.X) > mw {
				mw = int(mainInfo.Size.X)
			}
			if int(mainInfo.Size.Y) > mh {
				mh = int(mainInfo.Size.Y)
			}
			mainCoord := uintptr(uint32(uint16(mw)) | uint32(uint16(mh))<<16)
			procFixSetConsoleScreenBufSize.Call(uintptr(hStd), mainCoord)
			msr := windows.SmallRect{Left: 0, Top: 0, Right: int16(cols - 1), Bottom: int16(rows - 1)}
			procFixSetConsoleWindowInfo.Call(uintptr(hStd), 1, uintptr(unsafe.Pointer(&msr)))
		}
	}

	// Verify the grow actually stuck — conhost can return success and
	// keep the old grid.
	var after windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(h, &after) == nil {
		logger.Debug("console grid fixup: applied %dx%d -> post-apply window=%dx%d buf=%dx%d",
			cols, rows,
			int(after.Window.Right-after.Window.Left+1), int(after.Window.Bottom-after.Window.Top+1),
			after.Size.X, after.Size.Y)
	}
}
