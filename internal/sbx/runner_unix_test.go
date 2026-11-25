//go:build !windows

package sbx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processRunning reports whether pid is a process that has not exited. A
// killed process stays a zombie until its parent reaps it, and kill(pid, 0)
// still succeeds on one. scripts/dev.sh runs go test in a container without
// --init, where PID 1 is the go tool and never reaps an orphan, so on Linux
// the state is read from /proc and a zombie counts as gone.
func processRunning(pid int) bool {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	stat := string(b)
	i := strings.LastIndexByte(stat, ')') // the command name may hold spaces
	if i < 0 || i+2 >= len(stat) {
		return true
	}
	switch stat[i+2] {
	case 'Z', 'X', 'x':
		return false
	}
	return true
}

// gone waits up to 2s for pid to exit: a kill is not instant.
func gone(pid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func readChildPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("child pid %q: %v", b, err)
	}
	return pid
}

// A command that starts a child holding its output (stdout here) and then
// outlives its context: the child keeps the output pipe open, and Run used
// to wait for it far past the deadline.
const holdsOutput = "sleep 5 & echo $! >\"$1\"; wait"

func TestExecRunner_CancelReturnsPromptlyWhileAChildHoldsTheOutput(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := ExecRunner{}.Run(ctx, "", &bytes.Buffer{}, "sh", "-c", holdsOutput, "sh", pidFile)
	if err == nil {
		t.Fatal("a cancelled command reported success")
	}
	// Bounded by runWaitDelay even where the child is not stopped with it.
	if d := time.Since(start); d > 200*time.Millisecond+runWaitDelay+time.Second {
		t.Errorf("returned after %s; the deadline was 200ms and runWaitDelay %s", d, runWaitDelay)
	}
	t.Cleanup(func() { _ = syscall.Kill(readChildPID(t, pidFile), syscall.SIGKILL) })
}

func TestExecRunner_CancelStopsWhatTheCommandStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = ExecRunner{}.Run(ctx, "", &bytes.Buffer{}, "sh", "-c", holdsOutput, "sh", pidFile) // cancelled by design
	pid := readChildPID(t, pidFile)
	if !gone(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL) // not to leak it past the test
		t.Errorf("the command's child %d outlived the cancelled step", pid)
	}
}

// A command that succeeds is not failed for a child it left running that
// still holds the output: Run stops reading and succeeds, and w holds exactly
// what the command wrote (callers parse it). The child is the command's
// business and is left alone.
func TestExecRunner_SuccessWithAChildStillHoldingTheOutput(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	var out bytes.Buffer
	start := time.Now()
	err := ExecRunner{}.Run(context.Background(), "", &out, "sh", "-c", "sleep 30 & echo $! >\"$1\"; echo done", "sh", pidFile)
	pid := readChildPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err != nil {
		t.Fatalf("err = %v, want success: the command exited 0", err)
	}
	if d := time.Since(start); d > runWaitDelay+time.Second {
		t.Errorf("returned after %s, want about runWaitDelay (%s)", d, runWaitDelay)
	}
	if got := out.String(); got != "done\n" {
		t.Errorf("output %q, want exactly what the command wrote", got)
	}
	if !processRunning(pid) {
		t.Error("the child of a command that succeeded was stopped")
	}
}

// A probe whose sbx leaves a child holding stdout still gets an answer it can
// parse: Run adds nothing to what it captured. Driven through the real
// ListSandboxes, whose JSON a trailing note would have broken.
func TestListSandboxes_ParsesWhenSbxLeavesAChildHoldingTheOutput(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child")
	bin := filepath.Join(dir, "sbx")
	script := "#!/bin/sh\nsleep 30 & echo $! >'" + pidFile + "'\nprintf '[{\"name\":\"quil-a\",\"status\":\"running\"}]\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Client{Bin: bin, Runner: ExecRunner{}}
	got, err := c.ListSandboxes(context.Background())
	t.Cleanup(func() { _ = syscall.Kill(readChildPID(t, pidFile), syscall.SIGKILL) })
	if err != nil || len(got) != 1 || got[0].Name != "quil-a" {
		t.Fatalf("ListSandboxes = %+v, %v; want the one sandbox", got, err)
	}
}
