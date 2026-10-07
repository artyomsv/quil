//go:build unix

package clientauth

import (
	"path/filepath"
	"testing"
)

func TestSyncDir_SyncsARealDirectory(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir = %v", err)
	}
	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncDir of a missing directory reported success")
	}
}
