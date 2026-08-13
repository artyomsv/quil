package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// overlayServerDaemon starts a REAL IPC server for a daemon on a temp socket
// and returns the socket path.
//
// The gate under test is which conns count as clients, so the test has to drive
// real conns through ipc.Server: handleAttach is what registers one and
// handleConn's defer is what retires it, and ipc.Conn has no exported
// constructor, so nothing short of a live socket exercises that pair.
func overlayServerDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)

	d := New(config.Default())
	sock := filepath.Join(home, "s.sock")
	d.server = ipc.NewServer(sock, d.handleMessage, d.onClientDisconnect)
	if err := d.server.Start(); err != nil {
		t.Fatalf("start IPC server: %v", err)
	}
	t.Cleanup(func() { d.server.Stop() })
	return d, sock
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func overlayHiddenAt(p *Pane) time.Time {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.OverlayHiddenAt
}

// attachTestClient dials the daemon and sends MsgAttach, like a TUI.
//
// No wait here: a conn's frames are dispatched sequentially, so anything this
// client sends next is processed after the attach. Callers wait on the effect
// they actually care about — a claim landing, an overlay being stamped — which
// are production predicates rather than a counter kept alive for the tests.
func attachTestClient(t *testing.T, sock string) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	msg, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(msg); err != nil {
		t.Fatalf("send attach: %v", err)
	}
	return c
}

// reportOverlayVisible sends the visibility report a TUI sends, over a real
// conn — the only way the daemon can tell WHICH client is claiming.
func reportOverlayVisible(t *testing.T, c *ipc.Client, paneID string, visible bool) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID:         paneID,
		OverlayVisible: &visible,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(msg); err != nil {
		t.Fatalf("send update pane: %v", err)
	}
}

// claimOverlay attaches a client, has it claim the overlay visible, and waits
// for the claim to land — the shape every real TUI produces.
func claimOverlay(t *testing.T, d *Daemon, sock, paneID string) *ipc.Client {
	t.Helper()
	c := attachTestClient(t, sock)
	reportOverlayVisible(t, c, paneID, true)
	waitUntil(t, "the client's visibility claim to register", func() bool {
		return d.overlayClaimed(paneID)
	})
	return c
}

// Two TUIs on one daemon. Visibility used to be a single daemon-wide field, so
// whichever client spoke last defined it: client B merely switching tabs
// reported an overlay hidden while client A had it ON SCREEN, and the sweep
// destroyed A's lazygit five minutes later — mid-rebase, since lazygit is a
// git-mutating tool. A claim is per client now, and an overlay is hidden only
// when NO client claims it.
func TestHandleUpdatePane_OneClientCannotHideAnotherClientsVisibleOverlay(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	showing := claimOverlay(t, d, sock, p.ID)
	defer showing.Close()

	// The second client adopts the overlay pane with overlayVisible=false and
	// reports hidden the moment it navigates — no attacker required.
	elsewhere := attachTestClient(t, sock)
	defer elsewhere.Close()
	reportOverlayVisible(t, elsewhere, p.ID, false)

	// Ordered on one conn: a round trip this client can observe proves its
	// hidden report was processed.
	reportOverlayVisible(t, elsewhere, "no-such-pane", false)
	waitUntil(t, "the second client's report to be processed", func() bool {
		return d.session.Pane("no-such-pane") == nil
	})
	time.Sleep(50 * time.Millisecond)

	if at := overlayHiddenAt(p); !at.IsZero() {
		t.Fatalf("a second client's hidden report stamped %v on an overlay the first client has on screen", at)
	}

	// Close-out: once the client that HAS it on screen goes, nothing claims it
	// and it is stamped — so the assertion above cannot pass by inertness.
	showing.Close()
	waitUntil(t, "the overlay to be stamped once its last claimant leaves", func() bool {
		return !overlayHiddenAt(p).IsZero()
	})
}

