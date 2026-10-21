//go:build !windows

package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// execSelf must really attempt the exec on Unix. A path that does not exist
// makes execve fail without replacing the test binary, and the error must not
// be ErrUnsupported — that one tells respawnSelf to fall back silently, which
// is the Windows-only wrapper shape #268 removed here.
func TestExecSelf_MissingBinary_ReturnsExecError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "quil")
	err := execSelf(missing, nil, nil)
	if err == nil {
		t.Fatal("execSelf on a missing binary returned nil")
	}
	if errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("execSelf returned ErrUnsupported on Unix: %v", err)
	}
}
