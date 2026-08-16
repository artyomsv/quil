package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

// promotedActions have no [keybindings] field by design: they were promoted out
// of handleKey's reserved-key switch, which never had config fields, and adding
// twelve to a table the whole feature is migrating away from would be work
// aimed backwards. They get their bindings from keymap.DefaultLayer instead.
//
// Listed explicitly so promoting another action is a deliberate edit here
// rather than a silently shrinking assertion.
var promotedActions = map[keymap.ActionID]bool{
	"tab.next": true, "tab.prev": true, "system.shortcuts": true,
	"tab.switch_1": true, "tab.switch_2": true, "tab.switch_3": true,
	"tab.switch_4": true, "tab.switch_5": true, "tab.switch_6": true,
	"tab.switch_7": true, "tab.switch_8": true, "tab.switch_9": true,
}

func TestKeySpecsFromConfig_MapsEveryConfigBackedAction(t *testing.T) {
	specs := keySpecsFromConfig(config.Default().Keybindings)
	var want int
	for _, a := range keymap.Actions() {
		if promotedActions[a.ID] {
			if _, ok := specs[a.ID]; ok {
				t.Errorf("action %q is promoted and must not be mapped from config", a.ID)
			}
			continue
		}
		want++
		if _, ok := specs[a.ID]; !ok {
			t.Errorf("action %q has no config field mapping", a.ID)
		}
	}
	if len(specs) != want {
		t.Fatalf("mapped %d specs, want %d config-backed actions", len(specs), want)
	}
}

func TestKeySpecsFromConfig_MatchesRegistryDefaults(t *testing.T) {
	// The registry's Default column and config.Default() must agree, or the
	// per-action fallback restores a binding the user never had.
	specs := keySpecsFromConfig(config.Default().Keybindings)
	for _, a := range keymap.Actions() {
		if promotedActions[a.ID] {
			continue
		}
		if specs[a.ID] != a.Default {
			t.Errorf("action %q: config default %q != registry Default %q",
				a.ID, specs[a.ID], a.Default)
		}
	}
}

// The promoted actions have no config field, so the ONLY thing binding them is
// the default layer underneath the config layer. Without it Alt+1..9 dispatch
// nothing — which is exactly what deleting their reserved-key case would cause.
func TestBuildKeymap_PromotedActionsKeepTheirDefaults(t *testing.T) {
	km, _ := buildKeymap(config.Default().Keybindings)
	for _, tc := range []struct{ key, id string }{
		{"alt+1", "tab.switch_1"},
		{"alt+9", "tab.switch_9"},
	} {
		got, ok := km.MatchTier(keymap.TierLate, tc.key)
		if !ok || string(got) != tc.id {
			t.Errorf("MatchTier(late, %q) = (%q, %v), want %q", tc.key, got, ok, tc.id)
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