// The same client saying hidden after saying visible releases its own claim —
// the ordinary Alt+G / tab-switch path, which must still stamp.
func TestHandleUpdatePane_TheClaimingClientsOwnHideStamps(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	c := claimOverlay(t, d, sock, p.ID)
	defer c.Close()

	reportOverlayVisible(t, c, p.ID, false)
	waitUntil(t, "the overlay to be stamped hidden", func() bool {
		return !overlayHiddenAt(p).IsZero()
	})
	if d.overlayClaimed(p.ID) {
		t.Error("the claim survived the client's own hidden report")
	}
}

// A conn that never attached is not a client, so its assertion cannot PIN an
// overlay: the timestamps still move (that is unchanged partial-update
// behaviour), but nothing durable is recorded, so the next disconnect
// reconciles the overlay back to hidden.
func TestHandleUpdatePane_AnUnattachedConnCannotClaimAnOverlay(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayHiddenAt = time.Now()
	p.PluginMu.Unlock()

	raw, err := ipc.NewClient(sock) // dials, never attaches
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()
	reportOverlayVisible(t, raw, p.ID, true)
	// The timestamps still move for an unattached conn — that is unchanged
	// partial-update behaviour — and waiting on it is what proves the report
	// was processed before the disconnect below.
	waitUntil(t, "the unattached conn's report to be applied", func() bool {
		return overlayHiddenAt(p).IsZero()
	})

	other := attachTestClient(t, sock)
	waitUntil(t, "both conns to be accepted", func() bool { return d.server.ConnCount() == 2 })
	other.Close()

	waitUntil(t, "the unclaimed overlay to be stamped hidden", func() bool {
		return !overlayHiddenAt(p).IsZero()
	})
	if d.overlayClaimed(p.ID) {
		t.Error("an unattached conn registered a durable visibility claim")
	}
}

// A live MCP bridge holds an IPC conn for its whole lifetime and is a child of
// a claude process in a PANE — so bridges routinely outlive the TUI. Gating the
// detached-session stamp on ConnCount() therefore means it never fires in any
// session with a claude pane wired to `quil mcp`, which is the configuration
// the 7-live-overlay measurement came from. A bridge cannot hold a visibility
// claim (it never attaches), so the TUI leaving takes the last one with it.
func TestOnClientDisconnect_TUIExitStampsOverlaysWhileAnMCPBridgeStaysConnected(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	bridge, err := ipc.NewClient(sock) // dials, never attaches — `quil mcp`
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer bridge.Close()
	tui := claimOverlay(t, d, sock, p.ID)
	waitUntil(t, "both conns to be accepted", func() bool { return d.server.ConnCount() == 2 })

	tui.Close()

	waitUntil(t, "the overlay to be stamped hidden", func() bool {
		return !overlayHiddenAt(p).IsZero()
	})
	if d.server.ConnCount() == 0 {
		t.Fatal("the bridge conn is gone, so this test would pass against a ConnCount gate")
	}
}

// The other direction, and the one that must never fire: a bridge going away
// (a claude pane restarting its MCP server) while the TUI is attached and
// showing the overlay. Stamping there starts a five-minute countdown on a pane
// the user is looking at. The TUI's claim is what has to survive the
// disconnect-time reconciliation.
func TestOnClientDisconnect_BridgeExitLeavesOverlaysAlone(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	tui := claimOverlay(t, d, sock, p.ID)
	defer tui.Close()
	bridge, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	waitUntil(t, "both conns to be accepted", func() bool { return d.server.ConnCount() == 2 })

	// Drain the snapshot slot so receiving on it below proves the disconnect
	// callback actually ran rather than merely that removeConn did.
	select {
	case <-d.snapshotCh:
	default:
	}

	bridge.Close()

	select {
	case <-d.snapshotCh:
	case <-time.After(3 * time.Second):
		t.Fatal("the disconnect callback never ran")
	}
	time.Sleep(50 * time.Millisecond) // let the rest of the callback finish

	if at := overlayHiddenAt(p); !at.IsZero() {
		t.Fatalf("a bridge disconnect stamped the overlay hidden at %v while a TUI was attached", at)
	}

	// Close-out: the same overlay IS stamped when the attached client goes, so
	// the assertion above cannot pass by the machinery being inert.
	tui.Close()
	waitUntil(t, "the overlay to be stamped once the TUI leaves", func() bool {
		return !overlayHiddenAt(p).IsZero()
	})
}
