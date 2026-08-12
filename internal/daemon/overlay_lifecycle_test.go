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

// LRU, not FIFO: the overlay you keep using must survive even when it was
// created first. This test fails against a FIFO implementation.
func TestEnforceOverlayCap_EvictsLeastRecentlyShownNotOldest(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 2
	d := New(cfg)
	tab := d.session.CreateTab("t")

	oldestButActive := overlayPane(t, d, tab.ID)
	stale := overlayPane(t, d, tab.ID)
	recent := overlayPane(t, d, tab.ID)

	// The oldest-created one was shown a second ago; the middle one has not
	// been looked at in an hour; the newest was just shown. FIFO would evict
	// oldestButActive; LRU must evict stale.
	setShown := func(p *Pane, at time.Time) {
		p.PluginMu.Lock()
		p.OverlayShownAt = at
		p.PluginMu.Unlock()
	}
	setShown(oldestButActive, time.Now().Add(-time.Second))
	setShown(stale, time.Now().Add(-time.Hour))
	setShown(recent, time.Now())

	got := d.enforceOverlayCap("")
	if len(got) != 1 || got[0] != stale.ID {
		t.Fatalf("evicted %v, want [%s] (the least recently SHOWN)", got, stale.ID)
	}
	if d.session.Pane(oldestButActive.ID) == nil {
		t.Error("the oldest-created overlay was evicted; the policy is LRU, not FIFO")
	}
	if d.session.Pane(recent.ID) == nil {
		t.Error("the most recently shown overlay was evicted")
	}
}

// At the cap with nothing being admitted, nothing is evicted: MaxLive is "at
// most N live", not "fewer than N". The distinction only shows up through a
// caller that passes no exclude — createPaneAt always passes one, which is why
// the off-by-one hid.
func TestEnforceOverlayCap_AtCapWithNoAdmissionEvictsNothing(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 2
	d := New(cfg)
	tab := d.session.CreateTab("t")
	a := overlayPane(t, d, tab.ID)
	b := overlayPane(t, d, tab.ID)

	if got := d.enforceOverlayCap(""); len(got) != 0 {
		t.Fatalf("evicted %v at exactly the cap with no admission", got)
	}
	if d.session.Pane(a.ID) == nil || d.session.Pane(b.ID) == nil {
		t.Error("an overlay was destroyed while the session was exactly at the cap")
	}
}

func TestEnforceOverlayCap_ZeroDisablesTheCap(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 0
	d := New(cfg)
	tab := d.session.CreateTab("t")
	for i := 0; i < 8; i++ {
		overlayPane(t, d, tab.ID)
	}
	if got := d.enforceOverlayCap(""); len(got) != 0 {
		t.Fatalf("evicted %v with the cap disabled", got)
	}
}

// The pane being admitted must never evict itself.
func TestEnforceOverlayCap_ExcludesTheNewOverlay(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 1
	d := New(cfg)
	tab := d.session.CreateTab("t")
	fresh := overlayPane(t, d, tab.ID)

	got := d.enforceOverlayCap(fresh.ID)
	for _, id := range got {
		if id == fresh.ID {
			t.Fatal("the overlay being admitted evicted itself")
		}
	}
	if d.session.Pane(fresh.ID) == nil {
		t.Error("the new overlay was destroyed by its own cap check")
	}
}

// Nothing can be displaying an overlay when no client is attached. Without this
// an overlay hidden by a TUI that then exited would keep a zero
// OverlayHiddenAt forever and never become eligible — the case where reclaiming
// matters most, since the user is away.
func TestMarkOverlaysHidden_StampsOverlaysThatLackATimestamp(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	visible := overlayPane(t, d, tab.ID)
	already := overlayPane(t, d, tab.ID)

	earlier := time.Now().Add(-time.Hour)
	already.PluginMu.Lock()
	already.OverlayHiddenAt = earlier
	already.PluginMu.Unlock()

	if n := d.markOverlaysHidden(time.Now()); n != 1 {
		t.Errorf("stamped %d overlays, want 1", n)
	}

	visible.PluginMu.Lock()
	got := visible.OverlayHiddenAt
	visible.PluginMu.Unlock()
	if got.IsZero() {
		t.Error("a visible overlay was not marked hidden on last disconnect")
	}

	already.PluginMu.Lock()
	got = already.OverlayHiddenAt
	already.PluginMu.Unlock()
	if !got.Equal(earlier) {
		t.Error("an already-hidden overlay had its deadline pushed out")
	}
}
