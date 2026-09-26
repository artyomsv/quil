package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestSetTabLayout_CAS pins the compare-and-store: a matching base (including
// the special case of no base at all) accepts and bumps the revision; a stale
// base is refused with NO write, to either the layout or the revision.
func TestSetTabLayout_CAS(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("layout")

	// nil base: accepted unconditionally (an older client, or the tab's very
	// first write, for which nothing has a base to send).
	if !d.session.SetTabLayout(tab.ID, json.RawMessage(`{"v":0}`), nil) {
		t.Fatal("nil base should be accepted")
	}
	if got := d.session.Tab(tab.ID).LayoutRev; got != 1 {
		t.Fatalf("LayoutRev after nil-base write = %d, want 1", got)
	}

	// Current base: accepted, revision bumps again.
	cur := uint64(1)
	if !d.session.SetTabLayout(tab.ID, json.RawMessage(`{"v":1}`), &cur) {
		t.Fatal("a base matching the current revision should be accepted")
	}
	if got := d.session.Tab(tab.ID).LayoutRev; got != 2 {
		t.Fatalf("LayoutRev after matching-base write = %d, want 2", got)
	}

	// Stale base: refused. Neither the layout nor the revision moves.
	beforeLayout := string(d.session.Tab(tab.ID).Layout)
	stale := uint64(0)
	if d.session.SetTabLayout(tab.ID, json.RawMessage(`{"v":99}`), &stale) {
		t.Fatal("a stale base should be refused")
	}
	after := d.session.Tab(tab.ID)
	if after.LayoutRev != 2 {
		t.Errorf("LayoutRev after a refused write = %d, want unchanged 2", after.LayoutRev)
	}
	if string(after.Layout) != beforeLayout {
		t.Errorf("layout changed after a refused write: got %s, want unchanged %s", after.Layout, beforeLayout)
	}

	// Unknown tab: refused.
	if d.session.SetTabLayout("tab-deadbeef", json.RawMessage(`{}`), nil) {
		t.Error("an unknown tab should be refused")
	}
}

// callUpdateLayout builds and dispatches an UpdateLayoutPayload directly at
// handleUpdateLayout, the way the movepane_race_test.go race tests already
// drive it — the PRODUCTION handler, not SetTabLayout in isolation.
func callUpdateLayout(t *testing.T, d *Daemon, tabID string, layout json.RawMessage, baseRev *uint64) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdateLayout, ipc.UpdateLayoutPayload{TabID: tabID, Layout: layout, BaseRev: baseRev})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdateLayout(msg)
}

// fakeCoalesceTimer is one time.AfterFunc requestBroadcast armed. Tests fire
// it by hand instead of waiting on the real 50ms window — the same pattern
// clientsHarness.afterFn uses in clients_test.go.
type fakeCoalesceTimer struct {
	f       func()
	stopped bool
}

// stubBroadcastTimer points d's coalescer at a fake afterFn and returns the
// list it appends every arm to. One arm corresponds to exactly one eventual
// fireCoalescedBroadcast → broadcastState() call (that 1:1 pairing is itself
// pinned by TestRequestBroadcast_CoalescesBurst), so counting arms is
// counting coalesced broadcasts.
func stubBroadcastTimer(d *Daemon) *[]*fakeCoalesceTimer {
	timers := &[]*fakeCoalesceTimer{}
	d.broadcastAfterFn = func(_ time.Duration, f func()) func() bool {
		tm := &fakeCoalesceTimer{f: f}
		*timers = append(*timers, tm)
		return func() bool {
			was := !tm.stopped
			tm.stopped = true
			return was
		}
	}
	return timers
}

