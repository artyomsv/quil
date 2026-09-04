//go:build windows

package codexhook

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetShortPathName = kernel32.NewProc("GetShortPathNameW")
)

// shortPathName returns the 8.3 form of p (GetShortPathNameW), which carries
// no spaces and so can stand unquoted on a cmd.exe line. The lookup fails on
// a volume with 8.3 names disabled, or for a path that does not exist; the
// caller then falls back to the quoted form.
func shortPathName(p string) (string, error) {
	long, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	// First call sizes the buffer (the count includes the terminator).
	n, _, e1 := procGetShortPathName.Call(uintptr(unsafe.Pointer(long)), 0, 0)
	if n == 0 {
		return "", e1
	}
	buf := make([]uint16, n)
	n, _, e1 = procGetShortPathName.Call(uintptr(unsafe.Pointer(long)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		if n == 0 {
			return "", e1
		}
		return "", errors.New("codexhook: short path did not fit its own buffer")
	}
	return syscall.UTF16ToString(buf[:n]), nil
}
