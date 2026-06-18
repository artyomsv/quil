//go:build windows

package winconpty

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCoordToUintptr(t *testing.T) {
	tests := []struct {
		name string
		x, y int16
		want uintptr
	}{
		{"typical", 80, 24, 0x00180050},
		{"zero", 0, 0, 0},
		{"max int16", 32767, 32767, 0x7fff7fff},
		{"negative wraps to uint16", -1, -1, 0xffffffff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coordToUintptr(windows.Coord{X: tt.x, Y: tt.y})
			if got != tt.want {
				t.Errorf("coordToUintptr(%d,%d) = %#x, want %#x", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

func TestUpToDate_MissingDest(t *testing.T) {
	if upToDate(filepath.Join(t.TempDir(), "absent.dll"), "bins/conpty.dll") {
		t.Error("missing dest should not be up to date")
	}
}

func TestUpToDate_ContentMismatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "conpty.dll")
	if err := os.WriteFile(p, []byte("not the real dll"), 0o600); err != nil {
		t.Fatal(err)
	}
	if upToDate(p, "bins/conpty.dll") {
		t.Error("content mismatch should not be up to date")
	}
}

func TestUpToDate_Match(t *testing.T) {
	want, err := bundledFS.ReadFile("bins/conpty.dll")
	if err != nil {
		t.Fatalf("read embed: %v", err)
	}
	p := filepath.Join(t.TempDir(), "conpty.dll")
	if err := os.WriteFile(p, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if !upToDate(p, "bins/conpty.dll") {
		t.Error("exact embedded copy should be up to date")
	}
}

func TestExtract_EmptyBaseDir(t *testing.T) {
	if err := Extract(""); err == nil {
		t.Error(`Extract("") should return an error`)
	}
}

func TestExtract_RejectsSymlinkTarget(t *testing.T) {
	base := t.TempDir()
	// Pre-create the version dir as a symlink; Extract must refuse it.
	verDir := filepath.Join(base, "conpty", bundledVersion)
	if err := os.MkdirAll(filepath.Dir(verDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), verDir); err != nil {
		t.Skipf("cannot create symlink (privilege?): %v", err)
	}
	if err := Extract(base); err == nil {
		t.Error("Extract should refuse a symlinked target dir")
	}
}
