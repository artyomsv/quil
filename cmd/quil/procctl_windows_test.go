//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProcessProbe_Self_AliveWithImagePath(t *testing.T) {
	alive, comm := processProbe(os.Getpid())
	if !alive {
		t.Fatal("own process reported dead")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if !strings.EqualFold(filepath.Base(comm), filepath.Base(exe)) {
		t.Errorf("comm = %q, want a path ending in %q", comm, filepath.Base(exe))
	}
}

func TestProcessProbe_NoSuchProcess_Dead(t *testing.T) {
	// Windows PIDs are multiples of 4; walk down from a high one until
	// OpenProcess itself says "no such process", so the assertion is about
	// processProbe and not about the PID being free.
	pid := -1
	for p := uint32(0x3ffffffc); p > 0x3fff0000; p -= 4 {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p)
		if err == nil {
			_ = windows.CloseHandle(h)
			continue
		}
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			pid = int(p)
			break
		}
	}
	if pid < 0 {
		t.Skip("no free pid found")
	}
	if alive, comm := processProbe(pid); alive {
		t.Errorf("processProbe(%d) = alive (%q), want dead", pid, comm)
	}
}

// A handle held open keeps an exited process's object alive, so OpenProcess
// still succeeds; liveness must come from the exit status.
func TestProcessProbe_ExitedButHandleHeld_Dead(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 3")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start cmd.exe: %v", err)
	}
	pid := cmd.Process.Pid
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		_ = cmd.Wait()
		t.Skipf("cannot hold a handle to the child: %v", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if _, err := windows.WaitForSingleObject(h, 10_000); err != nil {
		t.Fatalf("WaitForSingleObject: %v", err)
	}
	_ = cmd.Wait()

	if alive, comm := processProbe(pid); alive {
		t.Errorf("exited child reported alive (%q)", comm)
	}
}
