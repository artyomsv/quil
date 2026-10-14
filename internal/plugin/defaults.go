package plugin

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/artyomsv/quil/internal/atomicfile"
)

//go:embed defaults/*.toml
var defaultPlugins embed.FS

// EnsureDefaultPlugins writes the embedded default plugin files that are
// missing from dir, then returns the existing ones whose schema_version is
// below the default's (StaleDefaults). A stale file is never overwritten:
// the TUI and the browser offer the migration instead.
func EnsureDefaultPlugins(dir string) ([]StalePlugin, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	entries, err := defaultPlugins.ReadDir("defaults")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dest := filepath.Join(dir, e.Name())
		if _, statErr := os.Stat(dest); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			log.Printf("warning: stat %s: %v", dest, statErr)
			continue
		}
		embeddedData, err := defaultPlugins.ReadFile("defaults/" + e.Name())
		if err != nil {
			log.Printf("warning: read embedded plugin %s: %v", e.Name(), err)
			continue
		}
		if err := atomicfile.Write(dest, embeddedData); err != nil {
			log.Printf("warning: write default plugin %s: %v", dest, err)
			continue
		}
		log.Printf("created default plugin: %s", dest)
	}
	return StaleDefaults(dir)
}

// StaleDefaults lists the plugin files in dir whose schema_version is below
// the embedded default's. Read-only: it never creates or changes a file, so
// the browser can call it on every GET.
func StaleDefaults(dir string) ([]StalePlugin, error) {
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
		need := ParseSchemaVersion(embeddedData)
		if need == 0 {
			continue // the default has no version: nothing to upgrade to
		}
		dest := filepath.Join(dir, e.Name())
		userData, err := os.ReadFile(dest)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("warning: read stale plugin %s: %v", dest, err)
			}
			continue
		}
		if ParseSchemaVersion(userData) >= need {
			continue
		}
		name := parsePluginName(embeddedData)
		if name == "" {
			name = e.Name()
		}
		stale = append(stale, StalePlugin{
			Name: name, FilePath: dest, UserData: userData, DefaultData: embeddedData, NeedVersion: need,
		})
	}
	return stale, nil
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
