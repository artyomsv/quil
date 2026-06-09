package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// callUpdatePaneEager drives handleUpdatePane with just the Eager field set.
func callUpdatePaneEager(t *testing.T, d *Daemon, paneID string, eager bool) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID: paneID,
		Eager:  &eager,
	})
	if err != nil {
		t.Fatalf("build msg: %v", err)
	}
	d.handleUpdatePane(msg)
}

func TestHandleUpdatePane_EagerFieldToggle(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-00000001", Name: "t", Panes: []string{"pane-00000001"}}
	panes := []*Pane{
		{ID: "pane-00000001", TabID: "tab-00000001", Type: "terminal", Name: "keep"},
	}
	d.session.RestoreTab(tab, panes)

	callUpdatePaneEager(t, d, "pane-00000001", true)
	if p := d.session.Pane("pane-00000001"); !p.Eager {
		t.Errorf("after update: Eager should be true")
	}
	if p := d.session.Pane("pane-00000001"); p.Name != "keep" {
		t.Errorf("Name should be preserved when only Eager is updated: got %q", p.Name)
	}

	callUpdatePaneEager(t, d, "pane-00000001", false)
	if p := d.session.Pane("pane-00000001"); p.Eager {
		t.Errorf("after second update: Eager should be false")
	}
}
