//go:build linux || darwin || freebsd

package pty_test

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/aethel/internal/pty"
)

func TestStartAndReadOutput(t *testing.T) {
	s := pty.New()
	err := s.Start("echo", "hello-aethel")
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
			if strings.Contains(output.String(), "hello-aethel") {
				return
			}
			if err != nil {
				if strings.Contains(output.String(), "hello-aethel") {
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
