//go:build unix

package ipc

import (
	"fmt"
	"os"
)

// ProtectDir is a no-op on Unix: QUIL_HOME is created 0700 and
// DirAccessWarning reports a wider mode at start.
func ProtectDir(string) error { return nil }

// ProtectFile restricts an existing file to its owner.
func ProtectFile(path string) error { return os.Chmod(path, 0o600) }

// CreatePrivateFile creates path exclusively with mode 0600.
func CreatePrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}

// DirAccessWarning reports a QUIL_HOME any other account can reach.
func DirAccessWarning(dir string) (string, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("%s has mode %04o: other accounts can reach the daemon's directory (want 0700)", dir, perm), nil
	}
	return "", nil
}

// protectSocket chmods the socket 0600 and REPORTS a failure: the daemon
// stops rather than serve a socket it could not restrict.
func protectSocket(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict socket %s to its owner: %w", path, err)
	}
	return nil
}
