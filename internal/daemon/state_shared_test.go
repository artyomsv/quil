package daemon

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// The frozen oracle (state_oracle_test.go) cannot know these keys; this test
// is their contract instead: wire, workspace.json and restore.
func TestSharedData_WireDiskAndRestoreRoundTrip(t *testing.T) {
	d := newTestDaemon(t) // lazy_restore_test.go:66 — sets QUIL_HOME
	p := d.session.CreateProject("api", t.TempDir())
	if err := d.session.GroupOp("create", "empty", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.session.SetProjectGroup(p.ID, "infra"); err != nil {
		t.Fatal(err)
	}
	d.session.RecordRecentCWD("/one")
	d.session.RecordRecentCWD("/two")
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.noteMu.Lock()
	pane.NoteRev.Store(7)
	pane.noteMu.Unlock()

	wire := stateMap(t, d.buildWorkspaceState())
	if wire["shared_data"] != true {
		t.Error("broadcast lacks shared_data: true")
	}
	if g, _ := wire["groups"].([]any); len(g) != 2 {
		t.Errorf("groups on the wire = %v", wire["groups"])
	}
	if r, _ := wire["recent_cwds"].([]any); len(r) != 2 || r[0] != "/two" {
		t.Errorf("recent_cwds on the wire = %v", wire["recent_cwds"])
	}

	d.snapshot()
	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["shared_data"]; ok {
		t.Error("shared_data written to workspace.json; it is broadcast-only")
	}
	if g, _ := disk["groups"].([]any); len(g) != 2 {
		t.Errorf("groups on disk = %v", disk["groups"])
	}

	fresh := newTestDaemonInDir(t, os.Getenv("QUIL_HOME"))
	if err := fresh.restoreWorkspace(); err != nil {
		t.Fatal(err)
	}
	groups, recent := fresh.session.SharedSnapshot()
	if len(groups) != 2 || len(recent) != 2 || recent[0] != "/two" {
		t.Errorf("restored groups=%v recent=%v", groups, recent)
	}
	for _, rp := range fresh.session.Projects() {
		if rp.ID == p.ID && rp.Group != "infra" {
			t.Errorf("restored project group = %q", rp.Group)
		}
	}
	rp := fresh.session.Pane(pane.ID)
	if rp == nil {
		t.Fatal("pane not restored")
	}
	rp.noteMu.Lock()
	rev := rp.NoteRev.Load()
	rp.noteMu.Unlock()
	if rev != 7 {
		t.Errorf("restored note_rev = %d, want 7", rev)
	}
}

// A project whose group is missing from the list gets the name appended.
func TestRestoreShared_RepairsAProjectGroupMissingFromTheList(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("api", "/r")
	sm.mu.Lock()
	sm.projects[p.ID].Group = "orphan"
	sm.mu.Unlock()
	sm.RestoreShared([]string{"kept"}, nil)
	groups, _ := sm.SharedSnapshot()
	if len(groups) != 2 || groups[1] != "orphan" {
		t.Errorf("groups = %v, want [kept orphan]", groups)
	}
}
