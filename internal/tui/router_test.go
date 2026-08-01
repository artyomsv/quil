package tui

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// fakeConn is a Client whose sends are recorded and whose receives are driven
// by the test. Defined here rather than in a per-test file because the later
// multi-daemon tests in this package reuse it; cmd/quil is a different package
// and needs its own.
type fakeConn struct {
	mu   sync.Mutex
	sent []*ipc.Message
	recv chan *ipc.Message
	err  error
}

func newFakeConn() *fakeConn { return &fakeConn{recv: make(chan *ipc.Message, 8)} }

func (f *fakeConn) Send(m *ipc.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeConn) Receive() (*ipc.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	m, ok := <-f.recv
	if !ok {
		return nil, io.EOF
	}
	return m, nil
}

func (f *fakeConn) sentCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

// lastSent returns the most recent message, or nil. Copied out under the lock:
// the pump goroutines of the other conns are still live when a test reads this.
func (f *fakeConn) lastSent() *ipc.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return nil
	}
	return f.sent[len(f.sent)-1]
}

// tabWithPane builds a one-pane tab, the smallest shape destOfPane can resolve.
func tabWithPane(tabID, paneID string) *TabModel {
	tab := NewTabModel(tabID, tabID)
	tab.Root = NewLeaf(NewPaneModel(paneID, 1024))
	tab.ActivePane = paneID
	return tab
}

func TestRouterEmitsExactlyOneLinkLostPerFailure(t *testing.T) {
	dead := newFakeConn()
	dead.err = errors.New("connection reset")
	r := NewRouter(map[string]Client{"gpu01": dead})

	first, err := r.Receive()
	if err != nil || first.Type != ipc.MsgLinkLost || first.Origin != "gpu01" {
		t.Fatalf("first = %+v, err = %v", first, err)
	}

	// A pump that loops would have flooded the channel by now.
	time.Sleep(50 * time.Millisecond)
	select {
	case extra := <-r.in:
		t.Fatalf("pump busy-looped: got a second %s — it must return after one", extra.Type)
	default:
	}
}

func TestRouterSendsToDestNamedByOrigin(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: "pane-x"})
	msg.Origin = "gpu01"
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d, want 1/0", gpu.sentCount(), local.sentCount())
	}
}

func TestRouterEmptyOriginResolvesToActiveDest(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	r.SetActiveDest("gpu01")

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatal("an unstamped message must go to the ACTIVE dest, not to local")
	}
}

func TestRouterActiveDestIsMutableAfterConstruction(t *testing.T) {
	// The Model is copied by tea.NewProgram, so a closure captured in main
	// would freeze at startup. The router must read a value that the running
	// program can still update.
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	first, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	r.Send(first)
	if local.sentCount() != 1 {
		t.Fatal("before any SetActiveDest, an unstamped send goes to local")
	}

	r.SetActiveDest("gpu01")
	second, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	r.Send(second)
	if gpu.sentCount() != 1 {
		t.Fatal("SetActiveDest must take effect on later sends")
	}
}

func TestRouterEmptyOriginFallsBackToSoleConn(t *testing.T) {
	// Remote-only startup: no projects yet, so the active dest is still "",
	// and there is no "" conn. The message must still reach the one daemon.
	gpu := newFakeConn()
	r := NewRouter(map[string]Client{"gpu01": gpu})

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gpu.sentCount() != 1 {
		t.Fatal("with exactly one connection, an unresolvable send must still reach it")
	}
}

func TestRouterDropsSendToUnknownDest(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: "pane-x"})
	msg.Origin = "offline-host"

	if err := r.Send(msg); err != nil {
		t.Fatalf("send to an offline dest must drop, not error: %v", err)
	}
	if local.sentCount() != 0 || gpu.sentCount() != 0 {
		t.Fatal("a message for an offline dest must not fall through to another daemon")
	}
}

// A stamped send whose dest is offline must not fall back to the sole-conn
// path either: that fallback exists for the unstamped startup case only, and
// widening it would put a remote pane's input on the local daemon.
func TestRouterSoleConnFallbackIsUnstampedOnly(t *testing.T) {
	local := newFakeConn()
	r := NewRouter(map[string]Client{"": local})

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-gpu", Data: []byte("x")})
	msg.Origin = "gpu01"
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if local.sentCount() != 0 {
		t.Fatal("input stamped for a dest that is not connected must drop, not land on the sole conn")
	}
}

