package daemon

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/ringbuf"
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
//
// NOTE: this helper mutates the package-level newSessionFn var, so tests that
// use it MUST NOT call t.Parallel() — a parallel scheduler would let two stubs
// collide.
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

// countingSession wraps fakeSession but increments a shared atomic counter on
// construction so a concurrent test can assert spawnPane ran exactly once.
type countingSession struct {
	fakeSession
}

// TestEnsurePaneSpawned_ConcurrentSpawnsOnce launches N goroutines all racing
// to spawn the SAME pending pane and asserts the underlying PTY constructor ran
// exactly once. This is what validates spawnMu actually serializes lazy spawns
// — the sequential idempotency test proves nothing about concurrency. Run under
// -race to also catch unsynchronized access to pane.Pending / pane.PTY.
func TestEnsurePaneSpawned_ConcurrentSpawnsOnce(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	var spawnCount atomic.Int64
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session {
		spawnCount.Add(1)
		return &countingSession{}
	}
	t.Cleanup(func() { newSessionFn = prev })

	d := New(config.Default())
	pane := &Pane{ID: "pane-0000000d", TabID: "tab-0000000d", Type: "terminal", Pending: true}
	d.session.RestoreTab(&Tab{ID: "tab-0000000d", Name: "D", Panes: []string{"pane-0000000d"}}, []*Pane{pane})

	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start // line everyone up so the race is real
			d.ensurePaneSpawned(pane)
		}()
	}
	close(start)
	wg.Wait()

	if got := spawnCount.Load(); got != 1 {
		t.Errorf("PTY constructed %d times, want exactly 1 — spawnMu did not serialize", got)
	}

	pane.PluginMu.Lock()
	gotPTY := pane.PTY
	pane.PluginMu.Unlock()
	if gotPTY == nil {
		t.Errorf("pane should be spawned (PTY non-nil) after concurrent ensure")
	}
	pane.spawnMu.Lock()
	pending := pane.Pending
	pane.spawnMu.Unlock()
	if pending {
		t.Errorf("Pending should be cleared after spawn")
	}
}

func TestSnapshot_PreservesDeferredPaneGhost(t *testing.T) {
	d := newTestDaemon(t)

	bufDir := config.BufferDir()
	if err := os.MkdirAll(bufDir, 0o700); err != nil {
		t.Fatalf("mkdir bufdir: %v", err)
	}
	ghost := []byte("important scrollback history\n")
	if err := persist.SaveBuffer(bufDir, "pane-0000000f", ghost); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	pane := &Pane{
		ID: "pane-0000000f", TabID: "tab-0000000f", Type: "terminal", Pending: true,
		OutputBuf: ringbuf.NewRingBuffer(d.session.bufSize),
	}
	pane.OutputBuf.Write(ghost) // restore pre-fills OutputBuf from the ghost — replicate that
	d.session.RestoreTab(&Tab{ID: "tab-0000000f", Name: "F", Panes: []string{"pane-0000000f"}}, []*Pane{pane})

	d.snapshot()

	got, err := persist.LoadBuffer(bufDir, "pane-0000000f")
	if err != nil {
		t.Fatalf("load ghost after snapshot: %v", err)
	}
	if string(got) != string(ghost) {
		t.Errorf("deferred pane ghost clobbered by snapshot: got %q want %q", got, ghost)
	}
}

func TestListPanes_DeferredPaneReportsNotRunning(t *testing.T) {
	d := newTestDaemon(t)
	d.session.RestoreTab(&Tab{ID: "tab-0000000f", Name: "F", Panes: []string{"pane-0000000f"}}, []*Pane{
		{ID: "pane-0000000f", TabID: "tab-0000000f", Type: "terminal", Pending: true},
	})
	infos := d.buildPaneInfos()
	var found bool
	for _, pi := range infos {
		if pi.ID == "pane-0000000f" {
			found = true
			if pi.Running {
				t.Errorf("deferred pane should report Running=false")
			}
			if !pi.Pending {
				t.Errorf("deferred pane should report Pending=true")
			}
		}
	}
	if !found {
		t.Fatalf("deferred pane missing from list")
	}
}
