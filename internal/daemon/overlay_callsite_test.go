package daemon

import (
	"bytes"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
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

// liveFakeSession is a fakeSession whose child stays alive until the PTY is
// closed. The plain fakeSession answers Read with an error and WaitExit with 0
// immediately, so its pane is reaped the instant it is spawned and onPaneExit
// auto-destroys the overlay — which is the very thing these assertions measure,
// and it raced them into passing for the wrong reason.
type liveFakeSession struct {
	fakeSession
	done chan struct{}
	once sync.Once
}

func newLiveFakeSession() *liveFakeSession { return &liveFakeSession{done: make(chan struct{})} }

func (f *liveFakeSession) Read(buf []byte) (int, error) { <-f.done; return 0, io.EOF }
func (f *liveFakeSession) WaitExit() int                { <-f.done; return 0 }
func (f *liveFakeSession) Close() error                 { f.once.Do(func() { close(f.done) }); return nil }

// overlayTestDaemon builds a daemon whose spawn path uses a liveFakeSession, so
// the REAL create-pane entry point can be driven without launching /bin/sh.
// Mirrors newTestDaemon (lazy_restore_test.go) but takes a config, because the
// policy under test lives in it.
//
// Mutates the package-level newSessionFn, so no caller may use t.Parallel().
func overlayTestDaemon(t *testing.T, cfg config.Config) *Daemon {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	var mu sync.Mutex
	var spawned []*liveFakeSession
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session {
		s := newLiveFakeSession()
		mu.Lock()
		spawned = append(spawned, s)
		mu.Unlock()
		return s
	}
	t.Cleanup(func() {
		newSessionFn = prev
		mu.Lock()
		defer mu.Unlock()
		for _, s := range spawned {
			s.Close() // release the exit watchers still parked on WaitExit
		}
	})
	return New(cfg)
}

// overlayServerDaemonWithConfig is overlayServerDaemon with a policy and the
// fake spawn path, for the tests that need a real IPC server AND real panes.
func overlayServerDaemonWithConfig(t *testing.T, cfg config.Config) (*Daemon, string) {
	t.Helper()
	d := overlayTestDaemon(t, cfg)
	sock := filepath.Join(config.QuilDir(), "s.sock")
	d.server = ipc.NewServer(sock, d.handleMessage, d.onClientDisconnect)
	if err := d.server.Start(); err != nil {
		t.Fatalf("start IPC server: %v", err)
	}
	t.Cleanup(func() { d.server.Stop() })
	return d, sock
}

// safeBuffer is an io.Writer the log package can be pointed at from the daemon's
// many goroutines while the test goroutine reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog redirects the standard logger and returns its restore func. The
// daemon log is the surface half of this fix — a failure line on the success
// path is what triage reads first — so it is asserted on directly.
func captureLog(w *safeBuffer) func() {
	prev := log.Writer()
	log.SetOutput(w)
	return func() { log.SetOutput(prev) }
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

// countWorkspaceFrames reads a client's inbound stream in the background and
// reports how many workspace_state frames have arrived. Reset() drops what has
// been seen so far, so a test can measure ONE action.
type frameCounter struct {
	mu sync.Mutex
	n  int
}

func (f *frameCounter) Reset()     { f.mu.Lock(); f.n = 0; f.mu.Unlock() }
func (f *frameCounter) Count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.n }

func countWorkspaceFrames(c *ipc.Client) *frameCounter {
	f := &frameCounter{}
	go func() {
		for {
			msg, err := c.Receive()
			if err != nil {
				return
			}
			if msg.Type == ipc.MsgWorkspaceState {
				f.mu.Lock()
				f.n++
				f.mu.Unlock()
			}
		}
	}()
	return f
}

// An eviction PASS emits one broadcast, not one per pane.
//
// destroyOverlay used to broadcast and request a snapshot itself, so a sweep
// reclaiming N overlays put N full workspace-state frames back to back onto a
// 64-slot must-deliver queue — the 2026-08-09 force-disconnect shape, landing
// on every attached client at once.
//
// Measured while writing this: the second broadcast a review attributed to
// onPaneExit is NOT reachable here. streamPTYOutput only calls onPaneExit while
// the pane is still in the session, and DestroyPane removes it before closing
// the PTY, so an evicted overlay's exit callback never runs (probe: 2 frames,
// 0 exit callbacks, 0 pane-not-found lines for a two-overlay sweep). The log
// assertion below is kept as the guard for the reverse order — a child that
// exits on its own inside the sweep's window.
func TestSweepIdleOverlays_EvictionIsQuietAndBroadcastsOnce(t *testing.T) {
	var logs safeBuffer
	restoreLog := captureLog(&logs)
	defer restoreLog()

	cfg := config.Default()
	cfg.Overlay.MaxLive = 0 // creation must not evict; the sweep is under test
	d, sock := overlayServerDaemonWithConfig(t, cfg)
	tab := d.session.CreateTab("t")

	var ids []string
	for i := 0; i < 2; i++ {
		p, err := d.createPaneAt(ipc.CreatePanePayload{TabID: tab.ID, Overlay: true}, "", "terminal")
		if err != nil {
			t.Fatalf("createPaneAt: %v", err)
		}
		p.PluginMu.Lock()
		p.OverlayHiddenAt = time.Now().Add(-6 * time.Minute)
		p.PluginMu.Unlock()
		ids = append(ids, p.ID)
	}

	client := attachTestClient(t, sock)
	defer client.Close()
	frames := countWorkspaceFrames(client)
	waitUntil(t, "the attach broadcast to land", func() bool { return frames.Count() > 0 })
	frames.Reset()

	if got := d.sweepIdleOverlays(time.Now()); len(got) != 2 {
		t.Fatalf("evicted %v, want both overlays", got)
	}

	// The PTY close and anything it wakes are asynchronous.
	time.Sleep(300 * time.Millisecond)

	if n := frames.Count(); n != 1 {
		t.Errorf("a sweep evicting 2 overlays broadcast %d workspace_state frames, want exactly 1", n)
	}
	for _, id := range ids {
		if strings.Contains(logs.String(), "pane not found: "+id) {
			t.Errorf("a successful eviction of %s logged a pane-not-found failure", id)
		}
	}
}

// A visibility-only update carries nothing any client can observe: neither
// OverlayHiddenAt nor OverlayShownAt is on the wire or on disk. Broadcasting the
// complete workspace state after one was therefore a must-deliver frame
// conveying zero changed state — and this branch made that happen on every tab
// switch, and N times per attach round. Same critical-queue pressure as the
// per-pane eviction broadcast, on a far hotter path.
//
// Drives the real dispatch (handleMessage → handleUpdatePane) over a real conn,
// which is also the only way the overlay field's routing is exercised at all.
func TestHandleUpdatePane_VisibilityOnlyUpdateDoesNotBroadcast(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	client := attachTestClient(t, sock)
	defer client.Close()
	frames := countWorkspaceFrames(client)
	waitUntil(t, "the attach broadcast to land", func() bool { return frames.Count() > 0 })
	frames.Reset()

	reportOverlayVisible(t, client, p.ID, false)
	waitUntil(t, "the visibility report to be applied", func() bool {
		return !overlayHiddenAt(p).IsZero()
	})
	time.Sleep(100 * time.Millisecond)

	if n := frames.Count(); n != 0 {
		t.Errorf("a visibility-only update broadcast %d workspace_state frames, want 0", n)
	}

	// Any other field in the same message still broadcasts — the skip is about
	// the overlay field being unobservable, not about this handler going quiet.
	visible := true
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID:         p.ID,
		Name:           "renamed",
		OverlayVisible: &visible,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitUntil(t, "the rename to broadcast", func() bool { return frames.Count() == 1 })
	if p.Name != "renamed" {
		t.Errorf("pane name = %q, want the rename applied alongside the visibility field", p.Name)
	}
}

// The narrow order this DOES leave: a child that exits on its own while the
// sweep is holding its snapshot, so the eviction destroys the pane first and
// the exit callback arrives second. "Not found" is then the ordinary outcome,
// not a failure — and the pass that evicted it has already broadcast.
func TestOnPaneExit_AlreadyEvictedOverlayIsQuietAndSendsNothing(t *testing.T) {
	var logs safeBuffer
	restoreLog := captureLog(&logs)
	defer restoreLog()

	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	client := attachTestClient(t, sock)
	defer client.Close()
	frames := countWorkspaceFrames(client)
	waitUntil(t, "the attach broadcast to land", func() bool { return frames.Count() > 0 })
	frames.Reset()

	if err := d.session.DestroyPane(p.ID); err != nil { // the eviction got there first
		t.Fatalf("DestroyPane: %v", err)
	}
	d.onPaneExit(p, 0)
	time.Sleep(100 * time.Millisecond)

	if strings.Contains(logs.String(), "destroy overlay") {
		t.Errorf("an already-evicted overlay logged a destroy failure:\n%s", logs.String())
	}
	if n := frames.Count(); n != 0 {
		t.Errorf("an already-evicted overlay's exit broadcast %d workspace_state frames, want 0", n)
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
