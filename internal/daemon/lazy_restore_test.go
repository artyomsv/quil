package daemon

import (
	"os"
	"path/filepath"
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
	return newTestDaemonInDir(t, t.TempDir())
}

// newTestDaemonInDir is like newTestDaemon but uses an explicit QUIL_HOME
// directory. Use this when two daemon instances in the same test must share the
// same workspace path (e.g. snapshot → restore round-trip tests).
func newTestDaemonInDir(t *testing.T, dir string) *Daemon {
	t.Helper()
	t.Setenv("QUIL_HOME", dir)
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

// TestEnsurePaneSpawned_ConcurrentWithSnapshot_NoRace exercises the data race
// fixed by guarding Pane.Type / Pane.CWD under PluginMu. A deferred pane whose
// saved CWD does NOT exist takes the `pane.CWD = ""` branch on lazy spawn
// (spawnRestoredPane). That write must be synchronized against snapshot() /
// buildPaneInfos() readers running on other goroutines.
//
// Without the reader-side locking (workspaceStateFromSnapshot / buildPaneInfos
// reading Type/CWD under PluginMu) this races the writer and `go test -race`
// reports a data race on Pane.CWD. The assertions are intentionally weak — the
// real signal is the race detector and the absence of a panic.
func TestEnsurePaneSpawned_ConcurrentWithSnapshot_NoRace(t *testing.T) {
	d := newTestDaemon(t)

	// A path that does not exist forces spawnRestoredPane down the
	// `pane.CWD = ""` branch on the lazy spawn (os.Stat fails).
	missingCWD := filepath.Join(t.TempDir(), "does-not-exist-"+t.Name())

	pane := &Pane{
		ID: "pane-00000011", TabID: "tab-00000011", Type: "terminal",
		CWD: missingCWD, Pending: true,
		OutputBuf: ringbuf.NewRingBuffer(d.session.bufSize),
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-00000011", Name: "R", Panes: []string{"pane-00000011"}},
		[]*Pane{pane},
	)

	var readerWG, writerWG sync.WaitGroup
	stop := make(chan struct{})

	// Reader goroutine: hammer snapshot() + buildPaneInfos() (both read
	// Type/CWD) while the writer repeatedly flips CWD on lazy spawn.
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				d.snapshot()
				_ = d.buildPaneInfos()
			}
		}
	}()

	// Writer goroutine: repeatedly reset the pane to the deferred state with a
	// missing saved CWD, then trigger the lazy spawn. spawnRestoredPane takes
	// the `pane.CWD = ""` branch each time — a write that must be synchronized
	// against the readers above. Looping keeps the write window wide so the
	// race detector reliably observes the conflict when the reader-side lock is
	// removed (the test would be vacuous against a single one-shot spawn).
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for i := 0; i < 500; i++ {
			pane.spawnMu.Lock()
			pane.Pending = true
			pane.spawnMu.Unlock()
			pane.PluginMu.Lock()
			pane.PTY = nil
			pane.CWD = missingCWD
			pane.PluginMu.Unlock()
			d.ensurePaneSpawned(pane)
		}
	}()

	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	// The lazy spawn must have completed and cleared the stale CWD.
	pane.PluginMu.Lock()
	gotCWD := pane.CWD
	gotType := pane.Type
	pane.PluginMu.Unlock()
	if gotCWD != "" {
		t.Errorf("missing saved CWD should have been cleared on lazy spawn, got %q", gotCWD)
	}
	if gotType != "terminal" {
		t.Errorf("Type unexpectedly changed: got %q want %q", gotType, "terminal")
	}
	pane.spawnMu.Lock()
	pending := pane.Pending
	pane.spawnMu.Unlock()
	if pending {
		t.Errorf("Pending should be cleared after lazy spawn")
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

func TestSnapshot_PreservesEmptyDeferredPaneGhost(t *testing.T) {
	d := newTestDaemon(t)

	bufDir := config.BufferDir()
	if err := os.MkdirAll(bufDir, 0o700); err != nil {
		t.Fatalf("mkdir bufdir: %v", err)
	}
	ghost := []byte("history that must not be deleted\n")
	if err := persist.SaveBuffer(bufDir, "pane-00000010", ghost); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	// Deferred pane whose OutputBuf is EMPTY (simulating a future where ghost
	// loading is lazy / not pre-filled). snapshot() will skip writing it
	// (len==0 guard); the on-disk ghost must SURVIVE because the pane is still
	// an active pane id and CleanBuffers must not delete it.
	pane := &Pane{
		ID: "pane-00000010", TabID: "tab-00000010", Type: "terminal", Pending: true,
		OutputBuf: ringbuf.NewRingBuffer(d.session.bufSize),
	}
	d.session.RestoreTab(&Tab{ID: "tab-00000010", Name: "G", Panes: []string{"pane-00000010"}}, []*Pane{pane})

	d.snapshot()

	got, err := persist.LoadBuffer(bufDir, "pane-00000010")
	if err != nil {
		t.Fatalf("load ghost after snapshot: %v", err)
	}
	if string(got) != string(ghost) {
		t.Errorf("empty-OutputBuf deferred pane ghost lost on snapshot: got %q want %q", got, ghost)
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

// TestBroadcast_NilServerDoesNotPanic guards against the panic window where
// respawnPanes (called before d.server is assigned in Start) spawns panes whose
// streamPTYOutput goroutines may call flushPaneOutput → broadcast before the
// IPC server exists. A nil d.server must be silently ignored.
func TestBroadcast_NilServerDoesNotPanic(t *testing.T) {
	d := newTestDaemon(t)
	// newTestDaemon calls New() but never Start(), so d.server is nil — this is
	// exactly the window the bug occurs in. Verify and document that assumption.
	if d.server != nil {
		t.Skip("d.server unexpectedly non-nil after New(); test precondition not met")
	}
	msg, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "pane-1", Data: []byte("x")})
	if err != nil {
		t.Fatalf("build msg: %v", err)
	}
	// Must not panic.
	d.broadcast(msg)
}

// TestRestoreWorkspace_EagerRoundTrip verifies that Pane.Eager survives a
// snapshot → restore cycle. workspaceStateFromSnapshot writes
// paneData["eager"] = true and restoreWorkspace reads it back; this test
// catches any omission of the write, a key typo, or a wrong type assertion.
//
// Two daemon instances share a single QUIL_HOME (set via t.Setenv before
// constructing either daemon) so d1.snapshot() writes workspace.json and
// d2.restoreWorkspace() reads the same file. newTestDaemonInDir is used for
// both so each daemon gets the same fakeSession swap — but since QUIL_HOME is
// already set before the first call, both daemons resolve config.WorkspacePath()
// to the shared directory.
func TestRestoreWorkspace_EagerRoundTrip(t *testing.T) {
	// Pin a single QUIL_HOME for both daemon instances.
	quilHome := t.TempDir()

	d1 := newTestDaemonInDir(t, quilHome)

	d1.session.RestoreTab(
		&Tab{ID: "tab-00000020", Name: "T", Panes: []string{"pane-00000020", "pane-00000021"}},
		[]*Pane{
			{ID: "pane-00000020", TabID: "tab-00000020", Type: "terminal", Eager: true},
			{ID: "pane-00000021", TabID: "tab-00000020", Type: "terminal", Eager: false},
		},
	)

	d1.snapshot()

	// Construct a fresh daemon against the same QUIL_HOME and restore the
	// snapshot written above.
	d2 := newTestDaemonInDir(t, quilHome)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	eagerPane := d2.session.Pane("pane-00000020")
	if eagerPane == nil {
		t.Fatalf("eager pane not found after restore")
	}
	if !eagerPane.Eager {
		t.Errorf("Eager=true pane: Eager should be true after round-trip, got false")
	}

	nonEagerPane := d2.session.Pane("pane-00000021")
	if nonEagerPane == nil {
		t.Fatalf("non-eager pane not found after restore")
	}
	if nonEagerPane.Eager {
		t.Errorf("Eager=false pane: Eager should be false after round-trip, got true")
	}
}

// TestPaneStatus_DeferredPaneReportsNotRunning verifies that buildPaneStatus
// (the pure-function half extracted from handlePaneStatusReq) returns
// Running=false and Pending=true for a deferred pane whose PTY has not yet
// been spawned, matching the contract of buildPaneInfos / TestListPanes_DeferredPaneReportsNotRunning.
// It also asserts that the pane's PTY is still nil after the call — buildPaneStatus
// must not trigger a lazy spawn.
func TestPaneStatus_DeferredPaneReportsNotRunning(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{
		ID:      "pane-00000022",
		TabID:   "tab-00000022",
		Type:    "terminal",
		Pending: true,
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-00000022", Name: "S", Panes: []string{"pane-00000022"}},
		[]*Pane{pane},
	)

	status := d.buildPaneStatus(pane)

	if status.Running {
		t.Errorf("deferred pane should report Running=false, got true")
	}
	if !status.Pending {
		t.Errorf("deferred pane should report Pending=true, got false")
	}

	// Non-spawning contract: buildPaneStatus must not trigger ensurePaneSpawned.
	pane.PluginMu.Lock()
	ptyAfter := pane.PTY
	pane.PluginMu.Unlock()
	if ptyAfter != nil {
		t.Errorf("buildPaneStatus must not spawn the pane (PTY should remain nil)")
	}
}
