package keymap

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed presets/*.toml
var presetFS embed.FS

// Preset is one shipped keymap.
//
// Prefix is carried rather than discarded, and that is the difference between
// `preset = "tmux"` working and doing nothing: the tmux preset writes every
// binding as "${prefix} x", so a caller that keeps only Bindings expands them
// against an empty prefix and drops all of them.
type Preset struct {
	Name     string
	Prefix   string
	Timeout  string
	Bindings map[ActionID]string
}

// presetFile is the on-disk shape, shared with the user's bindings.toml so the
// two are interchangeable.
type presetFile struct {
	Preset          string            `toml:"preset"`
	Prefix          string            `toml:"prefix"`
	SequenceTimeout string            `toml:"sequence_timeout"`
	Bindings        map[string]string `toml:"bindings"`
}

// DefaultPresetName is the base layer every other layer stacks on.
const DefaultPresetName = "default"

// DefaultLayer is the shipped keymap: every action's registered Default.
//
// Deliberately NOT a presets/default.toml. The registry already carries each
// action's shipped spec, and Build already falls back to it for a malformed
// binding — a file would be a second copy of the same data, free to drift from
// the one dispatch actually uses, and silently rebinding every existing user's
// keyboard when it did.
func DefaultLayer() map[ActionID]string {
	out := make(map[ActionID]string, len(registry))
	for _, a := range registry {
		out[a.ID] = a.Default
	}
	return out
}

// LoadPreset returns a shipped preset by name.
func LoadPreset(name string) (Preset, error) {
	if name == DefaultPresetName {
		return Preset{Name: name, Bindings: DefaultLayer()}, nil
	}
	data, err := presetFS.ReadFile("presets/" + name + ".toml")
	if err != nil {
		return Preset{}, fmt.Errorf("unknown preset %q", name)
	}
	var pf presetFile
	if err := toml.Unmarshal(data, &pf); err != nil {
		return Preset{}, fmt.Errorf("preset %q: %w", name, err)
	}
	out := Preset{
		Name:     name,
		Prefix:   pf.Prefix,
		Timeout:  pf.SequenceTimeout,
		Bindings: make(map[ActionID]string, len(pf.Bindings)),
	}
	for id, spec := range pf.Bindings {
		out.Bindings[ActionID(id)] = spec
	}
	return out, nil
}

// PresetNames lists every selectable preset, sorted. "default" is included
// even though it has no file — it is the registry layer.
func PresetNames() []string {
	out := []string{DefaultPresetName}
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return out
	}
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(path.Base(e.Name()), ".toml"))
	}
	sort.Strings(out)
	return out
}

// containsPrefixVar reports whether a spec still holds an unexpanded ${prefix}.
func containsPrefixVar(spec string) bool { return strings.Contains(spec, prefixVar) }
