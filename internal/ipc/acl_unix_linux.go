//go:build linux

package ipc

import (
	"net"
	"syscall"
)

// listenUnixPrivate binds under umask 0077, so the socket is never wider
// than 0600, not even between Listen and the chmod that follows.
//
// umask is process-wide and inherited across fork, so the narrowed window
// must not overlap a fork: a child started inside it (a warm shell, a
// sandbox helper) would keep 0077 for life and create every file 0600.
// Holding syscall.ForkLock for writing blocks every fork for the few
// syscalls the bind takes, and serialises concurrent callers so each one
// restores the umask it found rather than one left by another caller.
// This is safe on Linux only: net creates the socket with SOCK_CLOEXEC
// and never takes ForkLock itself, so the write lock cannot deadlock it.
func listenUnixPrivate(path string) (net.Listener, error) {
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}
