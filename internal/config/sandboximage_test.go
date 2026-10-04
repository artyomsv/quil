package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Keyed like RecentCWDsPath: the local name is stable, two destinations never
// share a file, and a destination never reaches the filename as a traversal.
func TestSandboxImagePath_KeyedLikeRecentCWDs(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if got := filepath.Base(SandboxImagePath("")); got != "sandbox-image.json" {
		t.Errorf("local = %q, want sandbox-image.json", got)
	}
	if SandboxImagePath("a/b") == SandboxImagePath("a-b") {
		t.Error("two destinations share one file")
	}
	if strings.Contains(filepath.Base(SandboxImagePath("../../x")), "..") {
		t.Error("a traversal marker survived into the filename")
	}
	if filepath.Dir(SandboxImagePath("user@host")) != QuilDir() {
		t.Error("a remote's file is not beside the local one in QUIL_HOME")
	}
}
