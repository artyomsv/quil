//go:build integration

package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// TestUpdateTab_SurvivesTheWireAndARestart drives the real round trip: encode
// → handleMessage → snapshot → restore. A unit test calling
// SessionManager.UpdateTab directly says nothing about whether the handler
// is wired up, and a rename that lives only in memory until the 30s
// periodic snapshot is exactly the bug this task fixes.
func TestUpdateTab_SurvivesTheWireAndARestart(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	msg, err := ipc.NewMessage(ipc.MsgUpdateTab, ipc.UpdateTabPayload{
		TabID: tab.ID,
		Name:  "Build",
		Color: "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	if tab.Name != "Build" || tab.Color != "4" {
		t.Fatalf("tab = %+v, want Name=Build Color=4", tab)
	}

	// The handler must SCHEDULE the write, not merely leave it in memory.
	// snapshotCh is buffered to 1 and requestSnapshot is a non-blocking
	// send, so a pending request is exactly one queued item.
	if len(d.snapshotCh) != 1 {
		t.Error("the update-tab handler scheduled no snapshot, so the edit " +
			"lives only in memory until the periodic ticker happens to fire")
	}

	d.snapshot()
	state, err := persist.Load(config.WorkspacePath())
	if err != nil {
		t.Fatalf("Load workspace: %v", err)
	}
	raw, _ := state["tabs"].([]any)
	if len(raw) != 1 {
		t.Fatalf("snapshot holds %d tabs, want 1", len(raw))
	}

	fresh := New(config.Default())
	fresh.restoreWorkspace()
	restored := fresh.session.Tab(tab.ID)
	if restored == nil {
		t.Fatalf("tab %s did not restore", tab.ID)
	}
	if restored.Name != "Build" {
		t.Errorf("restored Name = %q, want %q", restored.Name, "Build")
	}
	if restored.Color != "4" {
		t.Errorf("restored Color = %q, want %q", restored.Color, "4")
	}
}
