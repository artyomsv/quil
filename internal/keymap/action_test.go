package keymap

import "testing"

func TestActions_RegistryIntegrity(t *testing.T) {
	acts := Actions()
	if len(acts) != 41 {
		t.Fatalf("registry has %d actions, want 41 (one per KeybindingsConfig field)", len(acts))
	}
	seen := make(map[ActionID]bool, len(acts))
	orders := make(map[int]ActionID, len(acts))
	for _, a := range acts {
		if a.ID == "" || a.Label == "" || a.Group == "" {
			t.Errorf("action %+v has an empty ID, Label or Group", a)
		}
		if seen[a.ID] {
			t.Errorf("duplicate action ID %q", a.ID)
		}
		seen[a.ID] = true
		if prev, dup := orders[a.Order]; dup {
			t.Errorf("actions %q and %q share Order %d — the duplicate-conflict tie-break would be nondeterministic", prev, a.ID, a.Order)
		}
		orders[a.Order] = a.ID
		if _, err := ParseSpec(a.Default); err != nil {
			t.Errorf("action %q has an unparseable Default %q: %v", a.ID, a.Default, err)
		}
	}
}

func TestActions_TierSplitMatchesLegacySwitches(t *testing.T) {
	// Pinned against handleKey before the rewrite: the early switch
	// (model.go:3410-3525) ran before tryPluginRawKey (3562), the late switch
	// (3581-3734) after. Moving an action between tiers changes whether a
	// plugin's raw_keys entry beats it.
	early := map[ActionID]bool{
		"notification.toggle": true, "notification.focus": true,
		"sidebar.toggle": true, "pane.go_back": true, "pane.mute": true,
		"pane.toggle_eager": true, "pane.toggle_wrap": true,
		"pane.toggle_lazygit": true, "pane.command_history": true,
		"pane.quick_actions": true, "project.new": true,
		"project.destroy": true, "project.picker": true,
		"project.next": true, "project.prev": true,
		"project.toggle": true, "project.attention_queue": true,
	}
	if len(early) != 17 {
		t.Fatalf("expected-early table has %d entries, want 17", len(early))
	}
	for _, a := range Actions() {
		want := TierLate
		if early[a.ID] {
			want = TierEarly
		}
		if a.Tier != want {
			t.Errorf("action %q tier = %v, want %v", a.ID, a.Tier, want)
		}
	}
}

func TestActionsByGroup_IsDeterministic(t *testing.T) {
	groups, byGroup := ActionsByGroup()
	if len(groups) == 0 {
		t.Fatal("no groups")
	}
	var total int
	for _, g := range groups {
		bucket := byGroup[g]
		total += len(bucket)
		for i := 1; i < len(bucket); i++ {
			if bucket[i-1].Order > bucket[i].Order {
				t.Errorf("group %q is not sorted by Order", g)
			}
		}
	}
	if total != len(Actions()) {
		t.Errorf("grouping covers %d actions, want %d", total, len(Actions()))
	}
}

func TestLookup(t *testing.T) {
	a, ok := Lookup("pane.split_h")
	if !ok || a.Tier != TierLate {
		t.Errorf("Lookup(pane.split_h) = (%+v, %v)", a, ok)
	}
	if _, ok := Lookup("nope.nope"); ok {
		t.Error("Lookup(nope.nope) found something")
	}
}
