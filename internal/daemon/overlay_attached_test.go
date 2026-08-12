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
func attachTestClient(t *testing.T, d *Daemon, sock string) *ipc.Client {
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
	waitUntil(t, "the daemon to register the attach", func() bool { return d.attachedClientCount() == 1 })
	return c
}

// A live MCP bridge holds an IPC conn for its whole lifetime and is a child of
// a claude process in a PANE — so bridges routinely outlive the TUI. Gating the
// detached-session stamp on ConnCount() therefore means it never fires in any
// session with a claude pane wired to `quil mcp`, which is the configuration
// the 7-live-overlay measurement came from.
func TestOnClientDisconnect_TUIExitStampsOverlaysWhileAnMCPBridgeStaysConnected(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	bridge, err := ipc.NewClient(sock) // dials, never attaches — `quil mcp`
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer bridge.Close()
	tui := attachTestClient(t, d, sock)
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
// the user is looking at.
func TestOnClientDisconnect_BridgeExitLeavesOverlaysAlone(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	tui := attachTestClient(t, d, sock)
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
