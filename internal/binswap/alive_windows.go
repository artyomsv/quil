//go:build windows

package binswap

import (
	"math"

	"golang.org/x/sys/windows"
)

// processAlive reports whether pid is a live process: cmd/quil's processProbe
// without the image name. The answers to a refused handle and to an exit
// code are mapped in alive_win32.go, which builds on the Linux CI.
func processAlive(pid int) bool {
	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return openFailureMeansAlive(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return exitCodeMeansAlive(code)
}
