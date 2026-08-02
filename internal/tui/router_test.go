package tui

import (
	"errors"
	"io"
	"os"
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

// The mirror of TestPaneInputRoutesToThePanesOwnDaemon, and the direction the
// first round missed: "" is BOTH the local daemon and the zero value of an
// unstamped Origin, so a Send that cannot tell them apart re-aims every local
// pane at whatever remote project happens to be active.
func TestPaneInputForALocalPaneWhileARemoteProjectIsActive(t *testing.T) {
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

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-local", Data: []byte("x")})
	m.sendForPane("pane-local", msg)

	if local.sentCount() != 1 || gpu.sentCount() != 0 {
		t.Fatalf("local=%d gpu=%d — input for a local pane must not follow the active remote project",
			local.sentCount(), gpu.sentCount())
	}
}

// The same hole on the broadcast paths: resizeAllPanes, sendAllLayouts and
// overlayResizeCmd all stamp a project's or tab's dest, which is "" for local.
func TestSendForDestLocalIsHonouredWhileARemoteProjectIsActive(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	r.SetActiveDest("gpu01")

	m := Model{client: r}
	msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: "pane-local"})
	m.sendForDest("", msg)

	if local.sentCount() != 1 || gpu.sentCount() != 0 {
		t.Fatalf("local=%d gpu=%d — an explicit local stamp must not resolve to the active dest",
			local.sentCount(), gpu.sentCount())
	}
	if got := local.lastSent().Origin; got != "" {
		t.Fatalf("Origin = %q — the local sentinel must not escape the router", got)
	}
}

// The sole-conn fallback has the same hole: it exists for an unstamped startup
// send, and must not re-aim a message explicitly addressed to the local daemon
// at the only conn when that conn is a remote one.
func TestSoleConnFallbackIgnoresAnExplicitLocalStamp(t *testing.T) {
	gpu := newFakeConn()
	r := NewRouter(map[string]Client{"gpu01": gpu})

	m := Model{client: r}
	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-local", Data: []byte("x")})
	m.sendForDest("", msg)

	if gpu.sentCount() != 0 {
		t.Fatal("a local-stamped message must drop when no local conn exists, not fall through to the remote one")
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

// A live dest is never replaced: two pumps on one conn would race two readers
// over one socket.
func TestRouterAddIgnoresADuplicateLiveDest(t *testing.T) {
	first, second := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"gpu01": first})
	r.Add("gpu01", second)

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	msg.Origin = "gpu01"
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if first.sentCount() != 1 || second.sentCount() != 0 {
		t.Fatalf("first=%d second=%d — Add must not displace a live conn",
			first.sentCount(), second.sentCount())
	}
}

// The documented reconnect path: a pump that died must leave its dest free, or
// Add is a silent no-op and every later Send keeps hitting the dead client.
func TestRouterAddAfterPumpDeathInstallsAFreshConn(t *testing.T) {
	dead := newFakeConn()
	dead.err = errors.New("connection reset")
	r := NewRouter(map[string]Client{"gpu01": dead})

	if lost, err := r.Receive(); err != nil || lost.Type != ipc.MsgLinkLost {
		t.Fatalf("lost = %+v, err = %v", lost, err)
	}

	fresh := newFakeConn()
	r.Add("gpu01", fresh)

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	msg.Origin = "gpu01"
	if err := r.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fresh.sentCount() != 1 || dead.sentCount() != 0 {
		t.Fatalf("fresh=%d dead=%d — Add after a pump death must replace the conn",
			fresh.sentCount(), dead.sentCount())
	}

	out, _ := ipc.NewMessage(ipc.MsgWorkspaceState, nil)
	fresh.recv <- out
	got, err := r.Receive()
	if err != nil || got.Origin != "gpu01" {
		t.Fatalf("got = %+v, err = %v — the replacement needs a pump of its own", got, err)
	}
}

