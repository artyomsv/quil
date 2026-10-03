package ipc

import (
	"encoding/json"
	"testing"
)

func keysOf(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPaneState_ZeroValue_OnlyAlwaysPresentKeys(t *testing.T) {
	got := keysOf(t, PaneState{ID: "p", TabID: "t"})
	want := map[string]bool{"id": true, "tab_id": true, "cwd": true}
	for k := range got {
		if !want[k] {
			t.Errorf("zero PaneState emitted %q; only id, tab_id and cwd are unconditional", k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("zero PaneState dropped always-present key %q", k)
		}
	}
}

func TestPaneState_PointerZeroes_ArePresent(t *testing.T) {
	zero64, zeroInt, empty := int64(0), 0, ""
	got := keysOf(t, PaneState{ContextTokens: &zero64, GitAhead: &zeroInt, GitBehind: &zeroInt, SandboxAuth: &empty, SandboxClaudeConfig: &empty})
	for _, k := range []string{"context_tokens", "git_ahead", "git_behind", "sandbox_auth", "sandbox_claude_config"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%q set to a zero pointer was omitted; the daemon sends it even at zero", k)
		}
	}
}

func TestTabState_LayoutRevZero_IsPresent(t *testing.T) {
	got := keysOf(t, TabState{ID: "t", Panes: []string{}})
	for _, k := range []string{"id", "name", "color", "panes", "project_id", "layout_rev"} {
		if _, ok := got[k]; !ok {
			t.Errorf("always-present tab key %q missing", k)
		}
	}
	for _, k := range []string{"layout", "template_layout", "template_main"} {
		if _, ok := got[k]; ok {
			t.Errorf("conditional tab key %q present on a zero tab", k)
		}
	}
}

func TestProjectState_AllKeysAlwaysPresent(t *testing.T) {
	got := keysOf(t, ProjectState{})
	for _, k := range []string{"id", "name", "root_dir", "tab_ids", "active_tab", "bootstrap"} {
		if _, ok := got[k]; !ok {
			t.Errorf("project key %q missing", k)
		}
	}
}

func TestWorkspaceState_DiskShape_OmitsBroadcastOnlyKeys(t *testing.T) {
	got := keysOf(t, WorkspaceState{Tabs: []TabState{}, Panes: []PaneState{}, Projects: []ProjectState{}})
	for _, k := range []string{"active_tab", "tabs", "panes", "projects", "active_project"} {
		if _, ok := got[k]; !ok {
			t.Errorf("always-present key %q missing", k)
		}
	}
	for _, k := range []string{"update", "size_master", "clients", "daemon_limited", "rev", "run_id"} {
		if _, ok := got[k]; ok {
			t.Errorf("broadcast-only key %q present on a disk-shaped state", k)
		}
	}
}

func TestWorkspaceState_EmptyMasterPointer_IsPresent(t *testing.T) {
	empty, zero := "", 0
	got := keysOf(t, WorkspaceState{SizeMaster: &empty, Clients: &zero})
	if v, ok := got["size_master"]; !ok || v != "" {
		t.Errorf("size_master = %v (present %v); the broadcast sends \"\" for no master", v, ok)
	}
	if _, ok := got["clients"]; !ok {
		t.Error("clients = 0 was omitted; the broadcast always sends the count")
	}
}
