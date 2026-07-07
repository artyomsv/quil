package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePidData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want int
	}{
		{"plain pid", "29153", 29153},
		{"trailing newline", "29153\n", 29153},
		{"surrounding whitespace", "  4242 \r\n", 4242},
		{"empty", "", 0},
		{"garbage", "not-a-pid", 0},
		{"negative", "-5", 0},
		{"zero", "0", 0},
		{"float", "12.5", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePidData([]byte(tt.data)); got != tt.want {
				t.Errorf("parsePidData(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestIsQuildName(t *testing.T) {
	tests := []struct {
		name string
		comm string
		want bool
	}{
		{"plain", "quild", true},
		{"windows exe", "quild.exe", true},
		{"windows exe uppercase", "QUILD.EXE", true},
		{"dev variant", "quild-dev", true},
		{"dev variant exe", "quild-dev.exe", true},
		{"debug variant", "quild-debug", true},
		{"full path macos", "/Users/foo/.local/bin/quild", true},
		{"full path dev", "/home/foo/projects/quil/quild-dev", true},
		{"trailing newline from ps", "quild\n", true},
		{"unrelated process", "bash", false},
		{"tui binary not daemon", "quil", false},
		{"prefix without separator", "quilded", false},
		{"empty", "", false},
		{"windows path", `C:\Users\foo\quild.exe`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQuildName(tt.comm); got != tt.want {
				t.Errorf("isQuildName(%q) = %v, want %v", tt.comm, got, tt.want)
			}
		})
	}
}

// TestWaitForDaemonReadyWithin_SocketReady: once a daemon is listening, the
// wait returns true promptly.
func TestWaitForDaemonReadyWithin_SocketReady(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	start := time.Now()
	if !waitForDaemonReadyWithin(sock, 0, 2*time.Second, 10*time.Millisecond) {
		t.Fatal("expected ready=true for a listening socket")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took too long to detect a ready socket: %v", elapsed)
	}
}

// TestWaitForDaemonReadyWithin_DeadPidAbortsEarly: a slow restore that never
// opens the socket AND whose process has died must be reported immediately,
// not after the full timeout — this is the crash-aware fast-fail path that
// stops a genuine daemon crash from hanging the caller for the full budget.
func TestWaitForDaemonReadyWithin_DeadPidAbortsEarly(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock") // nothing listening

	const deadPID = 0x7fffffff // no such process in a test container
	if alive, _ := processProbe(deadPID); alive {
		t.Skipf("pid %d unexpectedly alive; cannot exercise crash path", deadPID)
	}

	start := time.Now()
	got := waitForDaemonReadyWithin(sock, deadPID, 5*time.Second, 10*time.Millisecond)
	elapsed := time.Since(start)

	if got {
		t.Fatal("expected ready=false when the daemon process is dead")
	}
	if elapsed > time.Second {
		t.Errorf("dead pid should abort fast, took %v (near the 5s budget = not aborting)", elapsed)
	}
}

// TestWaitForDaemonReadyWithin_AlivePidTimesOutNormally: a LIVE process whose
// socket never opens must still wait out the full timeout. This proves the
// crash-abort is dead-pid-specific — a regression that dropped the liveness
// check and aborted on any known pid (the "healthy-but-slow start" case the
// feature exists to tolerate) would fail here. os.Getpid() is guaranteed alive
// on both the Linux test container and Windows.
func TestWaitForDaemonReadyWithin_AlivePidTimesOutNormally(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock") // nothing listening

	timeout := 150 * time.Millisecond
	start := time.Now()
	if waitForDaemonReadyWithin(sock, os.Getpid(), timeout, 10*time.Millisecond) {
		t.Fatal("expected ready=false when the socket never opens")
	}
	if elapsed := time.Since(start); elapsed < timeout {
		t.Errorf("a live pid aborted early: %v < %v (fast-fail is not dead-pid-specific)", elapsed, timeout)
	}
}

// TestWaitForDaemonReadyWithin_TimesOut: with pid==0 (process unknown) and no
// socket, the wait polls until the deadline, then reports failure.
func TestWaitForDaemonReadyWithin_TimesOut(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock") // nothing listening

	timeout := 150 * time.Millisecond
	start := time.Now()
	if waitForDaemonReadyWithin(sock, 0, timeout, 10*time.Millisecond) {
		t.Fatal("expected ready=false when the socket never opens")
	}
	if elapsed := time.Since(start); elapsed < timeout {
		t.Errorf("returned before the timeout elapsed: %v < %v", elapsed, timeout)
	}
}