// A conn the caller removed must not publish afterwards. Both the message path
// and the failure path are checked, because a two-case select picks uniformly:
// with a buffered r.in both cases are ready, so an unprioritised stop leaks
// roughly half of everything a removed pump reads — including a link loss that
// quits the TUI in local mode.
func TestRouterRemovedPumpPublishesNothing(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		gpu := newFakeConn()
		r := NewRouter(map[string]Client{"gpu01": gpu})
		// Queue the message BEFORE the removal so the pump is guaranteed to
		// have something to publish once it wakes.
		out, _ := ipc.NewMessage(ipc.MsgWorkspaceState, nil)
		gpu.recv <- out
		r.Remove("gpu01")
		close(gpu.recv)

		time.Sleep(50 * time.Millisecond)
		select {
		case leaked := <-r.in:
			t.Fatalf("removed pump published %s", leaked.Type)
		default:
		}
	})

	t.Run("failure", func(t *testing.T) {
		gpu := newFakeConn()
		r := NewRouter(map[string]Client{"gpu01": gpu})
		r.Remove("gpu01")
		close(gpu.recv) // unparks Receive with io.EOF

		time.Sleep(50 * time.Millisecond)
		select {
		case leaked := <-r.in:
			t.Fatalf("removed pump published %s — a deliberate disconnect is not a link loss", leaked.Type)
		default:
		}
	})
}

// A Client that breaks Receive's contract by returning (nil, nil) must not
// crash the pump on the Origin stamp, and must not become a busy loop either.
func TestRouterPumpSurvivesANilMessage(t *testing.T) {
	nilConn := &nilMessageConn{}
	r := NewRouter(map[string]Client{"gpu01": nilConn})

	first, err := r.Receive()
	if err != nil || first.Type != ipc.MsgLinkLost {
		t.Fatalf("first = %+v, err = %v — a contract-breaking client must read as a link loss", first, err)
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case extra := <-r.in:
		t.Fatalf("pump busy-looped on nil messages: got %s", extra.Type)
	default:
	}
}

// nilMessageConn returns (nil, nil) forever — the contract violation the pump's
// nil guard exists for.
type nilMessageConn struct{}

func (c *nilMessageConn) Send(*ipc.Message) error        { return nil }
func (c *nilMessageConn) Receive() (*ipc.Message, error) { return nil, nil }

// scriptedConn replays queued messages as if they had arrived off the wire.
type scriptedConn struct{ msgs chan *ipc.Message }

func (c *scriptedConn) Send(*ipc.Message) error        { return nil }
func (c *scriptedConn) Receive() (*ipc.Message, error) { return <-c.msgs, nil }

// A daemon can put any type on the wire, link_lost included. Acting on one
// would let the peer quit the TUI in local mode or start reconnect churn in
// remote mode, where the daemon is on a host the user may not control.
func TestWireBorneLinkLostIsIgnored(t *testing.T) {
	c := &scriptedConn{msgs: make(chan *ipc.Message, 1)}
	c.msgs <- &ipc.Message{Type: ipc.MsgLinkLost}

	m := Model{client: c}
	got := m.listenForMessages()()
	if _, ok := got.(listenContinueMsg); !ok {
		t.Fatalf("msg is %T, want listenContinueMsg — a daemon must not be able to drop the link", got)
	}
}

