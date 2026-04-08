//go:build windows

package clipboard

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cfUnicodeText = 13
const gmemMoveable = 0x0002

// maxClipboardSize limits clipboard reads to prevent excessive memory allocation.
const maxClipboardSize = 10 * 1024 * 1024 // 10 MB

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	getClipboardData = user32.NewProc("GetClipboardData")
	emptyClipboard   = user32.NewProc("EmptyClipboard")
	setClipboardData = user32.NewProc("SetClipboardData")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
	globalAlloc      = kernel32.NewProc("GlobalAlloc")
	globalFree       = kernel32.NewProc("GlobalFree")
	globalSize       = kernel32.NewProc("GlobalSize")
)

func read() (string, error) {
	r, _, err := openClipboard.Call(0)
	if r == 0 {
		return "", fmt.Errorf("OpenClipboard: %w", err)
	}
	defer closeClipboard.Call()

	h, _, err := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("GetClipboardData: %w", err)
	}

	ptr, _, err := globalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock: %w", err)
	}
	defer globalUnlock.Call(h)

	// Use windows.UTF16PtrToString for safe null-terminated UTF-16 conversion.
	s := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr)))
	if len(s) > maxClipboardSize {
		s = s[:maxClipboardSize]
	}
	return s, nil
}

func write(text string) error {
	r, _, err := openClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("OpenClipboard: %w", err)
	}
	defer closeClipboard.Call()

	r, _, err = emptyClipboard.Call()
	if r == 0 {
		return fmt.Errorf("EmptyClipboard: %w", err)
	}

	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("UTF16FromString: %w", err)
	}
	size := uintptr(len(utf16) * 2)

	h, _, err := globalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %w", err)
	}

	ptr, _, err := globalLock.Call(h)
	if ptr == 0 {
		globalFree.Call(h) // free on lock failure
		return fmt.Errorf("GlobalLock: %w", err)
	}

	dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16))
	copy(dst, utf16)
	globalUnlock.Call(h)

	// SetClipboardData takes ownership of h on success.
	// On failure, we must free it ourselves.
	r, _, err = setClipboardData.Call(cfUnicodeText, h)
	if r == 0 {
		globalFree.Call(h)
		return fmt.Errorf("SetClipboardData: %w", err)
	}
	return nil
}
