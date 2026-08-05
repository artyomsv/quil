//go:build integration

package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// The fold has to survive the REAL round trip: encode → handleMessage → snapshot
// → restore.
//
// The unit tests call MergeProjects directly, so they say nothing about whether
// the handler is wired up at all, and a project message that is accepted and
// silently ignored is the exact failure this package has already paid for — a
// daemon that took a create and did nothing read as a broken dialog for an
// evening. The persistence half matters just as much: the fold REASSIGNS tabs
// and DROPS project records, so a snapshot that missed either side would bring
// the duplicates back on the next daemon start, holding tabs that no longer
// agree about who owns them.
func TestMergeProjects_SurvivesTheWireAndARestart(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	keep := d.session.CreateProject("Default1", "/home/a/.quil")
	dupA := d.session.CreateProject("cluster-management", "/home/a/homelab")
	dupB := d.session.CreateProject("cluster-management", "/home/a/homelab")
	d.session.CreateTabInProject(keep.ID, "shorli")
	d.session.CreateTabInProject(dupA.ID, "Shell")
	d.session.CreateTabInProject(dupB.ID, "Shell")

	// The exact payload the client sends.
	msg, err := ipc.NewMessage(ipc.MsgMergeProjects, ipc.MergeProjectsPayload{
		ProjectID: keep.ID,
		Absorb:    []string{dupA.ID, dupB.ID},
		Name:      "cluster-management",
	})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	projects := d.session.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d after the fold, want 1 — the handler is not wired "+
			"up, and a message accepted and ignored looks exactly like a broken dialog",
			len(projects))
	}
	if projects[0].Name != "cluster-management" {
		t.Errorf("name = %q, want the name the user typed", projects[0].Name)
	}
	if len(projects[0].TabIDs) != 3 {
		t.Errorf("survivor holds %d tabs, want all 3", len(projects[0].TabIDs))
	}
	// Untouched: the payload carries no root, so a fold cannot relocate a
	// project. Asserted through the REAL wire because the field's absence is the
	// guarantee — a payload that regrew one would be applied here.
	if projects[0].RootDir != "/home/a/.quil" {
		t.Errorf("RootDir = %q, want the survivor's own, unchanged", projects[0].RootDir)
	}

	// The handler must SCHEDULE the write, not merely leave the state right in
	// memory. snapshotCh is buffered to 1 and requestSnapshot is a non-blocking
	// send, so a pending request is exactly one queued item. Asserted because
	// calling d.snapshot() below bypasses the trigger entirely: without this,
	// deleting the handler's requestSnapshot() left this test green while a fold
	// survived only until the daemon was killed inside the 30 s ticker window.
	if len(d.snapshotCh) != 1 {
		t.Error("the merge handler scheduled no snapshot, so the fold lives only " +
			"in memory until the periodic ticker happens to fire")
	}

	d.snapshot()
	state, err := persist.Load(config.WorkspacePath())
	if err != nil {
		t.Fatalf("Load workspace: %v", err)
	}
	if raw, _ := state["projects"].([]any); len(raw) != 1 {
		t.Fatalf("snapshot holds %d projects, want the single survivor — the "+
			"duplicates come back on the next start", len(raw))
	}

	// A fresh daemon must agree about BOTH sides of the tab↔project link. The
	// client rebuilds a project from its TabIDs and skips any tab whose own
	// project_id disagrees, so a half-persisted fold renders as tabs that exist
	// in the daemon and appear nowhere in the sidebar.
	fresh := New(config.Default())
	fresh.restoreWorkspace()
	restored := fresh.session.Projects()
	if len(restored) != 1 {
		t.Fatalf("restored %d projects, want 1", len(restored))
	}
	if len(restored[0].TabIDs) != 3 {
		t.Fatalf("restored survivor holds %d tabs, want 3", len(restored[0].TabIDs))
	}
	for _, tabID := range restored[0].TabIDs {
		tab, ok := fresh.session.tabs[tabID]
		if !ok {
			t.Errorf("tab %s is listed by the project but did not restore", tabID)
			continue
		}
		if tab.ProjectID != restored[0].ID {
			t.Errorf("restored tab %s records project %q, want %q — the client "+
				"would skip it and the tab would vanish from the sidebar",
				tabID, tab.ProjectID, restored[0].ID)
		}
	}
}
