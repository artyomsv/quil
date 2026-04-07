//go:build linux || darwin || freebsd

package pty_test

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/pty"
)

func TestStartAndReadOutput(t *testing.T) {
	s := pty.New()
	err := s.Start("echo", "hello-quil")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	buf := make([]byte, 4096)
	var output strings.Builder
	deadline := time.After(3 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for output, got: %q", output.String())
		default:
			n, err := s.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			if strings.Contains(output.String(), "hello-quil") {
				return
			}
			if err != nil {
				if strings.Contains(output.String(), "hello-quil") {
					return
				}
				t.Fatalf("Read error: %v, output so far: %q", err, output.String())
			}
		}
	}
}

func TestResize(t *testing.T) {
	s := pty.New()
	err := s.Start("sh", "-c", "sleep 1")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Resize(40, 120); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestSetCWD(t *testing.T) {
	s := pty.New()
	s.SetCWD("/tmp")
	err := s.Start("pwd")
	if err != nil {
		t.Fatalf("Start with CWD failed: %v", err)
	}
	defer s.Close()

	buf := make([]byte, 4096)
	var output strings.Builder
	deadline := time.After(3 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for output, got: %q", output.String())
		default:
			n, err := s.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			// /tmp may resolve to /private/tmp on macOS
			out := output.String()
			if strings.Contains(out, "/tmp") {
				return
			}
			if err != nil {
				if strings.Contains(out, "/tmp") {
					return
				}
				t.Fatalf("Read error: %v, output so far: %q", err, out)
			}
		}
	}
}

func TestPid(t *testing.T) {
	s := pty.New()
	err := s.Start("sh", "-c", "sleep 1")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	pid := s.Pid()
	if pid == 0 {
		t.Error("expected non-zero PID")
	}
}

func TestWaitExit_Success_ReturnsZero(t *testing.T) {
	s := pty.New()
	if err := s.Start("true"); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	code := s.WaitExit()
	if code != 0 {
		t.Errorf("WaitExit: got %d, want 0", code)
	}
	// Second call should return same result (sync.Once)
	code2 := s.WaitExit()
	if code2 != 0 {
		t.Errorf("WaitExit second call: got %d, want 0", code2)
	}
}

func TestWaitExit_Failure_ReturnsNonZero(t *testing.T) {
	s := pty.New()
	if err := s.Start("sh", "-c", "exit 42"); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	code := s.WaitExit()
	if code != 42 {
		t.Errorf("WaitExit: got %d, want 42", code)
	}
}

func TestWaitExit_CalledFromClose_NoPanic(t *testing.T) {
	s := pty.New()
	if err := s.Start("true"); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	// WaitExit then Close — should not panic or race
	s.WaitExit()
	s.Close()
}
