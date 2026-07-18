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

func TestSwapBinaries_SecondSwapFails_RollsBackFirst(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)

	// Staged dir has ONLY the quil binary — quild is missing, forcing the
	// second swap to fail and the pair-rollback branch to run.
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, "windows"); err == nil {
		t.Fatal("swapPair with missing staged quild = nil error, want error")
	}

	gotQuil, err := os.ReadFile(quilTarget)
	if err != nil || string(gotQuil) != "old-quil" {
		t.Errorf("quil target after rollback = %q (err %v), want old-quil restored", gotQuil, err)
	}
	if _, err := os.Stat(quilTarget + ".old"); !os.IsNotExist(err) {
		t.Errorf("quil.old backup should be gone after rollback, stat err = %v", err)
	}
	gotQuild, err := os.ReadFile(quildTarget)
	if err != nil || string(gotQuild) != "old-quild" {
		t.Errorf("quild target = %q (err %v), want old-quild (untouched by its own inner rollback)", gotQuild, err)
	}
	if _, err := os.Stat(quildTarget + ".old"); !os.IsNotExist(err) {
		t.Errorf("quild.old backup should be gone after its own inner rollback, stat err = %v", err)
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
