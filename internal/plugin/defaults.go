package plugin

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

//go:embed defaults/*.toml
var defaultPlugins embed.FS

// EnsureDefaultPlugins writes embedded default plugin TOML files to dir
// if they don't already exist. Existing files whose schema_version is
// lower than the embedded default are NOT overwritten — instead they are
// returned as StalePlugin entries so the TUI can show a migration dialog.
func EnsureDefaultPlugins(dir string) ([]StalePlugin, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	entries, err := defaultPlugins.ReadDir("defaults")
	if err != nil {
		return nil, err
	}

	var stale []StalePlugin

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		embeddedData, err := defaultPlugins.ReadFile("defaults/" + e.Name())
		if err != nil {
			log.Printf("warning: read embedded plugin %s: %v", e.Name(), err)
			continue
		}

		dest := filepath.Join(dir, e.Name())
		if _, statErr := os.Stat(dest); statErr == nil {
			// File exists — check if it needs a schema upgrade.
			if needsSchemaUpgrade(dest, embeddedData) {
				userData, readErr := os.ReadFile(dest)
				if readErr != nil {
					log.Printf("warning: read stale plugin %s: %v", dest, readErr)
					continue
				}
				name := parsePluginName(embeddedData)
				if name == "" {
					name = e.Name() // fallback to filename
				}
				stale = append(stale, StalePlugin{
					Name:        name,
					FilePath:    dest,
					UserData:    userData,
					DefaultData: embeddedData,
				})
			}
			continue
		} else if !os.IsNotExist(statErr) {
			log.Printf("warning: stat %s: %v", dest, statErr)
			continue
		}

		if err := os.WriteFile(dest, embeddedData, 0600); err != nil {
			log.Printf("warning: write default plugin %s: %v", dest, err)
			continue
		}
		log.Printf("created default plugin: %s", dest)
	}
	return stale, nil
}

// needsSchemaUpgrade checks whether the user's plugin file has a lower
// schema_version than the embedded default. Returns false if the embedded
// default has no schema_version (nothing to upgrade to).
func needsSchemaUpgrade(userPath string, embeddedData []byte) bool {
	embeddedVer := ParseSchemaVersion(embeddedData)
	if embeddedVer == 0 {
		return false // embedded default has no version — no upgrade needed
	}

	userData, err := os.ReadFile(userPath)
	if err != nil {
		return false
	}
	userVer := ParseSchemaVersion(userData)
	return userVer < embeddedVer
}

// ParseSchemaVersion extracts [plugin].schema_version from raw TOML bytes.
func ParseSchemaVersion(data []byte) int {
	var partial struct {
		Plugin struct {
			SchemaVersion int `toml:"schema_version"`
		} `toml:"plugin"`
	}
	if err := toml.Unmarshal(data, &partial); err != nil {
		return 0
	}
	return partial.Plugin.SchemaVersion
}

// parsePluginName extracts [plugin].name from raw TOML bytes.
func parsePluginName(data []byte) string {
	var partial struct {
		Plugin struct {
			Name string `toml:"name"`
		} `toml:"plugin"`
	}
	if err := toml.Unmarshal(data, &partial); err != nil {
		return ""
	}
	return partial.Plugin.Name
}

