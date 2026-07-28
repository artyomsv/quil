package transport

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestHelperExit is not a test. It is re-executed as a child process by
// startHelperConn and exits with the status the parent asked for, which is the
// portable way to get a process with a chosen exit code — `sh -c 'exit 127'`
// is not available on Windows.
func TestHelperExit(t *testing.T) {
	code := os.Getenv("QUIL_HELPER_EXIT")
	if code == "" {
		return
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		t.Fatalf("QUIL_HELPER_EXIT=%q is not a number", code)
	}
	os.Exit(n)
}

// startHelperConn wires a child process into a stdioConn exactly the way SSH()
// does — own the pipes as *os.File, close the child's ends after Start — so the
// reap path under test is the production one rather than a stand-in.
func startHelperConn(t *testing.T, exitCode int) *stdioConn {
	t.Helper()

	childIn, parentWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	parentRead, childOut, err := os.Pipe()
	if err != nil {
		childIn.Close()
		parentWrite.Close()
		t.Fatalf("create stdout pipe: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperExit")
	cmd.Env = append(os.Environ(), "QUIL_HELPER_EXIT="+strconv.Itoa(exitCode))
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	childIn.Close()
	childOut.Close()

	return newStdioConn(cmd, parentRead, parentWrite, "helper")
}

// drain reads until the far side's stdout closes, which is what production does
// before asking why: the read failure is the trigger for the diagnosis.
//
// Draining rather than closing straight away is what makes the exit code
// deterministic. Close kills the child, so a Close racing a still-starting
// helper would report the kill (-1 on Unix, 1 on Windows) instead of the code
// the helper chose. Stdout closes when the kernel reaps the process's
// descriptors at exit, so EOF here means the child is already gone.
func drain(t *testing.T, c *stdioConn) {
	t.Helper()
	buf := make([]byte, 256)
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
}

func TestStdioConn_ExitCode_ReportsChildStatus(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"command not found", 127},
		{"not executable", 126},
		{"ssh own failure", 255},
		{"clean exit", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := startHelperConn(t, tt.want)
			drain(t, c)
			// Close is what production reads the exit code after: it reaps the
			// child. By now the helper is a zombie, so Close's Kill is a no-op
			// on a PID that cannot yet have been reused.
			c.Close()

			if got := c.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStdioConn_ExitCode_UnknownWhileRunning(t *testing.T) {
	// A conn with no command can never be reaped, which pins the initial value
	// deterministically — a real child would race us to exit.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	c := newStdioConn(nil, r, w, "no-child")
	defer c.Close()

	if got := c.ExitCode(); got != noExitCode {
		t.Errorf("ExitCode() = %d, want %d for a conn that has not exited", got, noExitCode)
	}
}

func TestStdioConn_Close_IsIdempotentWithReap(t *testing.T) {
	// exec.Cmd.Wait must not be called twice, and both pump and Close reach it.
	// A second Close must not panic or change the recorded status.
	c := startHelperConn(t, 42)
	drain(t, c)
	c.Close()
	first := c.ExitCode()
	c.Close()

	if got := c.ExitCode(); got != first {
		t.Errorf("ExitCode() changed across a second Close: %d then %d", first, got)
	}
	if first != 42 {
		t.Errorf("ExitCode() = %d, want 42", first)
	}
}

// TestStdioConn_ExitCode_SurvivesACloseDuringNaturalExit reproduces a bug that
// 100+ unit tests missed and only a real host exposed.
//
// ssh closes its stdout when the remote command ends, but keeps running for a
// moment while it tears the session down. The pump sees EOF and the caller —
// which is watching for exactly that — calls Close immediately, landing inside
// that window. Close used to Kill unconditionally, and on Windows Kill is
// TerminateProcess(handle, 1), so the child's real status was replaced by 1.
//
// That mattered because the real status IS the diagnosis: 127 means the remote
// shell could not find quil, which is offerable, while 1 means nothing in
// particular. Every "quil is not installed over there" was reported as an
// unreachable host instead.
//
// The helper closes stdout and lingers before exiting, which is the same shape
// and fails deterministically against the old code (1 on Windows, -1 on Unix).
func TestStdioConn_ExitCode_SurvivesACloseDuringNaturalExit(t *testing.T) {
	c := startLingeringHelperConn(t, 127)

	// Drain to EOF — the exact signal that made the caller close.
	drain(t, c)
	c.Close()

	if got := c.ExitCode(); got != 127 {
		t.Errorf("ExitCode() = %d, want 127 — Close killed a child that was already exiting", got)
	}
}

// TestHelperLingeringExit closes stdout, waits, then exits with a chosen code.
func TestHelperLingeringExit(t *testing.T) {
	code := os.Getenv("QUIL_HELPER_LINGER_EXIT")
	if code == "" {
		return
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		t.Fatalf("QUIL_HELPER_LINGER_EXIT=%q is not a number", code)
	}
	os.Stdout.Close()
	time.Sleep(300 * time.Millisecond)
	os.Exit(n)
}

func startLingeringHelperConn(t *testing.T, exitCode int) *stdioConn {
	t.Helper()

	childIn, parentWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	parentRead, childOut, err := os.Pipe()
	if err != nil {
		childIn.Close()
		parentWrite.Close()
		t.Fatalf("create stdout pipe: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperLingeringExit")
	cmd.Env = append(os.Environ(), "QUIL_HELPER_LINGER_EXIT="+strconv.Itoa(exitCode))
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	// Mirrors SSH(): a non-*os.File stderr is what gives exec a copier
	// goroutine, which is half of why Wait needs bounding.
	cmd.Stderr = &terminalSanitizer{w: io.Discard}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	childIn.Close()
	childOut.Close()

	return newStdioConn(cmd, parentRead, parentWrite, "lingering-helper")
}

// A healthy child that is NOT exiting must still be killed promptly — the grace
// wait is conditional on the pump having already failed, or every TUI exit
// would stall.
func TestStdioConn_Close_KillsAHealthyChildWithoutWaiting(t *testing.T) {
	c := startLingeringHelperConn(t, 0)
	t.Cleanup(func() { c.Close() })

	start := time.Now()
	c.Close()
	if elapsed := time.Since(start); elapsed > exitGrace {
		t.Errorf("Close took %v on a live child; the grace wait must be conditional on pump failure", elapsed)
	}
}
