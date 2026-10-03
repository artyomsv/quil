package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The remembered sandbox image, per destination.
//
// Ctrl+N pre-fills the image field from the last image used on the dialog's
// destination, else [sandbox] default_image. Kept in its own small file per
// destination (config.SandboxImagePath) rather than in config.toml, because
// config.Save rewrites the whole file — and its comments — and this one is
// written on every sandbox create whose image changed.

// sandboxImageFile is the on-disk shape. An object rather than a bare string
// so a later field does not need a migration.
type sandboxImageFile struct {
	Image string `json:"image"`
}

// LoadSandboxImage reads the remembered image, or "" for a missing, linked,
// malformed or oversized file — a bad file costs a pre-fill, never the dialog.
func LoadSandboxImage(path string) string {
	// Reject a symlink, matching LoadRecentCWDs.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f sandboxImageFile
	if json.Unmarshal(data, &f) != nil || len([]rune(f.Image)) > sandboxImageMax {
		return ""
	}
	return f.Image
}

// SaveSandboxImage writes it atomically (.tmp + rename), as SaveRecentCWDs
// does.
func SaveSandboxImage(path, image string) error {
	data, err := json.Marshal(sandboxImageFile{Image: image})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sandboxImageStore is how the Model reaches the disk. Nil functions mean "no
// memory", which is what every Model built directly in a test gets — so no
// test writes under the real ~/.quil (the SetRecentCWDs precedent).
type sandboxImageStore struct {
	load func(dest string) string
	save func(dest, image string)
}

// SetSandboxImageStore installs the store. cmd/quil/main.go wires the
// file-backed one.
func (m *Model) SetSandboxImageStore(load func(string) string, save func(string, string)) {
	m.sandboxImages = sandboxImageStore{load: load, save: save}
}

// sandboxImageDefault is what the image field pre-fills with for dest.
func (m Model) sandboxImageDefault(dest string) string {
	if m.sandboxImages.load != nil {
		if img := m.sandboxImages.load(dest); img != "" {
			return img
		}
	}
	return m.cfg.Sandbox.DefaultImage
}

// rememberSandboxImage files a submitted image under dest, when it changed.
func (m Model) rememberSandboxImage(dest, image string) {
	if m.sandboxImages.save == nil || image == "" {
		return
	}
	if m.sandboxImages.load != nil && m.sandboxImages.load(dest) == image {
		return
	}
	m.sandboxImages.save(dest, image)
}
