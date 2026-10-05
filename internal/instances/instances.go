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
	"path/filepath"
	"strings"
	"time"
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
//
// The temp file has a unique name: the TUI and the web gateway are separate
// processes writing this one file, and a shared "path.tmp" let one truncate
// the other's half-written copy before its rename. On Windows a rename fails
// while another process holds the target open for reading, so it is retried
// once after a short pause; the temp file is removed on every failure.
func Save(path string, s Store) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpPath, 0o600)
	}
	if werr == nil {
		if werr = renameFn(tmpPath, path); werr != nil {
			time.Sleep(renameRetryDelay)
			werr = renameFn(tmpPath, path)
		}
	}
	if werr != nil {
		os.Remove(tmpPath)
		return werr
	}
	return nil
}

// renameFn is os.Rename; a test replaces it to make the first attempt fail.
var renameFn = os.Rename

const renameRetryDelay = 50 * time.Millisecond

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
		result[i] = expandArg(arg, fields)
	}
	return result
}

// expandArg replaces each {name} token of arg in ONE pass over arg. A value is
// never expanded again: replacing field by field over a map let a value that
// contains "{other}" be substituted or not depending on map order. A token
// with no field stays as written.
func expandArg(arg string, fields map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(arg); {
		if arg[i] != '{' {
			b.WriteByte(arg[i])
			i++
			continue
		}
		end := strings.IndexByte(arg[i+1:], '}')
		if end < 0 {
			b.WriteString(arg[i:])
			break
		}
		key := arg[i+1 : i+1+end]
		if strings.IndexByte(key, '{') >= 0 {
			b.WriteByte('{')
			i++
			continue
		}
		if v, ok := fields[key]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(arg[i : i+2+end])
		}
		i += 2 + end
	}
	return b.String()
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
