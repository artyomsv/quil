//go:build linux || darwin || freebsd

package main

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// processProbe reports whether pid is alive and, when possible, its command
// name (for daemon-identity verification before signaling).
func processProbe(pid int) (alive bool, comm string) {
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, ""
	}
	// ps -o comm= prints the command name (full path on macOS, short name
	// on Linux); empty output means the process vanished between the probe
	// and the ps call.
	out, perr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	name := strings.TrimSpace(string(out))
	if perr != nil || name == "" {
		return err == nil, ""
	}
	return true, name
}

// signalTerm asks the daemon to shut down gracefully via SIGTERM (handled
// by the daemon's signal loop: final snapshot + PTY cleanup).
func signalTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