// The router's own synthesised loss still has to get through, naming its dest.
func TestRouterLinkLostReachesTheModel(t *testing.T) {
	dead := newFakeConn()
	dead.err = errors.New("connection reset")
	r := NewRouter(map[string]Client{"gpu01": dead})

	m := Model{client: r}
	got := m.listenForMessages()()
	lost, ok := got.(linkLostMsg)
	if !ok {
		t.Fatalf("msg is %T, want linkLostMsg", got)
	}
	if lost.dest != "gpu01" {
		t.Fatalf("dest = %q, want gpu01", lost.dest)
	}
	if lost.err == nil {
		t.Error("no cause carried; the reconnect banner has nothing to show")
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

// A dead conn must stay REACHABLE, or nothing can ever close it.
//
// Client is deliberately Send/Receive only, so this package cannot release the
// ssh child behind one — cmd/quil does, when redialCmd hands it the dead conn.
// The pump therefore retires its LIVENESS registration and leaves the record.
// Dropping the record instead made Conn(dest) nil for exactly the destination a
// redial was about to run for, leaking one ssh process and one remote
// `quil --stdio` per reconnect, for the life of the session.
func TestRouterKeepsTheDeadConnReachableForTheRedial(t *testing.T) {
	dead := newFakeConn()
	dead.err = errors.New("connection reset")
	r := NewRouter(map[string]Client{"gpu01": dead})

	if lost, err := r.Receive(); err != nil || lost.Type != ipc.MsgLinkLost {
		t.Fatalf("lost = %+v, err = %v", lost, err)
	}

	if got := r.Conn("gpu01"); got != Client(dead) {
		t.Fatalf("Conn after a pump death = %v, want the dead conn — the dialer is "+
			"handed this value and is the only layer that can close it", got)
	}

	// And the retirement must still let a replacement in: liveness is keyed off
	// the stop channel now, so a lingering conn record cannot make Add a no-op.
	fresh := newFakeConn()
	r.Add("gpu01", fresh)
	if got := r.Conn("gpu01"); got != Client(fresh) {
		t.Fatal("Add did not replace the dead conn; the reconnected daemon stays silent")
	}
}

// Exit must release every connection, not just one.
//
// closeClientFn type-asserts to *ipc.Client, so handing it the *Router simply
// missed and exit closed NOTHING. cmd/quil's own deferred Close cannot cover it
// either — it captured the startup conn of a single destination.
func TestCloseClientReleasesEveryRouterConn(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	closed := map[Client]bool{}
	m := Model{client: r}
	m.SetClientCloser(func(c Client) { closed[c] = true })

	m.CloseClient()

	if !closed[Client(local)] || !closed[Client(gpu)] {
		t.Fatalf("closed = %d conn(s), want both — every unclosed ssh child and its "+
			"remote `quil --stdio` outlives the client", len(closed))
	}
}

// Every connection is attached exactly once, however many WindowSizeMsgs arrive.
//
// Once for correctness: a router holding two conns delivered the old unstamped
// startup attach to conns[""] alone, so the second daemon was never attached —
// no workspace state, no projects, no error. At MOST once because every attach
// replays the daemon's whole ghost buffer, which doubles a pane's scrollback and
// re-counts the work-state events inside the replay.
func TestEveryConnIsAttachedExactlyOnce(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	m := Model{client: r, width: 80, height: 24}

	m.attachAllDests()
	m.attachAllDests() // a second WindowSizeMsg must not re-attach

	for name, c := range map[string]*fakeConn{"local": local, "gpu01": gpu} {
		var n int
		c.mu.Lock()
		for _, msg := range c.sent {
			if msg.Type == ipc.MsgAttach {
				n++
			}
		}
		c.mu.Unlock()
		if n != 1 {
			t.Errorf("%s got %d attaches, want exactly 1 — each attach replays the "+
				"full ghost buffer and re-counts work-state events", name, n)
		}
	}
}

// The local daemon's attach carries this process's CWD; a remote one's must not.
// os.Getwd() names a directory on the laptop and the daemon tests it against the
// SERVER's disk, so a path that happens to exist on both is the only thing that
// ever made it look right.
func TestAttachCWDIsPerDestinationInAMixedSession(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	m := Model{client: r, width: 80, height: 24}

	m.attachAllDests()

	cwdOf := func(t *testing.T, c *fakeConn) string {
		t.Helper()
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, msg := range c.sent {
			if msg.Type != ipc.MsgAttach {
				continue
			}
			var p ipc.AttachPayload
			if err := msg.DecodePayload(&p); err != nil {
				t.Fatalf("decode attach: %v", err)
			}
			return p.CWD
		}
		t.Fatal("no attach was sent")
		return ""
	}

	if got := cwdOf(t, gpu); got != "" {
		t.Errorf("remote attach carried CWD %q; the daemon would resolve this "+
			"laptop's path against the server's disk", got)
	}
	wd, _ := os.Getwd()
	if got := cwdOf(t, local); got != wd {
		t.Errorf("local attach CWD = %q, want %q — new panes would open in the "+
			"daemon's frozen working directory instead of the client's", got, wd)
	}
}

// asRemote points the Model's ACTIVE project at dest, which is what "this
// session talks to a daemon on another host" means now that every project
// carries its own destination.
//
// It replaces the SetRemoteDest setter, which was a session-wide flag. That flag
// could not survive a client holding a local daemon beside a remote one: it
// answered "remote" for whichever project was active, including a local one.
//
// A Model built directly by a test usually has no projects at all, so one is
// created — the same shape interimProject builds for the pre-attach client, and
// enough for activeDest() to have something to read.
func (m *Model) asRemote(dest string) {
	if p := m.cur(); p != nil {
		p.Dest = dest
		return
	}
	m.projects = []*ProjectModel{{ID: interimProjectID, Name: interimProjectName, Dest: dest}}
	m.activeProject = 0
}
