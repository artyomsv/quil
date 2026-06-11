//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// processProbe reports whether pid is alive and its image name via tasklist
// (for daemon-identity verification before killing).
func processProbe(pid int) (alive bool, comm string) {
	// CSV, no header: "quild.exe","29153","Console",...  Empty / INFO line
	// means no such process.
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false, ""
	}
	line := strings.TrimSpace(string(out))
	if line == "" || !strings.HasPrefix(line, "\"") {
		return false, ""
	}
	fields := strings.SplitN(line, "\",\"", 2)
	name := strings.TrimPrefix(fields[0], "\"")
	return true, name
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
