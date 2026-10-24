//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apty "github.com/artyomsv/quil/internal/pty"
)

// TestFixLaunchArgv_RunsBatchShim launches a real temp .cmd through the same
// PTY session a fix child gets, with the argv fixLaunchArgv produces, and
// reads its echo: CreateProcess cannot start a batch file itself, so without
// the cmd.exe wrap Start fails and the pane shows nothing. Windows host
// only — ConPTY is the thing under test.
func TestFixLaunchArgv_RunsBatchShim(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-fix.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho FIXCMD %1\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv, err := fixLaunchArgv([]string{script, "hello"}, "windows", os.Getenv)
	if err != nil {
		t.Fatalf("fixLaunchArgv: %v", err)
	}
	if !strings.EqualFold(filepath.Base(argv[0]), "cmd.exe") {
		t.Fatalf("argv[0] = %q, want cmd.exe", argv[0])
	}

	s := apty.NewWithSize(80, 24)
	s.SetCWD(dir)
	if err := s.Start(argv[0], argv[1:]...); err != nil {
		t.Fatalf("Start %q: %v", argv, err)
	}
	defer s.Close()

	// Read until the echo arrives or the deadline passes; Read blocks, so it
	// runs on its own goroutine.
	out := make(chan string, 1)
	go func() {
		var seen strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			seen.Write(buf[:n])
			if strings.Contains(seen.String(), "FIXCMD hello") || err != nil {
				out <- seen.String()
				return
			}
		}
	}()
	select {
	case got := <-out:
		if !strings.Contains(got, "FIXCMD hello") {
			t.Fatalf("batch output = %q, want the echo", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the batch script's echo never arrived")
	}
}
