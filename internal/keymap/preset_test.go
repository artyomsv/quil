package keymap

import "testing"

func TestDefaultLayer_CoversEveryAction(t *testing.T) {
	got := DefaultLayer()
	for _, a := range Actions() {
		spec, ok := got[a.ID]
		if !ok {
			t.Errorf("DefaultLayer is missing %q", a.ID)
			continue
		}
		if spec != a.Default {
			t.Errorf("DefaultLayer[%s] = %q, want the registered default %q", a.ID, spec, a.Default)
		}
	}
}

func TestLoadPreset_DefaultIsTheRegistry(t *testing.T) {
	p, err := LoadPreset(DefaultPresetName)
	if err != nil {
		t.Fatalf("LoadPreset(default): %v", err)
	}
	if len(p.Bindings) != len(Actions()) {
		t.Errorf("default preset has %d bindings, want %d", len(p.Bindings), len(Actions()))
	}
}

func TestLoadPreset_UnknownNameErrors(t *testing.T) {
	if _, err := LoadPreset("no-such-preset"); err == nil {
		t.Error("an unknown preset name must error, not return an empty keymap")
	}
}

func TestPresetNames_IncludesDefaultAndTmux(t *testing.T) {
	names := PresetNames()
	want := map[string]bool{"default": false, "tmux": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("PresetNames() = %v, must include %q", names, n)
		}
	}
}

// The preset carries its own prefix, and that is the difference between
// `preset = "tmux"` working and doing nothing: every tmux binding is written
// as "${prefix} x", so a loader that keeps only Bindings expands them all
// against an empty prefix and drops every one.
func TestLoadPreset_TmuxCarriesItsPrefix(t *testing.T) {
	p, err := LoadPreset("tmux")
	if err != nil {
		t.Fatalf("LoadPreset(tmux): %v", err)
	}
	if p.Prefix != "ctrl+b" {
		t.Errorf("tmux preset Prefix = %q, want ctrl+b", p.Prefix)
	}
}

// Every chord in every shipped preset must parse, after expansion. A chord
// spelled unlike the canonical form is silently dead with the conflict checker
// green, so this is the cheapest place to catch it.
func TestPresets_AllSpecsParse(t *testing.T) {
	for _, name := range PresetNames() {
		p, err := LoadPreset(name)
		if err != nil {
			t.Fatalf("LoadPreset(%s): %v", name, err)
		}
		prefix := p.Prefix
		if prefix == "" {
			prefix = "ctrl+b" // default layer has no ${prefix} to expand
		}
		expanded, conflicts := ExpandPrefix(p.Bindings, prefix)
		for _, c := range conflicts {
			t.Errorf("preset %s: %s", name, c)
		}
		for id, spec := range expanded {
			if spec == "" {
				continue
			}
			if containsPrefixVar(spec) {
				t.Errorf("preset %s, %s = %q: ${prefix} survived expansion", name, id, spec)
				continue
			}
			if _, err := ParseSpec(spec); err != nil {
				t.Errorf("preset %s, %s = %q: %v", name, id, spec, err)
			}
		}
	}
}

// Every action must resolve to a binding or an explicit "" AFTER the merge —
// not that each preset file names all 54.
func TestEveryPreset_CoversEveryActionAfterMerge(t *testing.T) {
	base := DefaultLayer()
	for _, name := range PresetNames() {
		p, err := LoadPreset(name)
		if err != nil {
			t.Fatalf("LoadPreset(%s): %v", name, err)
		}
		merged := Resolve(base, p.Bindings)
		for _, a := range Actions() {
			if _, ok := merged[a.ID]; !ok {
				t.Errorf("preset %s: %q resolves to nothing after merge", name, a.ID)
			}
		}
	}
}

// Every ID a preset names must be a real action. A typo here is silent
// otherwise: the binding lands in the map, Build reports an unknown action, and
// the tmux key the user expected simply does nothing.
func TestPresets_NameOnlyRegisteredActions(t *testing.T) {
	for _, name := range PresetNames() {
		p, _ := LoadPreset(name)
		for id := range p.Bindings {
			if _, ok := Lookup(id); !ok {
				t.Errorf("preset %s names unknown action %q", name, id)
			}
		}
	}
}

func TestPreset_TmuxBuildsWithoutStructuralConflicts(t *testing.T) {
	p, err := LoadPreset("tmux")
	if err != nil {
		t.Fatalf("LoadPreset(tmux): %v", err)
	}
	base, _ := ExpandPrefix(DefaultLayer(), p.Prefix)
	layer, conflicts := ExpandPrefix(p.Bindings, p.Prefix)
	if len(conflicts) != 0 {
		t.Fatalf("tmux preset failed to expand: %v", conflicts)
	}
	_, conflicts = BuildLayered(base, layer)
	for _, c := range conflicts {
		switch c.Kind {
		case ConflictMalformed, ConflictUnknownAction, ConflictPrefixInvalid:
			t.Errorf("tmux preset conflict: %s", c)
		}
	}
}

// tmux windows are 0-indexed, Quil tabs are 1-indexed. Binding ${prefix} 0 to
// tab 1 would double-bind it and hide tab 9 from anyone counting from zero.
func TestPreset_TmuxLeavesPrefixZeroUnbound(t *testing.T) {
	p, _ := LoadPreset("tmux")
	for id, spec := range p.Bindings {
		if spec == "${prefix} 0" {
			t.Errorf("%s binds ${prefix} 0; it must stay unbound", id)
		}
	}
}

// tmux binds rename-window to prefix-then-comma. The literal spelling is
// unparseable (a comma separates alternatives), so the preset must use the
// alias — and the result must be the real comma key.
func TestPreset_TmuxRenameIsTheCommaKey(t *testing.T) {
	p, _ := LoadPreset("tmux")
	expanded, _ := ExpandPrefix(p.Bindings, p.Prefix)
	seqs, err := ParseSpec(expanded["tab.rename"])
	if err != nil {
		t.Fatalf("tab.rename = %q: %v", expanded["tab.rename"], err)
	}
	if len(seqs) != 1 || len(seqs[0]) != 2 {
		t.Fatalf("tab.rename parsed to %v, want one two-chord sequence", seqs)
	}
	if got := seqs[0][1].Key; got != "," {
		t.Errorf("second chord = %q, want the comma key", got)
	}
}
