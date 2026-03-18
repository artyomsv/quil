package tui

import (
	"encoding/json"
	"os"
	"strings"
)

// SavedInstance is a user-created instance of a plugin (e.g., an SSH connection).
type SavedInstance struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Fields      map[string]string `json:"fields"`
	Description string            `json:"description,omitempty"`
}

// InstanceStore holds saved instances keyed by plugin name.
type InstanceStore map[string][]SavedInstance

// LoadInstances reads the instance store from a JSON file.
// Returns an empty store if the file doesn't exist.
func LoadInstances(path string) InstanceStore {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(InstanceStore)
	}
	var store InstanceStore
	if err := json.Unmarshal(data, &store); err != nil {
		return make(InstanceStore)
	}
	if store == nil {
		return make(InstanceStore)
	}
	return store
}

// SaveInstances writes the instance store to a JSON file atomically.
func SaveInstances(path string, store InstanceStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
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
func (si SavedInstance) DisplayAddr() string {
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
