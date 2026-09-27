//go:build windows

package main

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

// processProbe reports whether pid is alive and, when the process can be
// opened, its full image path (for daemon-identity verification before
// killing). The mapping of each answer lives in procprobe.go.
func processProbe(pid int) (alive bool, comm string) {
	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return false, ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return openFailureMeansAlive(err), ""
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true, ""
	}
	if !exitCodeMeansAlive(code) {
		return false, ""
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return true, ""
	}
	return true, windows.UTF16ToString(buf[:n])
}

// signalTerm is unsupported on Windows — there is no SIGTERM delivery to a
// detached process. Graceful shutdown is the IPC tier; callers skip to the
// kill tier on this error.
func signalTerm(int) error {
	return errors.New("SIGTERM not supported on windows")
}

func killProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
