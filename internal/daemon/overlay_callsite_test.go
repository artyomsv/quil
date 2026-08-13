package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

// The retention feature has exactly TWO production call sites —
// enforceOverlayCap from createPaneAt and sweepIdleOverlays from idleChecker —
// and every other test in this package calls those two functions directly.
// Deleting both lines therefore left the whole package green (verified by
// mutation), i.e. the feature could be made completely inert without a single
// red test. These two tests drive the real entry points instead: the create
// path for the cap, and the running ticker goroutine for the sweep.

// overlayTestDaemon builds a daemon whose spawn path uses a fakeSession, so the
// REAL create-pane entry point can be driven without launching /bin/sh. Mirrors
// newTestDaemon (lazy_restore_test.go) but takes a config, because the policy
// under test lives in it.
//
// Mutates the package-level newSessionFn, so no caller may use t.Parallel().
func overlayTestDaemon(t *testing.T, cfg config.Config) *Daemon {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return &fakeSession{} }
	t.Cleanup(func() { newSessionFn = prev })
	return New(cfg)
}

func setOverlayShown(p *Pane, at time.Time) {
	p.PluginMu.Lock()
	p.OverlayShownAt = at
	p.PluginMu.Unlock()
}

// Drives the ACTUAL create path. createPaneAt is where the cap is enforced, and
// the assertion is about the pane that path evicted — not about what
// enforceOverlayCap returns when called by hand.
func TestCreatePaneAt_OverlayPastTheCapEvictsThroughTheCreatePath(t *testing.T) {
	cfg := config.Default()
	cfg.Overlay.MaxLive = 2
	d := overlayTestDaemon(t, cfg)
	tab := d.session.CreateTab("t")

	newOverlay := func() *Pane {
		t.Helper()
		p, err := d.createPaneAt(ipc.CreatePanePayload{TabID: tab.ID, Overlay: true}, "", "terminal")
		if err != nil {
			t.Fatalf("createPaneAt: %v", err)
		}
		return p
	}

	// Explicit, distinct shown times: createPaneAt stamps OverlayShownAt at
	// creation, and three creates in a row can land close enough that the LRU
	// sort (not stable) has no defined answer.
	stale := newOverlay()
	setOverlayShown(stale, time.Now().Add(-3*time.Hour))
	recent := newOverlay()
	setOverlayShown(recent, time.Now().Add(-1*time.Hour))

	admitted := newOverlay() // the third one, one past the cap of 2

	if d.session.Pane(stale.ID) != nil {
		t.Error("creating an overlay past the cap evicted nothing; the create path is not wired to enforceOverlayCap")
	}
	if d.session.Pane(recent.ID) == nil {
		t.Error("the more recently shown overlay was evicted; the cap must evict LEAST recently shown")
	}
	if d.session.Pane(admitted.ID) == nil {
		t.Error("the overlay being admitted evicted itself through the create path")
	}
}

// A non-overlay create must not disturb the pool, even when the session sits at
// the cap — createPaneAt only enforces inside its `if payload.Overlay` branch.
func TestCreatePaneAt_NormalPaneAtTheCapEvictsNothing(t *testing.T) {
	cfg := config.Default()
	cfg.Overlay.MaxLive = 1
	d := overlayTestDaemon(t, cfg)
	tab := d.session.CreateTab("t")

	existing := overlayPane(t, d, tab.ID)
	if _, err := d.createPaneAt(ipc.CreatePanePayload{TabID: tab.ID}, "", "terminal"); err != nil {
		t.Fatalf("createPaneAt: %v", err)
	}

	if d.session.Pane(existing.ID) == nil {
		t.Error("creating an ORDINARY pane evicted an overlay")
	}
}

// The sweep's only production caller is the 1 s idleChecker tick, so this drives
// the real goroutine rather than a work function extracted for the test's
// convenience. Removing d.sweepIdleOverlays from that loop fails here and
// nowhere else.
func TestIdleChecker_TickSweepsAnExpiredOverlay(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayHiddenAt = time.Now().Add(-6 * time.Minute) // default timeout is 5m
	p.PluginMu.Unlock()

	go d.idleChecker()
	t.Cleanup(func() { d.shutdownOnce.Do(func() { close(d.shutdown) }) })

	waitUntil(t, "the idle checker's tick to evict the expired overlay", func() bool {
		return d.session.Pane(p.ID) == nil
	})
}
