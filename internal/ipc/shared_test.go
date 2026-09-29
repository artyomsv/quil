package ipc

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceState_SharedKeys_OmittedAtZero(t *testing.T) {
	got := keysOf(t, WorkspaceState{Tabs: []TabState{}, Panes: []PaneState{}, Projects: []ProjectState{}})
	for _, k := range []string{"shared_data", "groups", "recent_cwds"} {
		if _, ok := got[k]; ok {
			t.Errorf("zero-value key %q present; an older client must see the old shape", k)
		}
	}
	got = keysOf(t, WorkspaceState{SharedData: true, Groups: []string{"a"}, RecentCWDs: []string{"/x"}})
	for _, k := range []string{"shared_data", "groups", "recent_cwds"} {
		if _, ok := got[k]; !ok {
			t.Errorf("set key %q missing", k)
		}
	}
}

func TestProjectState_GroupOmittedWhenEmpty(t *testing.T) {
	if _, ok := keysOf(t, ProjectState{})["group"]; ok {
		t.Error("group present on an ungrouped project")
	}
	if v := keysOf(t, ProjectState{Group: "infra"})["group"]; v != "infra" {
		t.Errorf("group = %v", v)
	}
}

func TestPaneState_NoteRevOmittedAtZero(t *testing.T) {
	if _, ok := keysOf(t, PaneState{ID: "p", TabID: "t"})["note_rev"]; ok {
		t.Error("note_rev present at 0")
	}
	if v := keysOf(t, PaneState{NoteRev: 3})["note_rev"]; v != float64(3) {
		t.Errorf("note_rev = %v", v)
	}
}

func TestNoteSetRespPayload_JSONKeys(t *testing.T) {
	b, err := json.Marshal(NoteSetRespPayload{PaneID: "p", OK: false, Conflict: true, CurrentRev: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pane_id":"p","ok":false,"conflict":true,"current_rev":4}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}

func TestSharedImportPayload_KindsAndKeys(t *testing.T) {
	b, err := json.Marshal(SharedImportPayload{
		Kinds:  []string{ImportKindGroups, ImportKindNotes},
		Groups: []SharedImportGroup{{Name: "g", ProjectIDs: []string{"proj-1"}}},
		Notes:  []SharedImportNote{{PaneID: "pane-1", Text: "hi\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"kinds", "groups", "notes"} {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
	if _, ok := got["recent"]; ok {
		t.Error("recent present although not sent")
	}
}

func TestDaemonCaps_ListsSharedData(t *testing.T) {
	found := false
	for _, c := range DaemonCaps() {
		if c == CapSharedData {
			found = true
		}
	}
	if !found {
		t.Errorf("DaemonCaps() = %v lacks %q", DaemonCaps(), CapSharedData)
	}
}