// TestHandleUpdateLayout_BroadcastsOnAcceptOnly: an accepted write arms a
// coalesced broadcast; a refused one (stale base_rev) never calls
// requestBroadcast at all, so no new window opens while the first is still
// pending.
func TestHandleUpdateLayout_BroadcastsOnAcceptOnly(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("layout")
	timers := stubBroadcastTimer(d)

	// Accepted (nil base): should arm exactly one coalescer window.
	callUpdateLayout(t, d, tab.ID, json.RawMessage(`{"v":1}`), nil)
	if len(*timers) != 1 {
		t.Fatalf("arms after accepted write = %d, want 1", len(*timers))
	}

	// Refused (stale base, current revision is now 1): must not arm a
	// second window — SetTabLayout never even calls requestBroadcast.
	stale := uint64(0)
	callUpdateLayout(t, d, tab.ID, json.RawMessage(`{"v":2}`), &stale)
	if len(*timers) != 1 {
		t.Fatalf("arms after a refused write = %d, want still 1", len(*timers))
	}

	// Fire the pending window by hand, then confirm the layout the ACCEPTED
	// call wrote is what is live — the refused call above never touched it.
	(*timers)[0].f()
	if got := string(d.session.Tab(tab.ID).Layout); got != `{"v":1}` {
		t.Errorf("live layout = %s, want the accepted write's {\"v\":1}", got)
	}

	// A fresh accepted write after the window closed opens a NEW window.
	rev := uint64(1)
	callUpdateLayout(t, d, tab.ID, json.RawMessage(`{"v":3}`), &rev)
	if len(*timers) != 2 {
		t.Fatalf("arms after a post-fire accepted write = %d, want 2", len(*timers))
	}
}

// TestLayoutRev_SurvivesSnapshotRoundTrip: the revision a tab reaches through
// several accepted writes is exactly what a fresh daemon reads back after
// snapshot -> restore, not reset to 0 and not an off-by-one.
func TestLayoutRev_SurvivesSnapshotRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("layout")
	if !d.session.SetTabLayout(tab.ID, json.RawMessage(`{"v":1}`), nil) {
		t.Fatal("first SetTabLayout should be accepted")
	}
	rev := uint64(1)
	if !d.session.SetTabLayout(tab.ID, json.RawMessage(`{"v":2}`), &rev) {
		t.Fatal("second SetTabLayout should be accepted")
	}
	wantRev := d.session.Tab(tab.ID).LayoutRev
	if wantRev != 2 {
		t.Fatalf("setup: LayoutRev = %d, want 2", wantRev)
	}

	d.snapshot()

	d2 := New(config.Default())
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}
	restored := d2.session.Tab(tab.ID)
	if restored == nil {
		t.Fatalf("tab %s was not restored", tab.ID)
	}
	if restored.LayoutRev != wantRev {
		t.Errorf("restored LayoutRev = %d, want %d", restored.LayoutRev, wantRev)
	}
}

// TestRequestBroadcast_CoalescesBurst: 5 calls inside the coalescing window
// give exactly 1 armed timer, and firing it starts the cycle over.
func TestRequestBroadcast_CoalescesBurst(t *testing.T) {
	d := &Daemon{}
	timers := stubBroadcastTimer(d)

	for i := 0; i < 5; i++ {
		d.requestBroadcast()
	}
	if len(*timers) != 1 {
		t.Fatalf("arms after a burst of 5 = %d, want 1", len(*timers))
	}

	// Firing clears the in-flight marker, so the NEXT call opens a fresh
	// window rather than being folded into the one that just fired.
	(*timers)[0].f()
	d.requestBroadcast()
	if len(*timers) != 2 {
		t.Fatalf("arms after firing + one more call = %d, want 2", len(*timers))
	}
}

// TestWorkspaceState_TabCarriesLayoutRev: layout_rev is on the wire for every
// tab, including one that has never had a layout written (rev 0), and it
// tracks accepted writes.
func TestWorkspaceState_TabCarriesLayoutRev(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("layout")

	findTab := func(state map[string]any) map[string]any {
		t.Helper()
		tabsOut, _ := state["tabs"].([]map[string]any)
		for _, tb := range tabsOut {
			if tb["id"] == tab.ID {
				return tb
			}
		}
		t.Fatalf("tab %s missing from workspace state", tab.ID)
		return nil
	}

	tb := findTab(d.buildWorkspaceState())
	rev, ok := tb["layout_rev"]
	if !ok {
		t.Fatal("layout_rev missing from a tab that has never had a layout written")
	}
	if rev != uint64(0) {
		t.Errorf("layout_rev = %v, want 0", rev)
	}

	if !d.session.SetTabLayout(tab.ID, json.RawMessage(`{"v":1}`), nil) {
		t.Fatal("SetTabLayout should be accepted")
	}
	tb = findTab(d.buildWorkspaceState())
	rev, ok = tb["layout_rev"]
	if !ok || rev != uint64(1) {
		t.Errorf("layout_rev after one accepted write = %v (present=%v), want 1", rev, ok)
	}
}
