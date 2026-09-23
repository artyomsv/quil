package daemon

import (
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestHandleUpdateTab_ColorTransitions covers the color half of
// handleUpdateTab, in particular the ClearColor flag that lets the tab-color
// cycle wrap from the last palette color back to the default. Before the
// flag existed, an empty color sent together with a name was treated as "no
// change", so the cycle appeared stuck on the last color.
func TestHandleUpdateTab_ColorTransitions(t *testing.T) {
	cases := []struct {
		name      string
		initial   string
		payload   ipc.UpdateTabPayload
		wantColor string
	}{
		{
			name:      "set color",
			initial:   "",
			payload:   ipc.UpdateTabPayload{Name: "Shell", Color: "1"},
			wantColor: "1",
		},
		{
			name:      "rename keeps existing color",
			initial:   "208",
			payload:   ipc.UpdateTabPayload{Name: "Build"},
			wantColor: "208",
		},
		{
			name:      "cycle wrap clears color via ClearColor despite name present",
			initial:   "208",
			payload:   ipc.UpdateTabPayload{Name: "Shell", ClearColor: true},
			wantColor: "",
		},
		{
			name:      "legacy clear: empty name and empty color",
			initial:   "4",
			payload:   ipc.UpdateTabPayload{},
			wantColor: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New(config.Default())
			tab := d.session.CreateTab("Shell")
			tab.Color = tc.initial
			tc.payload.TabID = tab.ID

			msg, err := ipc.NewMessage(ipc.MsgUpdateTab, tc.payload)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			d.handleUpdateTab(msg)

			if tab.Color != tc.wantColor {
				t.Errorf("tab.Color = %q, want %q", tab.Color, tc.wantColor)
			}
		})
	}
}

// TestHandleUpdateTab_SchedulesSnapshot pins the fix: a rename/colour change
// used to broadcast without ever calling requestSnapshot(), so it survived
// only if the 30s periodic snapshot happened to run before the daemon
// stopped. The tab is created directly on d.session, which schedules no
// snapshot of its own, so the only way snapshotCh can hold an entry after
// the handler runs is if handleUpdateTab requested one.
func TestHandleUpdateTab_SchedulesSnapshot(t *testing.T) {
	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	msg, err := ipc.NewMessage(ipc.MsgUpdateTab, ipc.UpdateTabPayload{
		TabID: tab.ID,
		Name:  "Build",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdateTab(msg)

	if len(d.snapshotCh) != 1 {
		t.Errorf("snapshotCh len = %d, want 1 — the handler must schedule a "+
			"snapshot or the edit lives only in memory until the periodic "+
			"ticker happens to fire", len(d.snapshotCh))
	}
}

// TestHandleUpdateTab_UnknownTabSchedulesNothing pins the new edge case: an
// unknown tab ID must not broadcast or schedule a snapshot. It already
// returned before broadcasting; this asserts the snapshot request follows
// the same early-return, not just the broadcast.
func TestHandleUpdateTab_UnknownTabSchedulesNothing(t *testing.T) {
	d := New(config.Default())

	msg, err := ipc.NewMessage(ipc.MsgUpdateTab, ipc.UpdateTabPayload{
		TabID: "no-such-tab",
		Name:  "Build",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdateTab(msg)

	if len(d.snapshotCh) != 0 {
		t.Errorf("snapshotCh len = %d, want 0 — an unknown tab must schedule "+
			"nothing", len(d.snapshotCh))
	}
}

// TestUpdateTab_DoesNotRaceSnapshotState follows the pattern of
// TestSnapshotState_TabPanesDoNotRaceConcurrentCreatePane
// (snapshotstate_race_test.go:28): one goroutine drives handleUpdateTab in a
// loop while another loops SnapshotState and reads the returned tab's
// Name/Color.
//
// This test only means something under
// ./scripts/dev.sh test-race internal/daemon. It fails against the OLD
// handler, which wrote tab.Name/tab.Color through the live *Tab with no
// lock: SnapshotState copies *tab under sm.mu.RLock (session.go:1011), so
// the unlocked write races the reader.
//
// The writer goes through handleUpdateTab (an ipc.NewMessage built once per
// iteration), not sm.UpdateTab directly, so the test pins the HANDLER —
// SessionManager.UpdateTab locking itself is not enough if the handler ever
// stopped calling it.
func TestUpdateTab_DoesNotRaceSnapshotState(t *testing.T) {
	d := New(config.Default())
	tab := d.session.CreateTab("race")

	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			msg, err := ipc.NewMessage(ipc.MsgUpdateTab, ipc.UpdateTabPayload{
				TabID: tab.ID,
				Name:  "n",
				Color: "4",
			})
			if err != nil {
				return
			}
			d.handleUpdateTab(msg)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_, tabs, _, _, _ := d.session.SnapshotState()
			for _, tb := range tabs {
				_ = tb.Name
				_ = tb.Color
			}
		}
	}()

	wg.Wait()
}
