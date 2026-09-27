package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovePIDFileIfOwned_OtherPID_LeavesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quild.pid")
	if err := os.WriteFile(path, []byte("4242"), 0o600); err != nil {
		t.Fatal(err)
	}
	removePIDFileIfOwned(path, 1111)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("pid file naming another daemon was removed: %v", err)
	}
	if string(data) != "4242" {
		t.Errorf("pid file = %q, want it untouched", data)
	}
}

func TestRemovePIDFileIfOwned_OwnPID_RemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quild.pid")
	if err := os.WriteFile(path, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removePIDFileIfOwned(path, 4242)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("own pid file still present (stat err = %v)", err)
	}
}

func TestRemovePIDFileIfOwned_Unparseable_LeavesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quild.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	removePIDFileIfOwned(path, 4242)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("unparseable pid file was removed: %v", err)
	}
}

func TestRemovePIDFileIfOwned_Missing_NoPanic(t *testing.T) {
	removePIDFileIfOwned(filepath.Join(t.TempDir(), "quild.pid"), 4242)
}
