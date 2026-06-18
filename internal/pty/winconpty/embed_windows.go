//go:build windows

package winconpty

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed bins/conpty.dll bins/OpenConsole.exe
var bundledFS embed.FS

// bundledVersion stamps the extraction directory so a quil upgrade extracts to a
// fresh path instead of overwriting an OpenConsole.exe that a live pane may
// still hold open. Keep in sync with scripts/fetch-conpty.sh.
const bundledVersion = "1.24.260512001"

// Extract writes the embedded conpty.dll + OpenConsole.exe under
// baseDir/conpty/<version>/ (once) and points the loader at the extracted dll.
// The two files must stay co-located — conpty.dll launches OpenConsole.exe from
// its own directory.
func Extract(baseDir string) error {
	if baseDir == "" {
		return fmt.Errorf("winconpty: empty base dir")
	}
	dir := filepath.Join(baseDir, "conpty", bundledVersion)
	dll := filepath.Join(dir, "conpty.dll")
	exe := filepath.Join(dir, "OpenConsole.exe")

	// Refuse to follow symlinks at the extraction targets (redirect/TOCTOU
	// hardening, consistent with internal/persist/notes.go).
	for _, p := range []string{dir, dll, exe} {
		if err := rejectSymlink(p); err != nil {
			return err
		}
	}

	// Re-extract unless both files already match the embedded bytes (SHA256).
	// Hash (not size) so a corrupt-but-right-size file self-heals.
	if upToDate(dll, "bins/conpty.dll") && upToDate(exe, "bins/OpenConsole.exe") {
		BundledDLLPath = dll
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("winconpty: mkdir %s: %w", dir, err)
	}
	if err := extractFile("bins/conpty.dll", dll); err != nil {
		return err
	}
	if err := extractFile("bins/OpenConsole.exe", exe); err != nil {
		return err
	}
	BundledDLLPath = dll
	return nil
}

// rejectSymlink returns an error if path exists and is a symlink. A missing
// path is fine (nothing to extract over yet).
func rejectSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("winconpty: lstat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("winconpty: refusing to use symlink %s", path)
	}
	return nil
}

// upToDate reports whether dest already holds exactly the embedded file
// (SHA256-matched) — a cold-path integrity/version check that avoids
// re-extracting (and thus fighting a locked, in-use OpenConsole.exe).
func upToDate(dest, embedName string) bool {
	want, err := bundledFS.ReadFile(embedName)
	if err != nil {
		return false
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		return false
	}
	return sha256.Sum256(got) == sha256.Sum256(want)
}

func extractFile(embedName, dest string) error {
	data, err := bundledFS.ReadFile(embedName)
	if err != nil {
		return fmt.Errorf("winconpty: read embed %s: %w", embedName, err)
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("winconpty: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("winconpty: rename -> %s: %w", dest, err)
	}
	return nil
}
