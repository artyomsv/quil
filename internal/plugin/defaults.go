package plugin

import (
	"embed"
	"log"
	"os"
	"path/filepath"
)

//go:embed defaults/*.toml
var defaultPlugins embed.FS

// EnsureDefaultPlugins writes embedded default plugin TOML files to dir
// if they don't already exist. Existing files are not overwritten,
// allowing user customization to persist across upgrades.
func EnsureDefaultPlugins(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	entries, err := defaultPlugins.ReadDir("defaults")
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dest := filepath.Join(dir, e.Name())
		if _, err := os.Stat(dest); err == nil {
			continue // user file exists, don't overwrite
		} else if !os.IsNotExist(err) {
			log.Printf("warning: stat %s: %v", dest, err)
			continue
		}
		data, err := defaultPlugins.ReadFile("defaults/" + e.Name())
		if err != nil {
			log.Printf("warning: read embedded plugin %s: %v", e.Name(), err)
			continue
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			log.Printf("warning: write default plugin %s: %v", dest, err)
			continue
		}
		log.Printf("created default plugin: %s", dest)
	}
	return nil
}
