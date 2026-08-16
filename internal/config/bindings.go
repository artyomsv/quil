package config

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/artyomsv/quil/internal/keymap"
)

// Bindings is the parsed $QUIL_HOME/bindings.toml.
//
// It lives in this package rather than in internal/keymap because it needs
// QuilDir(). Keeping paths out of keymap is what lets that package be tested
// without QUIL_HOME and without building a Model.
type Bindings struct {
	Preset          string
	Prefix          string
	SequenceTimeout time.Duration
	Overrides       map[keymap.ActionID]string
}

// bindingsFile is the wire shape, shared with the shipped presets so a user can
// rename their bindings.toml into presets/ and select it by name.
//
// SequenceTimeout is a string so "0" can mean off and "500ms" can parse through
// time.ParseDuration. A numeric field would make "off" and "immediately"
// indistinguishable from the zero value.
type bindingsFile struct {
	Preset          string            `toml:"preset"`
	Prefix          string            `toml:"prefix"`
	SequenceTimeout string            `toml:"sequence_timeout"`
	Bindings        map[string]string `toml:"bindings"`
}

// BindingsPath is where the user's keymap lives. Derived from QuilDir(), never
// a literal ~/.quil — dev-mode isolation depends on it.
func BindingsPath() string { return filepath.Join(QuilDir(), "bindings.toml") }

// LoadBindings reads bindings.toml. A missing file is the normal first-launch
// state and yields the default preset with no overrides.
func LoadBindings() (Bindings, error) {
	out := Bindings{Preset: keymap.DefaultPresetName, Overrides: map[keymap.ActionID]string{}}

	data, err := os.ReadFile(BindingsPath())
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read bindings: %w", err)
	}
	var bf bindingsFile
	if err := toml.Unmarshal(data, &bf); err != nil {
		return out, fmt.Errorf("parse bindings: %w", err)
	}
	if bf.Preset != "" {
		out.Preset = bf.Preset
	}
	out.Prefix = bf.Prefix
	if bf.SequenceTimeout != "" && bf.SequenceTimeout != "0" {
		d, err := time.ParseDuration(bf.SequenceTimeout)
		if err != nil {
			return out, fmt.Errorf("parse sequence_timeout %q: %w", bf.SequenceTimeout, err)
		}
		out.SequenceTimeout = d
	}
	for id, spec := range bf.Bindings {
		out.Overrides[keymap.ActionID(id)] = spec
	}
	return out, nil
}

// WriteBindings writes bindings.toml atomically: temp file then rename, the
// same crash-safety pattern Save uses.
func WriteBindings(b Bindings) error {
	data, err := encodeBindings(b)
	if err != nil {
		return err
	}
	path := BindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write bindings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename bindings: %w", err)
	}
	return nil
}

// WriteBindingsExclusive creates bindings.toml only if it does not exist.
//
// O_EXCL rather than a Stat-then-Write, because two TUI clients can attach
// concurrently and a check-then-act would let the second clobber the first.
func WriteBindingsExclusive(b Bindings) error {
	data, err := encodeBindings(b)
	if err != nil {
		return err
	}
	path := BindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err // callers distinguish os.IsExist
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write bindings: %w", err)
	}
	return nil
}

func encodeBindings(b Bindings) ([]byte, error) {
	bf := bindingsFile{
		Preset:          b.Preset,
		Prefix:          b.Prefix,
		SequenceTimeout: "0",
		Bindings:        map[string]string{},
	}
	if b.SequenceTimeout > 0 {
		bf.SequenceTimeout = b.SequenceTimeout.String()
	}
	for id, spec := range b.Overrides {
		bf.Bindings[string(id)] = spec
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(bf); err != nil {
		return nil, fmt.Errorf("encode bindings: %w", err)
	}
	return buf.Bytes(), nil
}

// MigrateBindings creates bindings.toml from a legacy [keybindings] table on
// first launch. Reports whether it wrote anything.
//
// TUI-ONLY. Keybindings are read exclusively by internal/tui and this package;
// the daemon and the MCP bridge never touch them. The quick_actions comment in
// config.go documents why that matters: a startup write from every process that
// loads config would race the same file.
//
// cfg must be the Config that Load already returned, so the in-memory legacy
// patches — notably the quick_actions ctrl+a -> alt+a rewrite — have already
// run. Taking a Config rather than a path is what guarantees that ordering;
// migrating first would faithfully preserve a binding the project killed.
//
// loadErr is Load's error. A malformed config.toml must NOT migrate: it would
// produce pure defaults and permanently discard the user's customizations, and
// the migration is one-way. A MISSING config.toml is not that case — it is an
// ordinary first launch, and refusing it would leave the user with no
// bindings.toml on every subsequent launch, forever.
func MigrateBindings(cfg Config, loadErr error) (bool, error) {
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return false, fmt.Errorf("refusing to migrate bindings from an unreadable config: %w", loadErr)
	}
	if _, err := os.Stat(BindingsPath()); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat bindings: %w", err)
	}

	// Diff against the shipped defaults. Copying every field instead would
	// produce 42 overrides for an untouched install and pin that user to
	// today's defaults permanently — the exact trap this move exists to escape,
	// since config.Save rewrites the whole struct on any unrelated edit.
	shipped := KeySpecsFromConfig(Default().Keybindings)
	current := KeySpecsFromConfig(cfg.Keybindings)
	overrides := map[keymap.ActionID]string{}
	for id, spec := range current {
		if shipped[id] != spec {
			overrides[id] = spec
			log.Printf("bindings: adopting override %s = %q", id, spec)
		}
	}

	err := WriteBindingsExclusive(Bindings{Preset: keymap.DefaultPresetName, Overrides: overrides})
	if os.IsExist(err) {
		// A concurrent client won the race. Success for our purposes: the file
		// exists and holds the same diff.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
