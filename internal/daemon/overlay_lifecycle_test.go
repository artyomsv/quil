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
