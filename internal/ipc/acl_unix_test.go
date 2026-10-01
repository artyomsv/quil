//go:build unix

package ipc

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// GUARD (ruling P-7): Start chmodded the socket 0600 before listenUnixPrivate
// existed, so this passed then too. It pins the end state through that
// rewrite; TestListenUnixPrivate_RestoresUmask,
// TestListenUnixPrivate_BindsUnderOwnerOnlyUmask and
// TestProtectSocket_ReportsChmodFailure are the tests that failed before
// listenUnixPrivate existed.
func TestServerStart_SocketIsOwnerOnly(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	s := NewServer(sock, func(*Conn, *Message) {}, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("socket mode = %04o, want no group/other bits", perm)
	}
}

func TestListenUnixPrivate_RestoresUmask(t *testing.T) {
	before := syscall.Umask(0o022)
	syscall.Umask(before)
	ln, err := listenUnixPrivate(filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	after := syscall.Umask(before)
	if after != before {
		t.Fatalf("umask after = %04o, want %04o restored", after, before)
	}
}

func TestProtectSocket_ReportsChmodFailure(t *testing.T) {
	if err := protectSocket(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a chmod failure was swallowed; the daemon must stop on it")
	}
}

func TestCreatePrivateFile_ExclusiveAnd0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	f, err := CreatePrivateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, _ := os.Stat(p)
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %04o, want 0600", perm)
	}
	if _, err := CreatePrivateFile(p); err == nil {
		t.Fatal("CreatePrivateFile overwrote an existing file")
	}
}

func TestDirAccessWarning_WideMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if w, err := DirAccessWarning(dir); err != nil || w == "" {
		t.Fatalf("0755 dir: warning=%q err=%v, want a warning", w, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if w, err := DirAccessWarning(dir); err != nil || w != "" {
		t.Fatalf("0700 dir: warning=%q err=%v, want none", w, err)
	}
}
