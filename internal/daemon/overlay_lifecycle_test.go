package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// overlayPane creates a published overlay pane for the lifecycle tests.
func overlayPane(t *testing.T, d *Daemon, tabID string) *Pane {
	t.Helper()
	p, err := d.session.CreatePane(tabID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	p.PluginMu.Lock()
	p.Overlay = true
	p.OverlayShownAt = time.Now()
	p.PluginMu.Unlock()
	return p
}

func TestHandleUpdatePane_OverlayVisibilityStampsHiddenAndShown(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	no := false
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: p.ID, OverlayVisible: &no})
	if err != nil {
		t.Fatal(err)
	}
	d.handleUpdatePane(msg)

	p.PluginMu.Lock()
	hidden := p.OverlayHiddenAt
	p.PluginMu.Unlock()
	if hidden.IsZero() {
		t.Fatal("hiding an overlay did not stamp OverlayHiddenAt")
	}

	yes := true
	msg, err = ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: p.ID, OverlayVisible: &yes})
	if err != nil {
		t.Fatal(err)
	}
	d.handleUpdatePane(msg)

	p.PluginMu.Lock()
	hidden, shown := p.OverlayHiddenAt, p.OverlayShownAt
	p.PluginMu.Unlock()
	if !hidden.IsZero() {
		t.Error("showing an overlay must clear OverlayHiddenAt")
	}
	if shown.IsZero() {
		t.Error("showing an overlay must stamp OverlayShownAt for the LRU order")
	}
}

// A rename must not be readable as "hidden" — the partial-update tri-state is
// the whole reason OverlayVisible is a pointer.
func TestHandleUpdatePane_RenameLeavesOverlayVisibilityAlone(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: p.ID, Name: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	d.handleUpdatePane(msg)

	p.PluginMu.Lock()
	hidden := p.OverlayHiddenAt
	p.PluginMu.Unlock()
	if !hidden.IsZero() {
		t.Error("a rename marked the overlay hidden; the nil pointer must mean 'unchanged'")
	}
}

func TestSweepIdleOverlays_DestroysAnOverlayHiddenPastTheTimeout(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayHiddenAt = time.Now().Add(-6 * time.Minute)
	p.PluginMu.Unlock()

	got := d.sweepIdleOverlays(time.Now())
	if len(got) != 1 || got[0] != p.ID {
		t.Fatalf("evicted %v, want [%s]", got, p.ID)
	}
	if d.session.Pane(p.ID) != nil {
		t.Error("evicted overlay is still in the session")
	}
}

// The case an activity-based implementation gets wrong: lazygit emits nothing
// while you read it, so a VISIBLE overlay looks identical to an idle one.
func TestSweepIdleOverlays_NeverEvictsAVisibleOverlay(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayShownAt = time.Now().Add(-time.Hour) // shown long ago, still shown
	p.OverlayHiddenAt = time.Time{}
	p.PluginMu.Unlock()

	if got := d.sweepIdleOverlays(time.Now()); len(got) != 0 {
		t.Fatalf("evicted %v; a visible overlay must never be evicted", got)
	}
}

func TestSweepIdleOverlays_LeavesNormalPanesAlone(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.sweepIdleOverlays(time.Now().Add(24 * time.Hour)); len(got) != 0 {
		t.Fatalf("evicted %v; only overlay panes are subject to this policy", got)
	}
	if d.session.Pane(p.ID) == nil {
		t.Error("a normal pane was destroyed by the overlay sweep")
	}
}

func TestSweepIdleOverlays_ZeroTimeoutDisablesEviction(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.IdleTimeoutMinutes = 0
	d := New(cfg)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayHiddenAt = time.Now().Add(-24 * time.Hour)
	p.PluginMu.Unlock()

	if got := d.sweepIdleOverlays(time.Now()); len(got) != 0 {
		t.Fatalf("evicted %v with the timeout disabled", got)
	}
}
