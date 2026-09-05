package keymap

import "testing"

func TestActions_RegistryIntegrity(t *testing.T) {
	acts := Actions()
	if len(acts) != 58 {
		t.Fatalf("registry has %d actions, want 58 (42 config-backed + 12 promoted from the reserved-key switch + 4 reorder)", len(acts))
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
	// Pinned against handleKey before the rewrite: its first switch ran before
	// tryPluginRawKey and its second after, and each action kept the side it
	// was on. Moving one between tiers changes whether a plugin's raw_keys
	// entry beats it. Cited by symbol, never by line: the two switches have
	// moved twice already, and a stale line number reads as a fact.
	//
	// pane.toggle_hunk has no pre-rewrite position — it arrived on master
	// after this branch was cut. It is pinned against where MASTER put its
	// kbMatches arm instead: ahead of tryPluginRawKey, between toggle_lazygit
	// and command_history, which is the early switch. The two overlay toggles
	// share one slot per tab, so splitting them across the seam would let a
	// plugin's raw_keys claim one of the pair and not the other.
	early := map[ActionID]bool{
		"notification.toggle": true, "notification.focus": true,
		"sidebar.toggle": true, "pane.go_back": true, "pane.mute": true,
		"pane.toggle_eager": true, "pane.toggle_wrap": true,
		"pane.toggle_lazygit": true, "pane.toggle_hunk": true,
		"pane.command_history": true,
		"pane.quick_actions":   true, "project.new": true,
		"project.destroy": true, "project.picker": true,
		"project.next": true, "project.prev": true,
		"project.toggle": true, "project.attention_queue": true,
		"project.move_up": true, "project.move_down": true,
	}
	if len(early) != 20 {
		t.Fatalf("expected-early table has %d entries, want 20", len(early))
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

// The four reorder actions ship BOUND, unlike tab.next/tab.prev: the user asked
// for keyboard reordering, and every chord here was free. Projects are a
// vertical list, so they move with Up/Down; tabs are a horizontal strip, and
// PgUp/PgDn is the browser convention for moving one. Alt+Shift keeps them on
// the same layer as the other project keys and off anything an AI tool binds.
func TestReorderActionsShipBound(t *testing.T) {
	want := []struct {
		id    ActionID
		def   string
		tier  Tier
		group string
	}{
		{"tab.move_left", "alt+shift+pgup", TierLate, "Tabs"},
		{"tab.move_right", "alt+shift+pgdown", TierLate, "Tabs"},
		{"project.move_up", "alt+shift+up", TierEarly, "Projects"},
		{"project.move_down", "alt+shift+down", TierEarly, "Projects"},
	}
	for _, w := range want {
		a, ok := Lookup(w.id)
		if !ok {
			t.Errorf("action %q is not registered", w.id)
			continue
		}
		if a.Default != w.def || a.Tier != w.tier || a.Group != w.group || a.Hidden {
			t.Errorf("action %q = %+v, want Default %q, Tier %v, Group %q, visible", w.id, a, w.def, w.tier, w.group)
		}
	}
}
