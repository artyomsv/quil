package transport

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocal_DialsListeningSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket path length and semantics differ; covered on CI Linux")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()

	conn, err := Local(sock)(context.Background())
	if err != nil {
		t.Fatalf("Local dial: %v", err)
	}
	conn.Close()
}

func TestLocal_MissingSocket_ReturnsError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := Local(sock)(context.Background()); err == nil {
		t.Fatal("dial to a missing socket succeeded, want error")
	}
}
