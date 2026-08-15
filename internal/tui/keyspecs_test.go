package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

func TestKeySpecsFromConfig_MapsEveryAction(t *testing.T) {
	specs := keySpecsFromConfig(config.Default().Keybindings)
	if len(specs) != len(keymap.Actions()) {
		t.Fatalf("mapped %d specs, want %d", len(specs), len(keymap.Actions()))
	}
	for _, a := range keymap.Actions() {
		if _, ok := specs[a.ID]; !ok {
			t.Errorf("action %q has no config field mapping", a.ID)
		}
	}
}

func TestKeySpecsFromConfig_MatchesRegistryDefaults(t *testing.T) {
	// The registry's Default column and config.Default() must agree, or the
	// per-action fallback restores a binding the user never had.
	specs := keySpecsFromConfig(config.Default().Keybindings)
	for _, a := range keymap.Actions() {
		if specs[a.ID] != a.Default {
			t.Errorf("action %q: config default %q != registry Default %q",
				a.ID, specs[a.ID], a.Default)
		}
	}
}

func TestBuildKeymap_DefaultsAreConflictFree(t *testing.T) {
	km, conflicts := buildKeymap(config.Default().Keybindings)
	if km == nil {
		t.Fatal("buildKeymap returned nil")
	}
	if len(conflicts) != 0 {
		t.Errorf("shipped defaults have conflicts: %+v", conflicts)
	}
}

func TestBuildKeymap_DefaultsResolveToLegacyActions(t *testing.T) {
	km, _ := buildKeymap(config.Default().Keybindings)
	cases := []struct {
		key  string
		tier keymap.Tier
		want keymap.ActionID
	}{
		{"ctrl+q", keymap.TierLate, "app.quit"},
		{"ctrl+t", keymap.TierLate, "tab.new"},
		{"ctrl+w", keymap.TierLate, "pane.close"},
		{"alt+w", keymap.TierLate, "tab.close"},
		{"alt+m", keymap.TierEarly, "pane.mute"},
		{"alt+n", keymap.TierEarly, "notification.toggle"},
		{"alt+p", keymap.TierEarly, "project.picker"},
		{"alt+shift+h", keymap.TierLate, "pane.split_h"},
		{"alt+f2", keymap.TierLate, "pane.rename"},
		{"alt+shift+r", keymap.TierLate, "pane.rename"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got, ok := km.MatchTier(c.tier, c.key)
			if !ok || got != c.want {
				t.Errorf("= (%q,%v), want (%q,true)", got, ok, c.want)
			}
		})
	}
}

func TestBuildKeymap_MalformedFieldKeepsOtherBindings(t *testing.T) {
	kb := config.Default().Keybindings
	kb.Quit = "ctrl+" // malformed
	km, conflicts := buildKeymap(kb)
	if len(conflicts) == 0 {
		t.Error("malformed spec produced no conflict")
	}
	if id, ok := km.MatchTier(keymap.TierLate, "ctrl+q"); !ok || id != "app.quit" {
		t.Error("app.quit did not fall back to its default")
	}
	if id, ok := km.MatchTier(keymap.TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Error("an unrelated binding was discarded")
	}
}

func TestIsAction_SearchesBothTiersAndIsNilSafe(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.initKeymap()
	if !m.isAction("alt+m", "pane.mute") {
		t.Error("isAction missed an early action")
	}
	if !m.isAction("ctrl+w", "pane.close") {
		t.Error("isAction missed a late action")
	}
	if m.isAction("ctrl+x", "pane.close") {
		t.Error("isAction matched an unrelated key")
	}
	var nilM Model
	if nilM.isAction("ctrl+w", "pane.close") {
		t.Error("isAction on a keymap-less Model reported a match")
	}
}
