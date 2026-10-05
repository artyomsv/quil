package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
)

// A client id that starts with the reserved-master marker is refused at
// attach: a state frame names a reserved slot with that marker, and a client
// wearing it would read itself as the master. Both the exact marker (longer
// than the registry's id cap, so only the RAW id matches it) and a short id
// with the prefix are refused.
func TestClientID_ReservedPrefixRefused(t *testing.T) {
	h := newAuthHarness(t)
	h.d.session.CreateTab("T") // no first-attach bootstrap
	for _, id := range []string{reservedMasterMarker("owner-1"), reservedMasterPrefix + "x"} {
		c := attachTestClientID(t, h.sock, id)
		if p := waitErrorFrame(t, c, 3*time.Second); p.Code != ipc.ErrCodeRefused || p.Message != errClientIDReserved.Error() {
			t.Fatalf("attach as %q got %+v, want refused: %v", id, p, errClientIDReserved)
		}
		if n := h.d.clientCount(); n != 0 {
			t.Fatalf("attach as %q registered a client (count=%d)", id, n)
		}
		c.Close()
	}
}

// viewerThenOwner attaches a read-only viewer FIRST and a local owner second,
// with no input from either, so the viewer is the oldest attached client —
// the implicit target's "nobody typed" default before viewers were skipped.
func viewerThenOwner(t *testing.T, h *authHarness) (viewer, owner *ipc.Client) {
	t.Helper()
	viewer, _ = h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, viewer, "a-viewer") // sorts first if the attach times tie
	waitUntil(t, "viewer attached", func() bool { return h.d.clientCount() == 1 })
	owner = attachTestClientID(t, h.sock, "owner")
	t.Cleanup(func() { owner.Close() })
	waitUntil(t, "owner attached", func() bool { return h.d.clientCount() == 2 })
	return viewer, owner
}

func TestCloseTUI_ImplicitSkipsViewer(t *testing.T) {
	h := newAuthHarness(t)
	h.d.session.CreateTab("T")
	viewer, owner := viewerThenOwner(t, h)

	sendClientMsg(t, h.local(t), ipc.MsgCloseTUI, nil)

	if n := countType(readFor(owner, 500*time.Millisecond), ipc.MsgCloseTUI); n != 1 {
		t.Fatalf("owner got close_tui %d times, want 1 (the viewer is not an implicit target)", n)
	}
	if n := countType(readFor(viewer, 200*time.Millisecond), ipc.MsgCloseTUI); n != 0 {
		t.Fatalf("viewer got close_tui %d times, want 0", n)
	}
}

// An explicit client id is honoured as asked, viewer or not.
func TestCloseTUI_ExplicitViewerHonoured(t *testing.T) {
	h := newAuthHarness(t)
	h.d.session.CreateTab("T")
	viewer, owner := viewerThenOwner(t, h)

	sendClientMsg(t, h.local(t), ipc.MsgCloseTUI, ipc.CloseTUIPayload{Client: "a-viewer"})

	if n := countType(readFor(viewer, 500*time.Millisecond), ipc.MsgCloseTUI); n != 1 {
		t.Fatalf("viewer (named) got close_tui %d times, want 1", n)
	}
	if n := countType(readFor(owner, 200*time.Millisecond), ipc.MsgCloseTUI); n != 0 {
		t.Fatalf("owner got close_tui %d times, want 0", n)
	}
}

func TestSetActivePane_ImplicitSkipsViewer(t *testing.T) {
	h := newAuthHarness(t)
	tab := h.d.session.CreateTab("T")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.d.session.SwitchTab(h.d.session.CreateTab("Other").ID)
	viewer, owner := viewerThenOwner(t, h)

	sendClientMsg(t, h.local(t), ipc.MsgSetActivePane, ipc.SetActivePanePayload{PaneID: pane.ID})

	if n := countType(readFor(owner, 500*time.Millisecond), ipc.MsgSetActivePane); n != 1 {
		t.Fatalf("owner got set_active_pane %d times, want 1 (the viewer is not an implicit target)", n)
	}
	if n := countType(readFor(viewer, 200*time.Millisecond), ipc.MsgSetActivePane); n != 0 {
		t.Fatalf("viewer got set_active_pane %d times, want 0", n)
	}
}

// defaultCWD's most-recently-active step skips viewers. A (master) has a dead
// cwd, B a live one, and the viewer attached last, so with nobody typing it
// was the "most recently active" client — whose cwd is dropped at attach, so
// a bridge fell through to the daemon's own directory instead of B's.
func TestDefaultCWD_SkipsViewer(t *testing.T) {
	h := newAuthHarness(t)
	h.d.session.CreateTab("T")
	attachClientWithCWD(t, h.sock, "A", 200, 50, filepath.Join(t.TempDir(), "gone"))
	waitUntil(t, "A attached", func() bool { return h.d.clientCount() == 1 })
	dirB := resolvedTemp(t)
	attachClientWithCWD(t, h.sock, "B", 100, 30, dirB)
	waitUntil(t, "B attached", func() bool { return h.d.clientCount() == 2 })
	viewer, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, viewer, "viewer") // sorts last if the attach times tie
	waitUntil(t, "viewer attached", func() bool { return h.d.clientCount() == 3 })
	if h.d.masterID() != "A" {
		t.Fatalf("masterID = %q, want A", h.d.masterID())
	}

	got := decodeInto[ipc.BrowseDirRespPayload](t, roundTrip(t, h.local(t), ipc.MsgBrowseDirReq, ipc.MsgBrowseDirResp, ipc.BrowseDirReqPayload{}))
	if got.Resolved != dirB {
		t.Fatalf("a bridge's default dir = %q, want B's cwd %q (the viewer is skipped)", got.Resolved, dirB)
	}
}
