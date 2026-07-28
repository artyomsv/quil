package main

import (
	"io"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestProxyStdio_CopiesBothDirections covers the pure copy loop without
// touching the daemon-ensure path or os.Stdin/os.Stdout.
func TestProxyStdio_CopiesBothDirections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket semantics; covered on CI Linux")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	// Echo server standing in for the daemon.
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()

	daemon, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	inR, inW := net.Pipe()   // stands in for os.Stdin
	outR, outW := net.Pipe() // stands in for os.Stdout
	t.Cleanup(func() { inW.Close(); outR.Close() })

	go proxyStdio(daemon, inR, outW)

	go func() { inW.Write([]byte("round-trip")) }()

	buf := make([]byte, 10)
	outR.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(outR, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != "round-trip" {
		t.Errorf("got %q, want %q", buf, "round-trip")
	}
}

// TestProxyStdio_ReturnsWhenDaemonCloses proves the proxy exits rather than
// hanging when the far end goes away, so ssh tears the session down.
func TestProxyStdio_ReturnsWhenDaemonCloses(t *testing.T) {
	daemonA, daemonB := net.Pipe()
	inR, inW := net.Pipe()
	outR, outW := net.Pipe()
	t.Cleanup(func() { inW.Close(); outR.Close(); daemonA.Close() })

	done := make(chan struct{})
	go func() { proxyStdio(daemonA, inR, outW); close(done) }()

	daemonB.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("proxyStdio did not return after the daemon closed")
	}
}
