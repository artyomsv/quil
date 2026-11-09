//go:build !windows

package gitworktree

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO as the mark is refused at once: nothing writes to it, and a
// blocking open or read would hold the worker (and its permit) forever.
func TestReadOwnerMark_RefusesAFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := readOwnerMark(fifo); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errBadMark) {
			t.Errorf("fifo mark: err = %v, want errBadMark", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO mark blocked")
	}
}
