package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSwapOne_ReplacesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	staged := filepath.Join(dir, "staged-quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)

	if err := swapOne(target, staged); err != nil {
		t.Fatalf("swapOne: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new-binary" {
		t.Errorf("target content = %q, want new-binary", got)
	}
	backup, _ := os.ReadFile(target + ".old")
	if string(backup) != "old-binary" {
		t.Errorf("backup content = %q, want old-binary", backup)
	}
}

func TestSwapOne_MissingStaged_RollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)

	if err := swapOne(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("swapOne with missing staged = nil error, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored", got, err)
	}
}

func TestUpdateRestartPreapproved(t *testing.T) {
	t.Setenv("QUIL_UPDATE_RESTART", "")
	if updateRestartPreapproved() {
		t.Error("preapproved with empty env, want false")
	}
	t.Setenv("QUIL_UPDATE_RESTART", "1")
	if !updateRestartPreapproved() {
		t.Error("not preapproved with env=1, want true")
	}
}
