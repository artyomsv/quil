package transport

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperSlowExit keeps stdout OPEN, sleeps, then exits with a chosen code —
// the shape of Windows PowerShell as the OpenSSH DefaultShell: it takes a second
// or two to start, prints its "Unexpected token" error to stderr, and exits 1,
// all while the client's handshake has already given up. Not a test.
func TestHelperSlowExit(t *testing.T) {
	spec := os.Getenv("QUIL_HELPER_SLOW_EXIT")
	if spec == "" {
		return
	}
	ms, code, ok := strings.Cut(spec, ":")
	if !ok {
		t.Fatalf("QUIL_HELPER_SLOW_EXIT=%q, want <ms>:<code>", spec)
	}
	d, err := strconv.Atoi(ms)
	if err != nil {
		t.Fatalf("delay %q: %v", ms, err)
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		t.Fatalf("code %q: %v", code, err)
	}
	time.Sleep(time.Duration(d) * time.Millisecond)
	os.Exit(n)
}

func startSlowHelperConn(t *testing.T, delay time.Duration, exitCode int) *stdioConn {
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
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperSlowExit")
	cmd.Env = append(os.Environ(),
		"QUIL_HELPER_SLOW_EXIT="+strconv.Itoa(int(delay/time.Millisecond))+":"+strconv.Itoa(exitCode))
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	cmd.Stderr = &terminalSanitizer{w: io.Discard}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	childIn.Close()
	childOut.Close()
	c := newStdioConn(cmd, parentRead, parentWrite, "slow-helper")
	t.Cleanup(func() { c.Close() })
	return c
}

// The measured PowerShell case. Without the wait, Close would find the pipe
// still healthy, skip its own grace and kill the child — reporting the kill
// (-1 on Unix, 1 on Windows) instead of the code the far side chose. 42 is
// used rather than 1 so a Windows kill cannot pass for the real status.
func TestStdioConn_WaitExited_SlowChild_KeepsItsRealStatus(t *testing.T) {
	c := startSlowHelperConn(t, 300*time.Millisecond, 42)

	if !c.WaitExited(10 * time.Second) {
		t.Fatal("WaitExited = false for a child that exits after 300 ms")
	}
	c.Close()
	if got := c.ExitCode(); got != 42 {
		t.Errorf("ExitCode() = %d, want 42", got)
	}
}

// A hung child: the grace expires, WaitExited says so, and Close still kills
// it promptly — exactly the behaviour before WaitExited existed.
func TestStdioConn_WaitExited_HungChild_FalseAndCloseStillKills(t *testing.T) {
	c := startSlowHelperConn(t, 60*time.Second, 42)

	start := time.Now()
	if c.WaitExited(100 * time.Millisecond) {
		t.Fatal("WaitExited = true for a child that is still running")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("WaitExited took %v, want about its 100 ms grace", elapsed)
	}
	c.Close()
	if elapsed := time.Since(start); elapsed > exitGrace+5*time.Second {
		t.Errorf("Close after an expired grace took %v; it must kill, not wait", elapsed)
	}
	if got := c.ExitCode(); got == 42 {
		t.Error("ExitCode() = 42 for a child that was killed before it chose one")
	}
}

// A conn with no child has nothing to wait for.
func TestStdioConn_WaitExited_NoChild_ReturnsAtOnce(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	c := newStdioConn(nil, r, w, "no-child")
	defer c.Close()
	if !c.WaitExited(5 * time.Second) {
		t.Error("WaitExited = false for a conn with no child")
	}
	if got := c.ExitCode(); got != noExitCode {
		t.Errorf("ExitCode() = %d, want %d", got, noExitCode)
	}
}
