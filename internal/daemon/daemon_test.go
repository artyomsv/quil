package daemon

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestIsValidHexID(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		prefix string
		want   bool
	}{
		{"valid pane id", "pane-a1b2c3d4", "pane-", true},
		{"valid tab id", "tab-deadbeef", "tab-", true},
		{"all digits", "pane-12345678", "pane-", true},
		{"all hex letters", "pane-abcdefab", "pane-", true},
		{"uppercase hex rejected", "pane-A1B2C3D4", "pane-", false},
		{"prefix mismatch", "tab-a1b2c3d4", "pane-", false},
		{"too short", "pane-abc", "pane-", false},
		{"too long", "pane-a1b2c3d4e", "pane-", false},
		{"non-hex char", "pane-a1b2c3dz", "pane-", false},
		{"empty string", "", "pane-", false},
		{"prefix only", "pane-", "pane-", false},
		{"missing dash", "panea1b2c3d4", "pane-", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidHexID(tc.id, tc.prefix); got != tc.want {
				t.Errorf("isValidHexID(%q, %q) = %v, want %v", tc.id, tc.prefix, got, tc.want)
			}
		})
	}
}

// TestWorkspaceStateFromSnapshot drives the pure half of buildWorkspaceState
// directly, asserting key shape and value handling. It also exercises the
// optional-field elision for `name`, `type`, `instance_*`, and `layout`.
func TestWorkspaceStateFromSnapshot(t *testing.T) {
	d := New(config.Default())

	tabs := []*Tab{
		{
			ID:     "tab-aaaaaaaa",
			Name:   "Build",
			Color:  "blue",
			Panes:  []string{"pane-11111111"},
			Layout: json.RawMessage(`{"split":"H"}`),
		},
		{
			ID:    "tab-bbbbbbbb",
			Name:  "Notes",
			Panes: []string{"pane-22222222"},
		},
	}
	panesByTab := map[string][]*Pane{
		"tab-aaaaaaaa": {
			{
				ID:           "pane-11111111",
				TabID:        "tab-aaaaaaaa",
				CWD:          "/home/user",
				Name:         "make",
				Type:         "claude-code",
				InstanceName: "default",
				InstanceArgs: []string{"--resume", "abc"},
				PluginState:  map[string]string{"session_id": "abc"},
			},
		},
		"tab-bbbbbbbb": {
			{
				ID:    "pane-22222222",
				TabID: "tab-bbbbbbbb",
				CWD:   "/tmp",
				Type:  "terminal", // omitted from output by design
			},
		},
	}

	state := d.workspaceStateFromSnapshot("tab-aaaaaaaa", tabs, panesByTab)

	if got := state["active_tab"]; got != "tab-aaaaaaaa" {
		t.Errorf("active_tab = %v, want tab-aaaaaaaa", got)
	}

	tabsOut, _ := state["tabs"].([]map[string]any)
	if len(tabsOut) != 2 {
		t.Fatalf("tabs len = %d, want 2", len(tabsOut))
	}
	if tabsOut[0]["id"] != "tab-aaaaaaaa" || tabsOut[0]["color"] != "blue" {
		t.Errorf("tab[0] = %+v", tabsOut[0])
	}
	if _, ok := tabsOut[0]["layout"]; !ok {
		t.Error("tab[0] missing layout key")
	}
	if _, ok := tabsOut[1]["layout"]; ok {
		t.Error("tab[1] has layout — should be elided when zero-length")
	}

	panesOut, _ := state["panes"].([]map[string]any)
	if len(panesOut) != 2 {
		t.Fatalf("panes len = %d, want 2", len(panesOut))
	}

	pane0 := panesOut[0]
	if pane0["id"] != "pane-11111111" || pane0["cwd"] != "/home/user" {
		t.Errorf("pane[0] basic fields = %+v", pane0)
	}
	if pane0["name"] != "make" {
		t.Errorf("pane[0] name = %v, want 'make'", pane0["name"])
	}
	if pane0["type"] != "claude-code" {
		t.Errorf("pane[0] type = %v, want 'claude-code'", pane0["type"])
	}
	if pane0["instance_name"] != "default" {
		t.Errorf("pane[0] instance_name = %v", pane0["instance_name"])
	}
	if args, ok := pane0["instance_args"].([]string); !ok || !reflect.DeepEqual(args, []string{"--resume", "abc"}) {
		t.Errorf("pane[0] instance_args = %v, want [--resume abc]", pane0["instance_args"])
	}
	if ps, ok := pane0["plugin_state"].(map[string]string); !ok || ps["session_id"] != "abc" {
		t.Errorf("pane[0] plugin_state = %v", pane0["plugin_state"])
	}

	// Pane 2 is a default terminal with no extras → optional fields must
	// all be elided to keep workspace.json compact.
	pane1 := panesOut[1]
	for _, k := range []string{"name", "type", "instance_name", "instance_args", "plugin_state"} {
		if _, ok := pane1[k]; ok {
			t.Errorf("pane[1] has unexpected %q key: %v", k, pane1[k])
		}
	}
}

