package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

// A deferred sbx pane spawned lazily — a project or tab switch onto it after
// a restart — stands down with an announce (beginSbxPrepare → broadcastState)
// while ensurePaneSpawned still holds its spawnMu. The state builder used to
// read Pending under that same spawnMu, so the announce waited on its own
// caller with stateMu held, and every later broadcast on the daemon waited
// behind it: one project switch froze attach, new tab and new pane for every
// client (2026-10-22, v1.93.0). Pending is an atomic now; this pins that the
// lazy spawn of an sbx pane returns with a LIVE server — broadcastState
// returns early on a nil one, which is why no earlier test reached the lock.
func TestEnsurePaneSpawned_SbxPaneWithALiveServerDoesNotDeadlock(t *testing.T) {
	d, pane, r := sbxFixture(t, false)
	pane.SandboxName = "team-box"
	r.lsJSON = lsJSON(t, "team-box", pane.CWD)
	// Never started: a Broadcast to no conns is all the announce needs. The
	// git cache is what the state builder reads on the way, and the fixture's
	// literal Daemon has none.
	d.gitCache = newGitCache()
	d.server = ipc.NewServer(filepath.Join(t.TempDir(), "s.sock"), d.handleMessage, d.onClientDisconnect)

	prevSession := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return &fakeSession{} }
	t.Cleanup(func() { newSessionFn = prevSession })
	pane.Pending.Store(true)

	done := make(chan struct{})
	go func() {
		d.ensurePaneSpawned(pane)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ensurePaneSpawned did not return: the sbx stand-down's broadcastState waited on the spawnMu its caller holds")
	}
	if pane.Pending.Load() {
		t.Error("Pending was not cleared by the lazy spawn")
	}
	pane.PluginMu.Lock()
	spawnErr := pane.SpawnError
	pane.PluginMu.Unlock()
	if spawnErr != "" {
		t.Errorf("SpawnError = %q, want none for a stand-down", spawnErr)
	}

	// The lock that froze every later broadcast is not held: a broadcast
	// after the lazy spawn returns too.
	broadcast := make(chan struct{})
	go func() {
		d.broadcastState()
		close(broadcast)
	}()
	select {
	case <-broadcast:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcastState after the lazy spawn did not return")
	}
}
