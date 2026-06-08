package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestSnapshotRestore_PaneSizeRoundTrip verifies pane dimensions survive the
// workspace snapshot → restore cycle. Without persisted cols/rows the daemon
// respawns every restored pane's ConPTY at the 80x24 default and the child
// boots at the wrong size — interactive TUIs (claude-code) then render an
// 80-column UI inside a full-width pane until the next window resize.
func TestSnapshotRestore_PaneSizeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Cols = 238
	pane.Rows = 45

	// A second pane with no size recorded yet — must restore at 0/0 so the
	// respawn path falls back to the default constructor.
	pane2, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	d.snapshot()

	d2 := New(config.Default())
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	restored := d2.session.Pane(pane.ID)
	if restored == nil {
		t.Fatalf("pane %s not restored", pane.ID)
	}
	if restored.Cols != 238 || restored.Rows != 45 {
		t.Errorf("restored size = %dx%d, want 238x45", restored.Cols, restored.Rows)
	}

	restored2 := d2.session.Pane(pane2.ID)
	if restored2 == nil {
		t.Fatalf("pane %s not restored", pane2.ID)
	}
	if restored2.Cols != 0 || restored2.Rows != 0 {
		t.Errorf("size-less pane restored as %dx%d, want 0x0", restored2.Cols, restored2.Rows)
	}
}
