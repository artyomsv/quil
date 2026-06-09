package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
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

// newTestDaemon builds a daemon whose spawn path uses a fakeSession instead of
// a real PTY/process. The terminal plugin's real spawn shells out to /bin/sh
// (and rewrites the command via shellinit), which is brittle inside the test
// container; the lazy-restore logic under test is the *selection* decision
// (active/eager → spawn; others → Pending), not the PTY mechanics. Swapping the
// PTY constructor for a fake lets the assertions check pane.PTY != nil after a
// successful spawn without depending on a child process actually launching.
//
// newSessionFn follows the same swappable-package-var pattern this codebase
// already uses for test seams (claudeSessionExistsFn, readHookSessionIDFn).
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return &fakeSession{} }
	t.Cleanup(func() { newSessionFn = prev })
	return New(config.Default())
}

func TestRespawnPanes_DefersNonActiveNonEager(t *testing.T) {
	d := newTestDaemon(t)
	d.session.RestoreTab(&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}}, []*Pane{
		{ID: "pane-0000000a", TabID: "tab-0000000a", Type: "terminal"},
	})
	d.session.RestoreTab(&Tab{ID: "tab-0000000b", Name: "B", Panes: []string{"pane-0000000b", "pane-0000000e"}}, []*Pane{
		{ID: "pane-0000000b", TabID: "tab-0000000b", Type: "terminal"},
		{ID: "pane-0000000e", TabID: "tab-0000000b", Type: "terminal", Eager: true},
	})
	d.session.SwitchTab("tab-0000000a")

	d.respawnPanes()

	if p := d.session.Pane("pane-0000000a"); p.PTY == nil || p.Pending {
		t.Errorf("active-tab pane should be spawned, not pending")
	}
	if p := d.session.Pane("pane-0000000e"); p.PTY == nil || p.Pending {
		t.Errorf("eager pane should be spawned")
	}
	if p := d.session.Pane("pane-0000000b"); p.PTY != nil || !p.Pending {
		t.Errorf("non-active non-eager pane should be pending, not spawned")
	}
}

func TestEnsurePaneSpawned_IsIdempotent(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "pane-0000000c", TabID: "tab-0000000c", Type: "terminal", Pending: true}
	d.session.RestoreTab(&Tab{ID: "tab-0000000c", Name: "C", Panes: []string{"pane-0000000c"}}, []*Pane{pane})

	d.ensurePaneSpawned(pane)
	first := pane.PTY
	if first == nil || pane.Pending {
		t.Fatalf("first ensure should spawn and clear Pending")
	}
	d.ensurePaneSpawned(pane)
	if pane.PTY != first {
		t.Errorf("second ensure must not respawn (PTY pointer changed)")
	}
}
