//go:build linux

package ipc

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
)

// The bind itself must happen under an owner-only umask: with the process
// umask at 0 and no chmod afterwards, the socket still has no group or
// other bits. Without the umask a Linux socket is created 0777.
func TestListenUnixPrivate_BindsUnderOwnerOnlyUmask(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := listenUnixPrivate(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("socket mode = %04o under umask 0, want no group/other bits", perm)
	}
}

// Concurrent callers must each restore the umask they found; interleaved
// set/restore pairs can otherwise leave the process stuck at 0077.
func TestListenUnixPrivate_ConcurrentCallersRestoreUmask(t *testing.T) {
	before := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(before) })
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ln, err := listenUnixPrivate(filepath.Join(dir, "s"+strconv.Itoa(i)))
			if err != nil {
				t.Error(err)
				return
			}
			ln.Close()
		}(i)
	}
	wg.Wait()
	if got := syscall.Umask(0o022); got != 0o022 {
		t.Fatalf("umask after concurrent binds = %04o, want 0022", got)
	}
}