func TestRouterPumpStampsOriginOnEveryMessage(t *testing.T) {
	gpu := newFakeConn()
	r := NewRouter(map[string]Client{"gpu01": gpu})

	out, _ := ipc.NewMessage(ipc.MsgWorkspaceState, nil)
	gpu.recv <- out

	got, err := r.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got.Origin != "gpu01" {
		t.Fatalf("Origin = %q, want gpu01 — applyWorkspaceState scopes its merge by this", got.Origin)
	}
}

func TestRouterAddStartsPumpForALaterConn(t *testing.T) {
	local := newFakeConn()
	r := NewRouter(map[string]Client{"": local})

	gpu := newFakeConn()
	r.Add("gpu01", gpu)

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	msg.Origin = "gpu01"
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gpu.sentCount() != 1 {
		t.Fatal("Add must make the new dest routable")
	}

	out, _ := ipc.NewMessage(ipc.MsgWorkspaceState, nil)
	gpu.recv <- out
	got, err := r.Receive()
	if err != nil || got.Origin != "gpu01" {
		t.Fatalf("got = %+v, err = %v — Add must also start a pump", got, err)
	}
}

func TestRouterRemoveStopsRouting(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	r.Remove("gpu01")

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	msg.Origin = "gpu01"
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gpu.sentCount() != 0 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d — a removed dest routes nowhere", gpu.sentCount(), local.sentCount())
	}
}

func TestPaneInputRoutesToThePanesOwnDaemon(t *testing.T) {
	// The regression this whole mechanism exists to prevent: keystrokes are
	// not stamped by default, and the pane being typed into may not belong to
	// the active project.
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	r.SetActiveDest("")

	m := Model{
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-gpu")}},
		},
		activeProject: 0,
	}

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-gpu", Data: []byte("x")})
	m.sendForPane("pane-gpu", msg)

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d — input for a remote pane must not reach the local daemon",
			gpu.sentCount(), local.sentCount())
	}
}

// A pane the Model has never seen (created by the daemon a moment ago) falls
// back to the active dest — the only defensible guess, and the same daemon the
// user is looking at.
func TestSendForPaneFallsBackToActiveDestForAnUnknownPane(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	m := Model{
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-gpu")}},
		},
		activeProject: 1,
	}
	r.SetActiveDest(m.activeDest())

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-unknown", Data: []byte("x")})
	m.sendForPane("pane-unknown", msg)

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d — an unknown pane follows the active project", gpu.sentCount(), local.sentCount())
	}
}

// The overlay pane lives OUTSIDE the layout tree, so a FindLeaf-only lookup
// misses it — and Alt+G's destroy/resize sends name it by ID.
func TestDestOfPaneResolvesAnOverlayPane(t *testing.T) {
	tab := tabWithPane("tab-9", "pane-gpu")
	tab.overlayPane = NewPaneModel("pane-overlay", 1024)

	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tab}},
		},
		activeProject: 0,
	}
	if got := m.destOfPane("pane-overlay"); got != "gpu01" {
		t.Fatalf("destOfPane(overlay) = %q, want gpu01", got)
	}
}

func TestSyncActiveDestPushesTheActiveProjectsDest(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	m := Model{
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-gpu")}},
		},
		activeProject: 1,
	}
	m.syncActiveDest()

	// createTab is unstamped on purpose: a new tab belongs to the project the
	// user is looking at, which is what the pushed value now names.
	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d — syncActiveDest did not reach the router", gpu.sentCount(), local.sentCount())
	}
}

// syncActiveDest must tolerate the two-method clients: *ipc.Client and every
// test fake are not destRouters, and ~46 tests build a Model with one.
func TestSyncActiveDestIsANoOpOnAPlainClient(t *testing.T) {
	m := Model{client: &fakeSender{}}
	m.syncActiveDest()

	var nilClient Model
	nilClient.syncActiveDest()
}

func TestJumpToPaneSyncsTheActiveDest(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	m := Model{
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-gpu")}},
		},
		activeProject: 0,
	}
	if !m.jumpToPane("pane-gpu") {
		t.Fatal("jumpToPane could not reach the remote pane")
	}

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gpu.sentCount() != 1 {
		t.Fatal("a cross-project jump must move the router's default with it")
	}
}
