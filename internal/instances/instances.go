// Package instances is the saved plugin-instance store (instances.json): a
// plugin's named argument sets, e.g. one ssh host. The TUI dialog and the web
// gateway share it; the gateway expands a page's instance ID into arguments
// from its own disk (spec 5b E7), so a page never sends raw arguments.
// Stdlib only.
package instances

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Saved is a user-created instance of a plugin (e.g., an SSH connection).
type Saved struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Fields      map[string]string `json:"fields"`
	Description string            `json:"description,omitempty"`
}

// Store holds saved instances keyed by plugin name.
type Store map[string][]Saved

// Load reads the store. A missing file is an empty store; a file that does
// not parse is an error, so a read-modify-write never replaces it.
func Load(path string) (Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s == nil {
		s = Store{}
	}
	return s, nil
}

// LoadOrEmpty is Load for readers that show a list: any failure is an empty
// store (the TUI's historical LoadInstances behaviour).
func LoadOrEmpty(path string) Store {
	s, err := Load(path)
	if err != nil {
		return Store{}
	}
	return s
}

// Save writes the store atomically (temp file + rename), mode 0600.
func Save(path string, s Store) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Expand finds plugin's saved instance with id and expands it through the
// plugin's arg template. An unknown id is an error: a plugin started without
// its instance's arguments would connect somewhere the user did not pick.
func Expand(s Store, template []string, plugin, id string) (string, []string, error) {
	for _, si := range s[plugin] {
		if si.ID == id {
			return si.Name, BuildArgs(template, si.Fields), nil
		}
	}
	return "", nil, fmt.Errorf("no saved instance %s for %s", id, plugin)
}

// BuildArgs expands {placeholder} tokens in an arg template using field values.
func BuildArgs(template []string, fields map[string]string) []string {
	if len(template) == 0 {
		return nil
	}
	result := make([]string, len(template))
	for i, arg := range template {
		expanded := arg
		for k, v := range fields {
			expanded = strings.ReplaceAll(expanded, "{"+k+"}", v)
		}
		result[i] = expanded
	}
	return result
}

// DisplayAddr formats a saved instance's fields into a short address string.
// Tries user@host:port, falls back to showing the first non-name, non-description field.
func (si Saved) DisplayAddr() string {
	user := si.Fields["user"]
	host := si.Fields["host"]
	port := si.Fields["port"]

	if host != "" {
		addr := host
		if user != "" {
			addr = user + "@" + addr
		}
		if port != "" && port != "22" {
			addr += ":" + port
		}
		return addr
	}

	// Fallback: show first meaningful field value
	for k, v := range si.Fields {
		if k != "name" && k != "description" && v != "" {
			return v
		}
	}
	return ""
}
