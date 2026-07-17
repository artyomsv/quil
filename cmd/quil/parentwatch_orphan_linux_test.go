package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestParentWatchOrphanHelper is NOT a test in the normal suite (it skips
// unless the env gate is set). It is the CHILD half of the orphan
// integration test below: re-executed via the test binary, it arms the
// watchdog with a tiny poll interval and then blocks. If the watchdog
// works, the process exits 0 long before the sleep elapses; exit code 2
// means the watchdog never fired.
func TestParentWatchOrphanHelper(t *testing.T) {
	if os.Getenv("QUIL_PW_HELPER") != "1" {
		t.Skip("helper process for TestWatchParentExit_OrphanExitsOnReparent")
	}
	parentWatchInterval = 5 * time.Millisecond
	watchParentExit()
	time.Sleep(30 * time.Second)
	os.Exit(2)
}

// TestWatchParentExit_OrphanExitsOnReparent exercises the real reparent
// path: an intermediate sh spawns the helper (so sh is its parent), keeps
// living for 1 s while the helper arms the watchdog, then exits — the
// helper is reparented to the init/reaper and must self-terminate within a
// few poll intervals. This is the Linux-container-reachable counterpart of
// the Windows e2e (which needs a real host).
func TestWatchParentExit_OrphanExitsOnReparent(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	// Helper stdout/stderr go to /dev/null so the backgrounded process
	// doesn't hold our stdout pipe open past sh's exit.
	script := fmt.Sprintf(
		"QUIL_PW_HELPER=1 %q -test.run '^TestParentWatchOrphanHelper$' >/dev/null 2>&1 & echo $!; sleep 1",
		self)
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("spawn helper via sh: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 1 {
		t.Fatalf("helper pid parse: %q (%v)", out, err)
	}

	// sh has exited (Output returned) → helper is reparented. It must die
	// within a few 5 ms polls; 10 s is a generous CI bound. An orphan that
	// is reaped shows ENOENT; one whose new parent never reaps it stays
	// visible as a zombie — both count as exited.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if helperExited(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL) // don't leak the helper on failure
	t.Fatal("orphaned helper still running 10s after reparent — unix watchdog did not fire")
}

// helperExited reports whether pid is gone or a zombie (exited but not yet
// reaped by its adoptive parent — /proc state 'Z').
func helperExited(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true // ENOENT: fully gone
	}
	// Field 3 (state) follows the last ')' — comm may contain spaces.
	rest := string(stat)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 && i+2 < len(rest) {
		return rest[i+2] == 'Z'
	}
	return false
}
