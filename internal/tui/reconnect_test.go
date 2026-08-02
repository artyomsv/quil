package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// failingClient returns err from Receive forever.
type failingClient struct{ err error }

func (f *failingClient) Send(*ipc.Message) error        { return nil }
func (f *failingClient) Receive() (*ipc.Message, error) { return nil, f.err }

// closeTUIClient delivers exactly one MsgCloseTUI.
//
// A later call returns an error rather than blocking: listenForMessages reads
// once per invocation, so a second call only happens if the implementation is
// wrong, and an error fails the test where a block would hang it.
type closeTUIClient struct{ sent bool }

func (c *closeTUIClient) Send(*ipc.Message) error { return nil }
func (c *closeTUIClient) Receive() (*ipc.Message, error) {
	if !c.sent {
		c.sent = true
		return &ipc.Message{Type: ipc.MsgCloseTUI}, nil
	}
	return nil, errors.New("closeTUIClient read twice")
}

func isQuit(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}

// testDest is the destination a single-connection remote session keys
// EVERYTHING by: its project's Dest, its link table, its redial func, and the
// dest its link loss carries.
//
// It used to be "", because such a session routed every message unstamped and
// so had no host to key by. The router keys `quil --remote <host>` by that host
// now, exactly as a mixed session keys its remote daemons, so these fixtures do
// too — and they have to, since freezeInput and the banner read the ACTIVE
// project's dest. A link table keyed "" beside a project stamped "gpu01"
// describes no session that can exist.
const testDest = "gpu01"

// oneLink builds a link table holding ONE destination's reconnect state, under
// the key a single-connection remote session routes with.
//
// The argument is copied, so two models built from the same reconnectState hold
// independent state — the value semantics the singleton field used to give for
// free, and which a shared *reconnectState would silently remove.
func oneLink(ls reconnectState) map[string]*reconnectState {
	return map[string]*reconnectState{testDest: &ls}
}

// attachedTo builds the attach ledger with the given destinations already
// attached — the state a Model is in once the first WindowSizeMsg has been
// handled. A map rather than a bool because attach is per-daemon: one
// destination reconnecting must not make the client re-attach every other one,
// and each attach replays a whole ghost buffer.
func attachedTo(dests ...string) map[string]bool {
	out := make(map[string]bool, len(dests))
	for _, d := range dests {
		out[d] = true
	}
	return out
}

// A dead link in remote mode is a reconnectable event, not a quit.
func TestListenForMessages_RemoteLinkLoss_ReturnsLinkLostMsg(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}, clientGen: 3}
	m.asRemote("gpu01")

	msg := m.listenForMessages()()

	lost, ok := msg.(linkLostMsg)
	if !ok {
		t.Fatalf("msg is %T, want linkLostMsg", msg)
	}
	if lost.gen != 3 {
		t.Errorf("gen = %d, want 3", lost.gen)
	}
	if lost.err == nil {
		t.Error("linkLostMsg carries no cause; the banner has nothing to show")
	}
}

// MsgCloseTUI is the daemon asking us to exit. It must never reconnect.
func TestListenForMessages_CloseTUI_ReturnsQuit(t *testing.T) {
	m := Model{client: &closeTUIClient{}}
	m.asRemote("gpu01")

	if msg := m.listenForMessages()(); !isQuit(msg) {
		t.Fatalf("msg is %T, want tea.QuitMsg", msg)
	}
}

// Local mode keeps today's behaviour: a dead local daemon means dead panes, and
// quietly retrying would hide that.
func TestUpdate_LinkLost_LocalMode_Quits(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}}
	// No remote project and no redial func: the local daemon, keyed "".

	_, cmd := m.Update(linkLostMsg{gen: 0, err: errors.New("EOF")})
	if cmd == nil {
		t.Fatal("cmd is nil, want tea.Quit")
	}
	if !isQuit(cmd()) {
		t.Fatal("local link loss did not quit")
	}
}

// Remote mode with a dialer installed enters the reconnecting state instead of
// quitting. This is the behaviour the whole phase is built on.
func TestUpdate_LinkLost_RemoteMode_BeginsReconnect(t *testing.T) {
	m := Model{clientGen: 1}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("unused") })

	updated, _ := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	got := updated.(Model)

	if !got.linkOf(testDest).active {
		t.Fatal("remote link loss did not begin a reconnect")
	}
	if got.linkOf(testDest).lastErr == nil {
		t.Error("reconnect state kept no cause")
	}
}

// Remote mode with no dialer installed is still fatal — there is nothing to
// retry with, and pretending otherwise would hang the session forever.
func TestUpdate_LinkLost_RemoteWithoutDialer_Quits(t *testing.T) {
	m := Model{}
	m.asRemote("gpu01")

	_, cmd := m.Update(linkLostMsg{gen: 0, dest: testDest, err: errors.New("EOF")})
	if cmd == nil || !isQuit(cmd()) {
		t.Fatal("remote link loss without a dialer did not quit")
	}
}

// TestCanReconnect_DoesNotRequireRemoteMode pins that the decision is per
// DESTINATION and reads nothing about what is active.
//
// canReconnect used to require RemoteMode() as a conjunct, which answers for the
// ACTIVE PROJECT. Those are different questions the moment a client holds more
// than one daemon: a background remote host dropping while a LOCAL project is on
// screen would read RemoteMode() == false and turn a perfectly reconnectable
// drop fatal. redialFns[dest] != nil is sufficient alone — a local destination
// never gets a dialer installed in the first place, which is what keeps a dead
// local daemon fatal.
func TestCanReconnect_DoesNotRequireRemoteMode(t *testing.T) {
	m := Model{}
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("unused") })

	if m.RemoteMode() {
		t.Fatal("setup invariant broken: no project is active, so RemoteMode() must read false")
	}
	if !m.canReconnect(testDest) {
		t.Fatalf("canReconnect(%q) = false — a dialer is installed; what is ACTIVE must not gate it", testDest)
	}
	if m.canReconnect("") {
		t.Fatal(`canReconnect("") = true for the local daemon; a dead local daemon must stay fatal`)
	}
}

// One destination dropping must leave every other one alone. This is the whole
// point of a per-daemon link table: a client holding several daemons has no
// single "the link", and a shared flag would park a working host because an
// unrelated one died.
func TestOneDestDroppingDoesNotParkAnother(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Dest: "gpu01"}, {ID: "proj-b", Dest: "prod"},
	}}

	m.handleLinkLost("gpu01", errors.New("connection reset"))

	if !m.linkFor("gpu01").active {
		t.Fatal("gpu01 should be reconnecting")
	}
	if m.linkFor("prod").active {
		t.Fatal("prod must be unaffected by gpu01 dropping")
	}
}

// The client stays on the project the user is looking at, even when that is the
// one whose daemon just died.
func TestActiveProjectStaysPutWhenItsDaemonDrops(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-a", Dest: "gpu01"}, {ID: "proj-b", Dest: ""}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	if m.activeProject != 0 {
		t.Fatal("must not auto-switch away from the project the user is on — " +
			"stale work honestly labelled beats being teleported into different work")
	}
}

// A background daemon dropping must not freeze the keyboard. freezeInput is a
// METHOD, not a flag: it reports whether the message should be dropped, so the
// behaviour is what is asserted.
func TestBackgroundDestDropDoesNotFreezeInput(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	if _, frozen := m.freezeInput(key); frozen {
		t.Fatal("a background daemon dropping must not freeze typing into local panes")
	}
}

// The control arm: the freeze must still fire for the daemon the user IS typing
// into, or a keystroke lands on a link that cannot carry it.
func TestActiveDestDropDoesFreezeInput(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	if _, frozen := m.freezeInput(key); !frozen {
		t.Fatal("input to a daemon that is reconnecting must still be dropped")
	}
}

// A link-loss report from a previous client must be ignored: the old listen
// loop is still parked in Receive when the new client is already live.
func TestUpdate_LinkLost_StaleGeneration_Ignored(t *testing.T) {
	m := Model{clientGen: 5}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("must not dial") })

	updated, cmd := m.Update(linkLostMsg{gen: 4, dest: testDest, err: errors.New("stale")})
	got := updated.(Model)

	if got.linkOf(testDest).active {
		t.Error("a stale generation started a reconnect")
	}
	if cmd != nil {
		t.Error("a stale generation produced a command")
	}
}

// The curve doubles from the base delay and caps, with jitter scaling each
// result into [50%, 100%] of the nominal value. Bounds below are therefore
// [nominal/2, nominal] — attempt 1 is 250-500ms, not 500ms-1s.
func TestReconnectDelay_GrowsAndCaps(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{"first attempt is prompt", 1, 250 * time.Millisecond, 500 * time.Millisecond},
		{"second doubles", 2, 500 * time.Millisecond, 1 * time.Second},
		{"third doubles again", 3, 1 * time.Second, 2 * time.Second},
		{"last of the fast curve", 4, 2 * time.Second, 4 * time.Second},
		// Past reconnectDecayAfter the ceiling rises to reconnectSlowMaxDelay,
		// so the curve keeps doubling instead of plateauing at 30s.
		{"keeps doubling past the fast cap", 7, 16 * time.Second, 32 * time.Second},
		{"caps at the slow plateau", 12, 150 * time.Second, 5 * time.Minute},
		{"stays capped past the shift width", 100, 150 * time.Second, 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, j := range []float64{0, 0.5, 0.999} {
				got := reconnectDelay(tt.attempt, j)
				if got < tt.wantMin || got > tt.wantMax {
					t.Errorf("reconnectDelay(%d, %v) = %v, want within [%v, %v]",
						tt.attempt, j, got, tt.wantMin, tt.wantMax)
				}
			}
		})
	}
}

// Jitter must actually vary the result, or every client of a restarted server
// redials in lockstep and the herd hits it together.
func TestReconnectDelay_JitterVaries(t *testing.T) {
	if reconnectDelay(5, 0) == reconnectDelay(5, 0.99) {
		t.Error("jitter had no effect")
	}
}

// The curve must never go backwards as attempts climb.
func TestReconnectDelay_MonotonicAtFixedJitter(t *testing.T) {
	for _, j := range []float64{0, 0.5, 1} {
		prev := time.Duration(0)
		for a := 1; a <= 15; a++ {
			got := reconnectDelay(a, j)
			if got < prev {
				t.Errorf("jitter %v: attempt %d = %v, shorter than attempt %d = %v", j, a, got, a-1, prev)
			}
			prev = got
		}
	}
}

// Attempt 0 and negatives must not produce a zero or trivial delay — a hot
// redial loop against an unreachable host is worse than the outage.
func TestReconnectDelay_NonPositiveAttempt_StillDelays(t *testing.T) {
	floor := reconnectBaseDelay / 2 // the shortest the jitter window can yield
	for _, a := range []int{-1, 0} {
		if got := reconnectDelay(a, 0); got < floor {
			t.Errorf("reconnectDelay(%d, 0) = %v, want >= %v", a, got, floor)
		}
	}
}

// jitter is documented as [0,1) but is a parameter, so an out-of-range value
// must clamp rather than produce a negative or over-long delay.
func TestReconnectDelay_OutOfRangeJitter_Clamps(t *testing.T) {
	nominal := 4 * time.Second // attempt 4: 500ms << 3
	if got := reconnectDelay(4, -5); got != nominal/2 {
		t.Errorf("negative jitter = %v, want %v (the 50%% floor)", got, nominal/2)
	}
	if got := reconnectDelay(4, 9); got != nominal {
		t.Errorf("jitter above 1 = %v, want %v (the 100%% ceiling)", got, nominal)
	}
}

// The floor must hold for EVERY attempt, not just small ones. The exponent is
// a runtime shift on an int64, so intermediate values wrap rather than
// saturate: 500ms is 2^8 * 1953125 ns, and 1953125 is odd, so the product is
// exactly zero only once the shift reaches 56. Between the first overflow and
// that point the wrapped value is nonzero and its sign is whatever bit 63
// happens to be — a positive wrap smaller than the cap would slip past a
// guard that only tests for zero or negative, and produce a hot redial loop.
func TestReconnectDelay_NeverDropsBelowFloorAtAnyAttempt(t *testing.T) {
	floor := reconnectBaseDelay / 2
	for a := 1; a <= 300; a++ {
		for _, j := range []float64{0, 0.5, 1} {
			if got := reconnectDelay(a, j); got < floor {
				t.Fatalf("reconnectDelay(%d, %v) = %v, below the %v floor", a, j, got, floor)
			}
		}
	}
}

// recordingDialer counts calls and returns a scripted outcome.
type recordingDialer struct {
	calls   int
	client  Client
	err     error
	lastOld Client
}

func (d *recordingDialer) dial(old Client) (Client, error) {
	d.calls++
	d.lastOld = old
	return d.client, d.err
}

// Entering the reconnect state must arm the first attempt, not just record it.
func TestReconnect_BeginArmsFirstAttempt(t *testing.T) {
	d := &recordingDialer{err: errors.New("refused")}
	m := Model{clientGen: 1}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, d.dial)

	updated, cmd := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	got := updated.(Model)

	if !got.linkOf(testDest).active {
		t.Fatal("reconnect not active")
	}
	if got.linkOf(testDest).attempt != 1 {
		t.Errorf("attempt = %d, want 1", got.linkOf(testDest).attempt)
	}
	if cmd == nil {
		t.Fatal("no timer armed; the reconnect would never fire")
	}
	if got.linkOf(testDest).nextAt.IsZero() {
		t.Error("nextAt not set; the banner has no countdown to show")
	}
}

// A second link loss while already reconnecting must not start a parallel loop.
func TestReconnect_SecondLinkLossDoesNotStackLoops(t *testing.T) {
	d := &recordingDialer{err: errors.New("refused")}
	m := Model{clientGen: 1}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, d.dial)

	updated, _ := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	m = updated.(Model)
	updated, cmd := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF again")})
	got := updated.(Model)

	if got.linkOf(testDest).attempt != 1 {
		t.Errorf("attempt = %d, want 1 — a second loop was armed", got.linkOf(testDest).attempt)
	}
	if cmd != nil {
		t.Error("a second timer was armed")
	}
}

// The tick for the current generation runs the dialer.
func TestReconnect_TickRunsDialer(t *testing.T) {
	d := &recordingDialer{err: errors.New("refused")}
	old := &failingClient{err: errors.New("dead")}
	m := Model{clientGen: 4, client: old, links: oneLink(reconnectState{active: true, attempt: 1, gen: 4})}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, d.dial)

	_, cmd := m.Update(redialTickMsg{gen: 4, dest: testDest, attempt: 1})
	if cmd == nil {
		t.Fatal("tick produced no dial command")
	}
	msg := cmd()

	if d.calls != 1 {
		t.Errorf("dialer called %d times, want 1", d.calls)
	}
	if d.lastOld != Client(old) {
		t.Error("the dead client was not handed to the dialer; it can never be closed")
	}
	res, ok := msg.(redialResultMsg)
	if !ok {
		t.Fatalf("msg is %T, want redialResultMsg", msg)
	}
	if res.gen != 4 {
		t.Errorf("result gen = %d, want 4", res.gen)
	}
}

// A tick for a superseded generation must not dial.
func TestReconnect_StaleTickDoesNotDial(t *testing.T) {
	d := &recordingDialer{err: errors.New("refused")}
	m := Model{clientGen: 9, links: oneLink(reconnectState{active: true, attempt: 1, gen: 9})}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, d.dial)

	_, cmd := m.Update(redialTickMsg{gen: 8, dest: testDest, attempt: 1})
	if cmd != nil {
		t.Fatal("a stale tick produced a dial command")
	}
	if d.calls != 0 {
		t.Errorf("dialer called %d times on a stale tick", d.calls)
	}
}

// Success swaps the client, bumps the generation, clears the state, and
// re-attaches.
func TestReconnect_SuccessSwapsClientAndBumpsGeneration(t *testing.T) {
	fresh := &failingClient{err: errors.New("unused")}
	m := Model{clientGen: 7, links: oneLink(reconnectState{active: true, attempt: 2, gen: 7})}
	m.asRemote("gpu01")

	updated, cmd := m.Update(redialResultMsg{gen: 7, dest: testDest, client: fresh})
	got := updated.(Model)

	if got.clientGen != 8 {
		t.Errorf("clientGen = %d, want 8", got.clientGen)
	}
	if got.linkOf(testDest).active {
		t.Error("reconnect still active after success")
	}
	if got.client != Client(fresh) {
		t.Error("client was not swapped")
	}
	if cmd == nil {
		t.Fatal("no command returned; expected re-attach plus a new listen loop")
	}
}

// A failed attempt schedules another and leaves the state active.
func TestReconnect_FailureSchedulesAnother(t *testing.T) {
	m := Model{clientGen: 2, links: oneLink(reconnectState{active: true, attempt: 1, gen: 2})}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("refused") })

	updated, cmd := m.Update(redialResultMsg{gen: 2, dest: testDest, err: errors.New("refused")})
	got := updated.(Model)

	if !got.linkOf(testDest).active {
		t.Error("reconnect ended on a failed attempt")
	}
	if got.linkOf(testDest).attempt != 2 {
		t.Errorf("attempt = %d, want 2", got.linkOf(testDest).attempt)
	}
	if got.linkOf(testDest).lastErr == nil || got.linkOf(testDest).lastErr.Error() != "refused" {
		t.Errorf("lastErr = %v, want the dial error for the banner", got.linkOf(testDest).lastErr)
	}
	if cmd == nil {
		t.Error("no retry scheduled")
	}
}

// A result addressed to a superseded generation is dropped, INCLUDING one that
// carries a live client: a slow first attempt completing after a fast second
// one would otherwise replace a working connection with one nobody reads.
func TestReconnect_StaleResultDroppedEvenWhenLive(t *testing.T) {
	live := &failingClient{err: errors.New("unused")}
	current := &failingClient{err: errors.New("current")}
	m := Model{clientGen: 9, client: current}
	m.asRemote("gpu01")

	updated, cmd := m.Update(redialResultMsg{gen: 8, dest: testDest, client: live})
	got := updated.(Model)

	if got.clientGen != 9 {
		t.Errorf("clientGen = %d, want 9 (unchanged)", got.clientGen)
	}
	if got.client != Client(current) {
		t.Error("a stale result replaced the live client")
	}
	if cmd != nil {
		t.Error("stale result produced a command")
	}
}

// A dialer that reports success with no client must be treated as a failure.
// Go's typed-nil trap makes this reachable in practice: a *ipc.Client nil
// returned into a Client interface is NOT == nil, so a naive check passes it
// through and the next Receive panics.
func TestReconnect_NilClientWithoutErrorIsAFailure(t *testing.T) {
	m := Model{clientGen: 3, links: oneLink(reconnectState{active: true, attempt: 1, gen: 3})}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, nil })

	updated, cmd := m.Update(redialResultMsg{gen: 3, dest: testDest, client: nil, err: nil})
	got := updated.(Model)

	if got.clientGen != 3 {
		t.Errorf("clientGen = %d, want 3 — a nil client was installed", got.clientGen)
	}
	if !got.linkOf(testDest).active {
		t.Error("reconnect ended after a nil client was returned")
	}
	if cmd == nil {
		t.Error("no retry scheduled after a nil client")
	}
}

// The reconnected destination must come out of finishReconnect marked ATTACHED.
// The flag gates the WindowSizeMsg attach sweep, so leaving it unset makes the
// next resize attach a second time — and the daemon replays the entire output
// buffer on every attach, so the cost is a doubled scrollback, not a redundant
// no-op. Invisible until you scroll up.
//
// The second case is the one the per-destination ledger added: a destination
// unreachable at LAUNCH never carried the flag at all, so a reconnect has to
// SET it rather than merely preserve it.
func TestReconnect_MarksTheDestAttached(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start map[string]bool
	}{
		{"attached at launch", attachedTo(testDest)},
		{"unreachable at launch, attached by the reconnect", map[string]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := &failingClient{err: errors.New("unused")}
			m := Model{clientGen: 1, attached: tc.start,
				links: oneLink(reconnectState{active: true, attempt: 1, gen: 1})}
			m.asRemote("gpu01")

			updated, _ := m.Update(redialResultMsg{gen: 1, dest: testDest, client: fresh})

			if got := updated.(Model); !got.attached[testDest] {
				t.Error("the reconnected destination is not marked attached; the next resize " +
					"will attach it again and double every pane's scrollback")
			}
		})
	}
}

// The generation must be bumped BEFORE the new listen loop is built, or that
// loop stamps its reports with the old number and its own link loss is
// discarded as stale — leaving a session that can never reconnect again.
func TestReconnect_NewListenLoopCarriesTheNewGeneration(t *testing.T) {
	fresh := &failingClient{err: errors.New("second death")}
	m := Model{clientGen: 7, attached: attachedTo(testDest), links: oneLink(reconnectState{active: true, attempt: 1, gen: 7})}
	m.asRemote("gpu01")

	updated, cmd := m.Update(redialResultMsg{gen: 7, dest: testDest, client: fresh})
	got := updated.(Model)
	if got.clientGen != 8 {
		t.Fatalf("clientGen = %d, want 8", got.clientGen)
	}
	if cmd == nil {
		t.Fatal("no command returned")
	}

	// Runs the cmd finishReconnect ACTUALLY built, and compares against a
	// literal. An earlier version called got.listenForMessages() afresh and
	// compared to got.clientGen — but listenForMessages is a value receiver that
	// stamps gen from m.clientGen at call time, so that compared a field to
	// itself and could never fail. Verified: building the cmd before the bump
	// left the whole suite passing.
	lost := findLinkLostMsg(t, cmd)
	if lost.gen != 8 {
		t.Errorf("the listen loop finishReconnect built stamped gen %d, want 8 — it was built "+
			"before the generation bump, so its own drop would be discarded as stale and the "+
			"session could never reconnect again", lost.gen)
	}
}

// findLinkLostMsg runs a (possibly batched) command and returns the linkLostMsg
// one of its branches produces. The client in these tests fails every Receive,
// so the listen branch yields one immediately.
func findLinkLostMsg(t *testing.T, cmd tea.Cmd) linkLostMsg {
	t.Helper()
	switch msg := cmd().(type) {
	case linkLostMsg:
		return msg
	case tea.BatchMsg:
		for _, c := range msg {
			if c == nil {
				continue
			}
			if lost, ok := c().(linkLostMsg); ok {
				return lost
			}
		}
	}
	t.Fatal("no linkLostMsg among the returned commands; the listen loop was not started")
	return linkLostMsg{}
}

// Input is dropped, not buffered, while the link is down: a keystroke replayed
// into a live agent session minutes later lands at a prompt that has moved on.
// Ctrl+Q must stay live — it is the only way out of a host that never returns.
//
// Coverage note: the table below asserts cmd == nil, and on this fixture (no
// tabs, no client) several of those messages would return nil through NORMAL
// handling too — so those rows would still pass with freezeInput deleted. The
// ctrl+q subtest is genuinely load-bearing, and
// TestReconnect_InputResumesWhenNotReconnecting is the control that proves the
// freeze is what suppresses input, by running the same key with the link up and
// down and requiring the outcomes to differ.
func TestReconnect_SwallowsInputExceptQuit(t *testing.T) {
	newM := func() Model {
		m := Model{cfg: config.Default(), links: oneLink(reconnectState{active: true, attempt: 3})}
		m.asRemote("gpu01")
		return m
	}

	frozen := []struct {
		name string
		msg  tea.Msg
	}{
		{"printable key", tea.KeyPressMsg{Code: 'a', Text: "a"}},
		{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"paste", tea.PasteMsg{Content: "rm -rf /"}},
		{"mouse click", tea.MouseClickMsg{}},
		{"mouse wheel", tea.MouseWheelMsg{}},
		{"mouse motion", tea.MouseMotionMsg{}},
		{"mouse release", tea.MouseReleaseMsg{}},
	}
	for _, tt := range frozen {
		t.Run(tt.name, func(t *testing.T) {
			if _, cmd := newM().Update(tt.msg); cmd != nil {
				t.Errorf("%s produced a command while the link was down", tt.name)
			}
		})
	}

	t.Run("ctrl+q still quits", func(t *testing.T) {
		m := newM()
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
		if cmd == nil {
			t.Fatal("ctrl+q produced no command during reconnect")
		}
		if !isQuit(cmd()) {
			t.Error("ctrl+q did not quit during reconnect")
		}
	})
}

// The freeze must lift once the link is back, or the session is unusable.
//
// Asserted on an observable the freeze would suppress, not on "it did not
// panic": an earlier version of this test called Update and discarded both
// returns, which could only ever have failed on a crash and would have passed
// with freezeInput deleted.
func TestReconnect_InputResumesWhenNotReconnecting(t *testing.T) {
	newM := func(active bool) Model {
		m := Model{cfg: config.Default(), width: 80, height: 24}
		m.asRemote("gpu01")
		m.linkFor(testDest).active = active
		return m
	}

	// F1 opens the About dialog — a Model-visible effect needing no pane, no
	// client and no daemon, so the two arms differ only by the freeze.
	key := tea.KeyPressMsg{Code: tea.KeyF1}

	frozen, _ := newM(true).Update(key)
	if got := frozen.(Model); got.dialog != dialogNone {
		t.Errorf("a keypress opened dialog %v while the link was down", got.dialog)
	}

	live, _ := newM(false).Update(key)
	if got := live.(Model); got.dialog != dialogAbout {
		t.Errorf("the same keypress left dialog = %v with the link up; the freeze did not "+
			"lift, or this test no longer observes anything", got.dialog)
	}
}

func TestRenderReconnectBanner_NamesHostAndAttempt(t *testing.T) {
	m := Model{links: oneLink(reconnectState{
		active:  true,
		attempt: 4,
		lastErr: errors.New("connection refused"),
	})}
	m.asRemote("gpu01")

	out := stripANSI(m.renderReconnectBanner(80))
	for _, want := range []string{"gpu01", "4", "connection refused", "ctrl+q"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q\ngot: %s", want, out)
		}
	}
}

// The banner must never wrap the frame — it is drawn as an overlay, so a line
// wider than the terminal corrupts the row below it rather than clipping.
func TestRenderReconnectBanner_FitsWidth(t *testing.T) {
	host := strings.Repeat("host", 30)
	m := Model{links: map[string]*reconnectState{host: {
		active:  true,
		attempt: 12,
		lastErr: errors.New(strings.Repeat("very long ssh diagnostic ", 20)),
	}}}
	m.asRemote(host)

	for _, w := range []int{20, 40, 80, 200} {
		for _, line := range strings.Split(m.renderReconnectBanner(w), "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d: line measured %d\n%q", w, got, line)
			}
		}
	}
}

// The escape hatch outranks everything, at EVERY width the TUI will render at.
//
// minTermWidth is 40, so 40 is reachable in practice, not a defensive edge —
// and a single truncated string cut the hint to "ctr…" there.
func TestRenderReconnectBanner_KeepsExitHintAtEveryWidth(t *testing.T) {
	m := Model{links: oneLink(reconnectState{
		active:  true,
		attempt: 2,
		lastErr: errors.New(strings.Repeat("diagnostic ", 30)),
	})}
	m.asRemote("gpu01")

	for _, w := range []int{40, 50, 60, 80, 120, 200} {
		out := stripANSI(m.renderReconnectBanner(w))
		if !strings.Contains(out, "ctrl+q") {
			t.Errorf("width %d: exit hint missing or truncated\ngot: %s", w, out)
		}
	}
}

// A REAL ssh error runs past 50 characters. An all-or-nothing fit check hid it
// at every width below ~110, including 80 — so the diagnostic captured in batch
// mode was never actually seen. It must be truncated to fit, not dropped.
func TestRenderReconnectBanner_ShowsRealisticSSHErrorAt80(t *testing.T) {
	m := Model{links: oneLink(reconnectState{
		active:  true,
		attempt: 4,
		lastErr: errors.New("ssh: connect to host gpu01 port 22: Connection refused"),
	})}
	m.asRemote("gpu01")

	out := stripANSI(m.renderReconnectBanner(80))
	if !strings.Contains(out, "ssh:") {
		t.Errorf("a realistic ssh error is absent at 80 columns; the whole point of\n"+
			"capturing it in batch mode is that the user reads it\ngot: %s", out)
	}
	if !strings.Contains(out, "ctrl+q") {
		t.Errorf("the detail crowded out the exit hint\ngot: %s", out)
	}
}

// Below minBannerDetail the diagnostic is dropped rather than shown as noise.
func TestRenderReconnectBanner_DropsUselesslyShortDetail(t *testing.T) {
	m := Model{links: oneLink(reconnectState{
		active:  true,
		attempt: 4,
		lastErr: errors.New("Connection refused by the remote host"),
	})}
	m.asRemote("gpu01")

	// 50 leaves under minBannerDetail once the core is placed.
	out := stripANSI(m.renderReconnectBanner(50))
	if strings.Contains(out, "Conn…") || strings.Contains(out, "C…") {
		t.Errorf("a uselessly truncated detail was rendered\ngot: %s", out)
	}
	if !strings.Contains(out, "ctrl+q") {
		t.Errorf("exit hint missing\ngot: %s", out)
	}
}

// A multi-line ssh diagnostic must not turn a one-row overlay into three.
func TestRenderReconnectBanner_MultilineErrorStaysOneLine(t *testing.T) {
	m := Model{links: oneLink(reconnectState{
		active:  true,
		attempt: 1,
		lastErr: errors.New("Permission denied (publickey).\nssh: connect failed\nmore"),
	})}
	m.asRemote("gpu01")

	if n := strings.Count(m.renderReconnectBanner(120), "\n"); n != 0 {
		t.Errorf("banner spans %d extra lines; the overlay would eat the tab bar and the row below", n)
	}
}

// Nothing is drawn when no reconnect is in flight.
func TestRenderReconnectBanner_EmptyWhenInactive(t *testing.T) {
	m := Model{}
	m.asRemote("gpu01")
	if got := m.renderReconnectBanner(80); got != "" {
		t.Errorf("banner rendered while inactive: %q", got)
	}
}

// newReconnectTestModel builds a Model with one tab holding n layout panes plus
// an overlay pane. The overlay lives OUTSIDE the layout tree, so it is the case
// a tree walk silently misses.
func newReconnectTestModel(t *testing.T, n int) *Model {
	t.Helper()
	tab := NewTabModel("tab-1", "T")
	prevID := ""
	for i := 0; i < n; i++ {
		p := NewPaneModel(fmt.Sprintf("p%d", i), 4096)
		t.Cleanup(p.Dispose)
		if tab.Root == nil {
			tab.Root = NewLeaf(p)
		} else {
			ph := tab.Root.SplitLeaf(prevID, SplitHorizontal)
			ph.Pane = p
		}
		prevID = p.ID
	}
	tab.ActivePane = "p0"

	ov := NewPaneModel("overlay", 4096)
	t.Cleanup(ov.Dispose)
	tab.overlayPane = ov

	tab.Resize(80, 24)

	m := &Model{projects: oneProject(tab), width: 80, height: 24, cfg: config.Default()}
	m.asRemote("gpu01")
	return m
}

// reconnectTestPanes returns every pane the reset must touch, overlay included.
func reconnectTestPanes(m *Model) []*PaneModel {
	var out []*PaneModel
	for _, tab := range m.curTabs() {
		out = append(out, tab.Leaves()...)
		if tab.overlayPane != nil {
			out = append(out, tab.overlayPane)
		}
	}
	return out
}

// One reconnect must not double a pane's scrollback. handleAttach replays the
// whole output buffer as ghost chunks on EVERY attach and handlePaneOutput
// appends unconditionally, so without this the next reconnect triples it.
func TestReconnect_ResetsScrollbackBeforeReplay(t *testing.T) {
	m := newReconnectTestModel(t, 2)
	panes := reconnectTestPanes(m)
	if len(panes) != 3 {
		t.Fatalf("fixture built %d panes, want 3 (2 layout + 1 overlay)", len(panes))
	}
	for _, p := range panes {
		p.AppendOutput([]byte("PREDROP line one\r\nPREDROP line two\r\n"))
		p.scrollBack = 5
		p.liveOutputSeen = true
	}
	if panes[0].rawBuf.Len() == 0 {
		t.Fatal("fixture wrote no output")
	}
	m.selection = &Selection{PaneID: "p0"}

	armAndReplayAll(t, m, "replayed after reattach")

	// Asserted as "the pre-drop content is gone", not "the buffer is empty":
	// the reset is now consumed BY the replay, so afterwards the buffer holds
	// the replayed chunk. Surviving pre-drop content is precisely the doubling.
	for _, p := range panes {
		if got := string(p.rawBuf.Bytes()); strings.Contains(got, "PREDROP") {
			t.Errorf("pane %s: pre-drop content survived the replay, so scrollback doubled:\n%q", p.ID, got)
		}
		if got := string(p.rawBuf.Bytes()); !strings.Contains(got, "replayed after reattach") {
			t.Errorf("pane %s: the replayed chunk did not land:\n%q", p.ID, got)
		}
		if p.scrollBack != 0 {
			t.Errorf("pane %s: scrollBack = %d, want 0", p.ID, p.scrollBack)
		}
		if p.liveOutputSeen {
			t.Errorf("pane %s: liveOutputSeen survived; the ghost/live transition will not re-fire", p.ID)
		}
	}
	if m.selection != nil {
		t.Error("selection survived; it anchors to coordinates in content that was just discarded")
	}
}

// Terminal panes are reset too. The existing rule that terminal panes skip
// ResetVT protects RESTORED content from a respawned shell's init output —
// nothing respawns here and the content is about to be re-sent, so applying
// that rule would be the bug.
func TestReconnect_ResetsTerminalPanesAlso(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	p := m.curTabs()[0].Leaves()[0]
	p.Type = "terminal"
	p.AppendOutput([]byte("PREDROP shell output\r\n"))

	armAndReplayAll(t, m, "replayed after reattach")

	if got := string(p.rawBuf.Bytes()); strings.Contains(got, "PREDROP") {
		t.Errorf("terminal pane not reset, pre-drop content survived:\n%q", got)
	}
}

// The overlay pane is a live daemon pane that gets replayed like any other, but
// it sits outside the layout tree — so a Leaves()-only walk misses it and its
// scrollback doubles while every other pane is fine.
func TestReconnect_ResetsOverlayPane(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	ov := m.curTabs()[0].overlayPane
	ov.AppendOutput([]byte("PREDROP lazygit screen\r\n"))
	if ov.rawBuf.Len() == 0 {
		t.Fatal("fixture wrote nothing to the overlay pane")
	}

	armAndReplayAll(t, m, "replayed after reattach")

	// Overlay panes are served by their own branch in handlePaneOutput that
	// returns early, so this also pins that the branch consumes the armed reset.
	if got := string(ov.rawBuf.Bytes()); strings.Contains(got, "PREDROP") {
		t.Errorf("overlay pane not reset, pre-drop content survived:\n%q", got)
	}
}

// Panes in BACKGROUND tabs are replayed on the same attach, so they need the
// same reset — a walk over the active tab only would leave them doubling.
func TestReconnect_ResetsBackgroundTabs(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	bg := NewTabModel("tab-2", "BG")
	p := NewPaneModel("bg0", 4096)
	t.Cleanup(p.Dispose)
	bg.Root = NewLeaf(p)
	bg.ActivePane = "bg0"
	bg.Resize(80, 24)
	m.appendTab(bg)

	p.AppendOutput([]byte("PREDROP background output\r\n"))
	if p.rawBuf.Len() == 0 {
		t.Fatal("fixture wrote nothing to the background pane")
	}

	armAndReplayAll(t, m, "replayed after reattach")

	if got := string(p.rawBuf.Bytes()); strings.Contains(got, "PREDROP") {
		t.Errorf("background-tab pane not reset, pre-drop content survived:\n%q", got)
	}
}

// The reset must be reached BY the reconnect, not merely exist. This drives the
// real path: a successful redial result arriving in Update.
func TestReconnect_SuccessPathResetsPanes(t *testing.T) {
	m := newReconnectTestModel(t, 2)
	m.clientGen = 3
	m.attached = attachedTo(testDest)
	*m.linkFor(testDest) = reconnectState{active: true, attempt: 1, gen: m.clientGen}

	panes := reconnectTestPanes(m)
	for _, p := range panes {
		p.AppendOutput([]byte("PREDROP pre-reconnect content\r\n"))
	}
	m.selection = &Selection{PaneID: "p0"}

	updated, cmd := m.Update(redialResultMsg{gen: 3, dest: testDest, client: &failingClient{err: errors.New("unused")}})
	got := updated.(Model)

	if cmd == nil {
		t.Fatal("no attach/listen command returned")
	}
	// The reconnect ARMS the reset; the daemon's replay consumes it. Both halves
	// are asserted, because arming without a replay ever landing was the bug that
	// blanked every pane with no ghost buffer.
	for _, p := range panes {
		if !p.reattachReset {
			t.Errorf("pane %s was not armed by the reconnect", p.ID)
		}
		if got := string(p.rawBuf.Bytes()); !strings.Contains(got, "PREDROP") {
			t.Errorf("pane %s was wiped at arm time rather than on the replay", p.ID)
		}
	}
	if got.selection != nil {
		t.Error("selection survived the reconnect")
	}

	for _, p := range panes {
		deliverGhost(t, &got, p.ID, "replayed after reattach")
	}
	for _, p := range panes {
		if s := string(p.rawBuf.Bytes()); strings.Contains(s, "PREDROP") {
			t.Errorf("pane %s: pre-drop content survived its replay, so scrollback doubled:\n%q", p.ID, s)
		}
	}
}

// Replayed SubagentStart events must not wedge the spinner. applyWorkTransition
// has no dedup, so a replay re-increments counters that already reflect it.
func TestReconnect_ResetsWorkCounters(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	p := m.curTabs()[0].Leaves()[0]

	m.applyWorkTransition(p.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(p.ID, "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore", "coalesced": "3"})
	if !p.working {
		t.Fatal("fixture did not put the pane into a working state")
	}
	if got := p.subagents["Explore"]; got != 3 {
		t.Fatalf("fixture subagents[\"Explore\"] = %d, want 3 (coalesced burst)", got)
	}

	m.resetWorkStateForReattach(testDest)

	if p.working {
		t.Error("pane still working after reset")
	}
	if len(p.subagents) != 0 {
		t.Errorf("subagents = %v, want empty — a replayed start would stack on top of this", p.subagents)
	}
	if p.turnActive {
		t.Error("turnActive survived the reset")
	}
}

// The unseen mark reports unread COMPLETED work, not a live turn. It is the only
// signal that a background pane finished something while the link was down, so
// clearing it would lose exactly the information the user reconnected to see.
func TestReconnect_KeepsUnseenMark(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	p := m.curTabs()[0].Leaves()[0]
	p.unseen = true

	m.resetWorkStateForReattach(testDest)

	if !p.unseen {
		t.Error("unseen mark cleared by reconnect; it reports unread work, not a live turn")
	}
}

// pinnedAttention is a user-set pin with no connection to execution state.
func TestReconnect_KeepsPinnedAttention(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	p := m.curTabs()[0].Leaves()[0]
	p.pinnedAttention = true

	m.resetWorkStateForReattach(testDest)

	if !p.pinnedAttention {
		t.Error("the user's attention pin was cleared by a reconnect")
	}
}

// workTickRunning must NOT be cleared here. The spinner loop is self-stopping:
// the in-flight tick observes !anyPaneWorking() and clears the flag itself.
// Clearing it while that tick is still scheduled lets the next hook event start
// a SECOND loop, and the spinner then animates at double rate forever.
func TestReconnect_DoesNotClearWorkTickRunning(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.workTickRunning = true

	m.resetWorkStateForReattach(testDest)

	if !m.workTickRunning {
		t.Error("workTickRunning was cleared; a later hook event will start a second spinner loop " +
			"alongside the tick still in flight")
	}
}

// Background tabs are replayed on the same attach, so their counters need the
// same reset.
func TestReconnect_ResetsWorkStateInBackgroundTabs(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	bg := NewTabModel("tab-2", "BG")
	p := NewPaneModel("bg0", 4096)
	t.Cleanup(p.Dispose)
	bg.Root = NewLeaf(p)
	bg.ActivePane = "bg0"
	bg.Resize(80, 24)
	m.appendTab(bg)

	m.applyWorkTransition("bg0", "hook.claude.UserPromptSubmit", nil)
	if !p.working {
		t.Fatal("fixture did not start a turn on the background pane")
	}

	m.resetWorkStateForReattach(testDest)

	if p.working || p.turnActive {
		t.Error("background-tab work state survived the reset")
	}
}

// The reset must be reached BY the reconnect, not merely exist.
//
// The unseen assertion is made on the NON-focused pane deliberately.
// applyWorkTransition only ever marks a pane that is not the focused pane of
// the active tab, and Update's ackFocusedPane clears the focused one at entry —
// so an unseen mark on the focused pane is a state that cannot arise, and
// asserting it survives would be testing against the ack rather than the
// reconnect. A background pane finishing work while the link was down is both
// the realistic case and the one the user reconnected to find out about.
func TestReconnect_SuccessPathResetsWorkState(t *testing.T) {
	m := newReconnectTestModel(t, 2)
	m.clientGen = 5
	m.attached = attachedTo(testDest)
	*m.linkFor(testDest) = reconnectState{active: true, attempt: 1, gen: m.clientGen}
	focused := m.curTabs()[0].Leaves()[0]
	other := m.curTabs()[0].Leaves()[1]
	if focused.ID != m.curTabs()[0].ActivePane {
		focused, other = other, focused
	}

	m.applyWorkTransition(focused.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(focused.ID, "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore", "coalesced": "2"})
	other.unseen = true

	m.Update(redialResultMsg{gen: 5, dest: testDest, client: &failingClient{err: errors.New("unused")}})

	if focused.working {
		t.Error("pane still working after a successful reconnect")
	}
	if len(focused.subagents) != 0 {
		t.Errorf("subagents = %v after reconnect, want empty", focused.subagents)
	}
	if !other.unseen {
		t.Error("the reconnect cleared a background pane's unseen mark — the only signal " +
			"that it finished something while the link was down")
	}
}

// Exactly one listen loop may drive the Model after a swap.
//
// The old loop is still parked in Receive on the dead client when the
// replacement goes live; when it finally errors, its linkLostMsg carries the
// old generation. This drives the full sequence rather than injecting a stale
// generation directly, so it also covers the bump in finishReconnect.
func TestReconnect_OldListenLoopCannotStartASecondReconnect(t *testing.T) {
	dials := 0
	m := Model{clientGen: 1, attached: attachedTo(testDest)}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) {
		dials++
		return &failingClient{err: errors.New("unused")}, nil
	})

	// Link drops, then a redial succeeds — the generation moves to 2.
	updated, _ := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	m = updated.(Model)
	updated, _ = m.Update(redialResultMsg{gen: m.linkOf(testDest).gen, dest: testDest, client: &failingClient{err: errors.New("live")}})
	m = updated.(Model)
	if m.clientGen != 2 {
		t.Fatalf("clientGen = %d after a successful reconnect, want 2", m.clientGen)
	}
	if m.linkFor(testDest).active {
		t.Fatal("reconnect still active after success")
	}

	// Now the DEAD client's loop finally errors, reporting the old generation.
	updated, cmd := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	got := updated.(Model)

	if got.linkOf(testDest).active {
		t.Error("a stale listen loop restarted the reconnect")
	}
	if cmd != nil {
		t.Error("stale link loss produced a command")
	}
	if got.clientGen != 2 {
		t.Errorf("clientGen = %d, want 2 (untouched by the stale report)", got.clientGen)
	}
}

// A genuine second drop on the CURRENT generation must still reconnect. The
// stale-report guard must reject old generations without deafening the Model to
// real ones — a session that heals once and then dies silently is worse than
// one that never healed.
func TestReconnect_SecondGenuineDropStillReconnects(t *testing.T) {
	m := Model{clientGen: 1, attached: attachedTo(testDest)}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) {
		return &failingClient{err: errors.New("unused")}, nil
	})

	updated, _ := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	m = updated.(Model)
	updated, _ = m.Update(redialResultMsg{gen: m.linkOf(testDest).gen, dest: testDest, client: &failingClient{err: errors.New("live")}})
	m = updated.(Model)

	// The NEW client's own loop reports a drop, stamped with the new generation.
	updated, cmd := m.Update(linkLostMsg{gen: m.clientGen, dest: testDest, err: errors.New("EOF again")})
	got := updated.(Model)

	if !got.linkOf(testDest).active {
		t.Error("a genuine second drop did not start a reconnect")
	}
	if cmd == nil {
		t.Error("no retry armed for the second drop")
	}
}

// sawFirstState must survive a reconnect, so the once-per-launch update notice
// cannot reopen when the daemon re-broadcasts workspace_state on reattach.
//
// Belt-and-braces today: maybeShowUpdateNotice returns early in remote mode
// (its update info describes the REMOTE daemon while accepting applies a LOCAL
// staged update), and this Model is remote because its ACTIVE PROJECT carries a
// dest — so the two conditions coincide and reconnecting here can never reach
// the notice at all. This pins the invariant for RD-027, which makes update
// controls remote-aware and would otherwise reintroduce it silently.
func TestReconnect_DoesNotReopenUpdateNotice(t *testing.T) {
	m := Model{clientGen: 1, attached: attachedTo(testDest), sawFirstState: true,
		links: oneLink(reconnectState{active: true, attempt: 1, gen: 1})}
	m.asRemote(testDest)

	updated, _ := m.Update(redialResultMsg{gen: 1, dest: testDest, client: &failingClient{err: errors.New("unused")}})
	got := updated.(Model)

	requireReconnectRan(t, got)
	if !got.sawFirstState {
		t.Error("sawFirstState was cleared by the reconnect; the update notice would reopen " +
			"on the reattach broadcast once RD-027 makes it remote-aware")
	}
}

// requireReconnectRan fails unless finishReconnect actually ran for testDest.
//
// It exists because every "the reconnect leaves X alone" test asserts that
// something did NOT change, and such a test passes for two entirely different
// reasons: the reconnect preserved X, or the reconnect never happened. The
// second is not hypothetical — moving the generation onto reconnectState.gen
// left two of these fixtures stamping a gen the gate no longer matched, so the
// result was dropped as stale and both assertions held because nothing had
// touched them. Nothing in that diff pointed at the tests.
//
// active is the right probe: only finishReconnect clears it on a successful
// result, and it is cleared for THIS destination, so a result routed to another
// one cannot satisfy it either.
func requireReconnectRan(t *testing.T, m Model) {
	t.Helper()
	if m.linkOf(testDest).active {
		t.Fatalf("the reconnect never ran — the result was dropped before finishReconnect, "+
			"so every assertion below holds vacuously (link state: %+v)", m.linkOf(testDest))
	}
}

// The reconnect must not resurrect a dialog or leave one stranded. Dialog state
// is client-independent, so it is left exactly as the user had it.
func TestReconnect_LeavesDialogStateAlone(t *testing.T) {
	m := Model{clientGen: 1, attached: attachedTo(testDest), dialog: dialogAbout, dialogCursor: 3,
		links: oneLink(reconnectState{active: true, attempt: 1, gen: 1})}
	m.asRemote(testDest)

	updated, _ := m.Update(redialResultMsg{gen: 1, dest: testDest, client: &failingClient{err: errors.New("unused")}})
	got := updated.(Model)

	requireReconnectRan(t, got)
	if got.dialog != dialogAbout {
		t.Errorf("dialog = %v after reconnect, want dialogAbout (unchanged)", got.dialog)
	}
	if got.dialogCursor != 3 {
		t.Errorf("dialogCursor = %d, want 3 (unchanged)", got.dialogCursor)
	}
}

// Ghost re-dim on reconnect is ACCEPTED behaviour, recorded rather than fixed
// (RD-016). resetForReattach clears liveOutputSeen so the replay is treated as
// ghost output exactly as on a first attach, which means panes briefly show the
// muted "restored" border again. Pinning it means a future change to the
// ghost/live latch has to make this decision deliberately.
func TestReconnect_GhostDimIsAcceptedNotAvoided(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	p := m.curTabs()[0].Leaves()[0]
	p.liveOutputSeen = true

	armAndReplayAll(t, m, "replayed after reattach")

	if p.liveOutputSeen {
		t.Error("liveOutputSeen survived; the ghost→live transition and its settle repaints " +
			"would not re-fire for the replayed content")
	}
}

// While the backoff waits, the banner must say the host is unreachable and count
// down — the substance of "I would rather see that it is still trying" than a
// line that looks identical whether the TUI is working or wedged.
func TestRenderReconnectBanner_WaitingPhase_NamesUnreachableAndCountsDown(t *testing.T) {
	m := Model{links: oneLink(reconnectState{
		active:  true,
		attempt: 6,
		nextAt:  time.Now().Add(8 * time.Second),
		lastErr: errors.New("connection timed out"),
	})}
	m.asRemote("gpu01")

	out := stripANSI(m.renderReconnectBanner(120))
	for _, want := range []string{"gpu01", "unreachable", "retry in", "s", "attempt 6", "ctrl+q"} {
		if !strings.Contains(out, want) {
			t.Errorf("waiting-phase banner missing %q\ngot: %s", want, out)
		}
	}
}

// Once the tick fires and a dial is in flight, the wording changes. Against a
// down host that state lasts as long as the transport's ConnectTimeout, so it
// must be distinguishable from waiting rather than sharing one label.
func TestRenderReconnectBanner_ConnectingPhase_DiffersFromWaiting(t *testing.T) {
	base := reconnectState{active: true, attempt: 3, lastErr: errors.New("timed out")}

	// oneLink copies its argument, so the two models hold independent states —
	// the value semantics the old singleton field gave for free.
	waiting := Model{links: oneLink(base)}
	waiting.linkFor(testDest).nextAt = time.Now().Add(5 * time.Second)
	waiting.asRemote("gpu01")

	connecting := Model{links: oneLink(base)} // nextAt zero = in the past
	connecting.asRemote("gpu01")

	w := stripANSI(waiting.renderReconnectBanner(120))
	c := stripANSI(connecting.renderReconnectBanner(120))

	if w == c {
		t.Fatalf("both phases render identically:\n%s", w)
	}
	if !strings.Contains(c, "Connecting") {
		t.Errorf("in-flight phase does not say it is connecting\ngot: %s", c)
	}
	if strings.Contains(c, "retry in") {
		t.Errorf("in-flight phase shows a countdown it is not waiting on\ngot: %s", c)
	}
}

// A sub-second remainder must read as "1s", never "0s" — a countdown that sits
// on zero looks stuck, which is the impression the wording exists to avoid.
func TestRenderReconnectBanner_CountdownNeverShowsZero(t *testing.T) {
	for _, remain := range []time.Duration{
		1 * time.Millisecond, 200 * time.Millisecond, 999 * time.Millisecond,
	} {
		m := Model{links: oneLink(reconnectState{active: true, attempt: 1, nextAt: time.Now().Add(remain)})}
		m.asRemote("gpu01")
		out := stripANSI(m.renderReconnectBanner(120))
		if strings.Contains(out, "in 0s") {
			t.Errorf("remain=%v rendered a zero countdown\ngot: %s", remain, out)
		}
	}
}

// Both phases keep the exit hint at the narrowest width the TUI will render at.
func TestRenderReconnectBanner_BothPhasesKeepExitHintAtMinWidth(t *testing.T) {
	const host = "some-rather-long-hostname.example.internal"
	for _, wait := range []time.Duration{0, 9 * time.Second} {
		m := Model{links: map[string]*reconnectState{host: {
			active:  true,
			attempt: 12,
			lastErr: errors.New(strings.Repeat("diagnostic ", 20)),
		}}}
		if wait > 0 {
			m.linkFor(host).nextAt = time.Now().Add(wait)
		}
		m.asRemote(host)

		for _, w := range []int{40, 60, 80} {
			out := stripANSI(m.renderReconnectBanner(w))
			if !strings.Contains(out, "ctrl+q") {
				t.Errorf("wait=%v width=%d: exit hint missing\ngot: %s", wait, w, out)
			}
		}
	}
}

// Sustained failure must slow the retry RATE, not just cap it.
//
// Every attempt is a full ssh authentication, and there is a real case where
// authentication can never succeed while the link is fine: the startup dial runs
// non-batch and may prompt for a key passphrase, every reconnect runs batch and
// cannot. On a flat 30s cap that is ~120 failed authentications an hour from the
// operator's own address, which a default fail2ban sshd jail bans — locking the
// owner out of their own host overnight. This pins the decay that reduces it.
func TestReconnectDelay_SustainedFailureSlowsTheRate(t *testing.T) {
	// Worst case for the rate is minimum jitter throughout.
	const jitter = 0.0
	attemptsInFirstHour := 0
	var elapsed time.Duration
	for a := 1; elapsed < time.Hour; a++ {
		elapsed += reconnectDelay(a, jitter)
		attemptsInFirstHour++
	}

	// ~33 at worst-case jitter, against ~120 on the old flat 30s cap. The bound
	// is set from the design rather than from the measurement: anything near the
	// old figure means the decay stopped working.
	if attemptsInFirstHour > 40 {
		t.Errorf("%d attempts in the first hour of sustained failure — the decay is not "+
			"bounding the rate (a flat 30s cap gives ~120)", attemptsInFirstHour)
	}

	// The load-bearing property, and the honest one. The early burst still puts
	// ~11 attempts in the first ten minutes, which a strict jail can act on —
	// that is stated in reconnectSlowMaxDelay's comment and is not fixable
	// without classifying the failure as permanent. What IS guaranteed is the
	// STEADY state: once the ladder reaches its plateau, the spacing keeps a
	// rolling ten-minute window under the usual 5-failure threshold.
	const jailWindow = 10 * time.Minute
	const jailThreshold = 5
	plateau := reconnectDelay(60, jitter) // well past the decay boundary
	if perWindow := int(jailWindow / plateau); perWindow >= jailThreshold {
		t.Errorf("steady-state spacing %v allows %d attempts per %v, at or above the %d-failure "+
			"threshold a default jail acts on", plateau, perWindow, jailWindow, jailThreshold)
	}

	// Guard the other direction: the early attempts must stay quick, or a
	// transient blip takes minutes to heal when it should take under a second.
	if got := reconnectDelay(1, jitter); got > time.Second {
		t.Errorf("first attempt = %v, too slow for a transient drop", got)
	}
}

// The decay boundary is where the ceiling changes, so pin it directly rather
// than inferring it from the curve.
func TestReconnectDelay_DecayBoundary(t *testing.T) {
	// At and below the boundary the fast cap applies.
	if got := reconnectDelay(reconnectDecayAfter, 1); got > reconnectMaxDelay {
		t.Errorf("attempt %d = %v, want <= the fast cap %v", reconnectDecayAfter, got, reconnectMaxDelay)
	}
	// Far past it the slow plateau applies.
	if got := reconnectDelay(reconnectDecayAfter+40, 1); got != reconnectSlowMaxDelay {
		t.Errorf("attempt %d = %v, want the slow plateau %v", reconnectDecayAfter+40, got, reconnectSlowMaxDelay)
	}
}

// THE CRITICAL REGRESSION, and the P1 that followed the first fix.
//
// handleAttach replays only plugins with ghost_buffer = true; the rest get
// nothing and fall through to redrawKick. Resetting such a pane destroys the
// only content it has, and if nothing then repaints it the result is a blank
// rectangle in front of a live process — indefinitely, in a background tab.
// That hit opencode, lazygit, k9s and lazysql.
//
// redrawKick now covers the keyless case with a resize rather than returning
// silently, so the blank window is bounded in practice. This test does not rely
// on that: the reset must not happen regardless of whether a later repaint
// would have papered over it.
//
// The first fix predicted the daemon's choice from the Model's plugin registry.
// That registry is loaded from THIS machine's config.PluginsDir(), while
// handleAttach decides from the DAEMON's — different machines in remote mode, and
// even locally the TUI reloads its own registry when a plugin TOML is saved,
// ahead of the daemon. A mismatch corrupts the reattach in both directions.
//
// So the reset is now armed and consumed by the daemon's ACTUAL replay. No
// registry, no prediction, no agreement needed. These tests drive that contract:
// a pane that receives a replay is reset exactly once before the replay lands; a
// pane that receives none keeps everything it had.
func TestReconnect_ResetIsConsumedByTheReplayNotPredicted(t *testing.T) {
	t.Run("a replayed pane is reset before the chunk lands", func(t *testing.T) {
		m := newReconnectTestModel(t, 1)
		p := m.curTabs()[0].Leaves()[0]
		p.AppendOutput([]byte("stale content from before the drop\r\n"))
		if p.rawBuf.Len() == 0 {
			t.Fatal("fixture wrote nothing")
		}

		m.armReattachReset(testDest)
		if p.rawBuf.Len() == 0 {
			t.Error("arming alone wiped the pane — the reset must wait for the replay, " +
				"or a pane that never gets one is left blank")
		}

		deliverGhost(t, m, p.ID, "replayed\r\n")

		if p.reattachReset {
			t.Error("the armed flag survived its replay; a later chunk would reset again mid-replay")
		}
		if got := string(p.rawBuf.Bytes()); strings.Contains(got, "stale content") {
			t.Errorf("the pre-drop content survived the replay, so scrollback doubled:\n%q", got)
		}
		if got := string(p.rawBuf.Bytes()); !strings.Contains(got, "replayed") {
			t.Errorf("the replayed chunk did not land:\n%q", got)
		}
	})

	t.Run("a pane with no replay keeps its content", func(t *testing.T) {
		m := newReconnectTestModel(t, 2)
		replayed, silent := m.curTabs()[0].Leaves()[0], m.curTabs()[0].Leaves()[1]
		for _, p := range []*PaneModel{replayed, silent} {
			p.AppendOutput([]byte("content that predates the reconnect\r\n"))
		}

		m.armReattachReset(testDest)
		deliverGhost(t, m, replayed.ID, "only this pane is replayed\r\n")

		if silent.rawBuf.Len() == 0 {
			t.Error("a pane the daemon never replayed was wiped — nothing will repaint it, " +
				"so it renders blank in front of a live process")
		}
		if !silent.reattachReset {
			t.Error("the un-replayed pane's flag was consumed by another pane's chunk")
		}
	})

	t.Run("only a ghost chunk consumes the reset, not live output", func(t *testing.T) {
		m := newReconnectTestModel(t, 1)
		p := m.curTabs()[0].Leaves()[0]
		p.AppendOutput([]byte("before\r\n"))

		m.armReattachReset(testDest)
		// Live output is not a replay and must not be mistaken for one.
		updated, _ := m.Update(PaneOutputMsg{PaneID: p.ID, Data: []byte("live\r\n")})
		m = func() *Model { g := updated.(Model); return &g }()

		if !p.reattachReset {
			t.Error("live output consumed the armed reset; the real replay would then " +
				"append onto content it should have replaced")
		}
	})
}

// Every rung of the banner ladder must keep the exit hint, in both phases. The
// width-sampled tests cover today's rungs; this pins the invariant itself so a
// rung added later without ctrl+q fails immediately rather than at some width
// nobody sampled.
func TestBannerCandidates_EveryRungKeepsTheExitHint(t *testing.T) {
	for _, wait := range []time.Duration{0, 9 * time.Second} {
		m := Model{links: oneLink(reconnectState{active: true, attempt: 3})}
		if wait > 0 {
			m.linkFor(testDest).nextAt = time.Now().Add(wait)
		}
		m.asRemote("gpu01")

		got := m.bannerCandidates()
		if len(got) == 0 {
			t.Fatalf("wait=%v: no candidates; renderReconnectBanner would have nothing to fall back to", wait)
		}
		for i, c := range got {
			if !strings.Contains(c, "ctrl+q") {
				t.Errorf("wait=%v rung %d (%q) has no exit hint", wait, i, c)
			}
		}
	}
}

// A flapping link must not restart the backoff ladder each time. A remote daemon
// that accepts, verifies, attaches and dies immediately would otherwise get a
// fresh ssh roughly twice a second forever with the counter stuck at 1 — the same
// signature as the false-success bug verifyRemoteLink was added to fix.
func TestReconnect_FlappingLinkCarriesTheBackoffForward(t *testing.T) {
	m := Model{clientGen: 1, attached: attachedTo(testDest)}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) {
		return &failingClient{err: errors.New("unused")}, nil
	})

	// Climb a few attempts, then succeed.
	updated, _ := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	m = updated.(Model)
	for i := 0; i < 2; i++ {
		updated, _ = m.Update(redialResultMsg{gen: m.linkOf(testDest).gen, dest: testDest, err: errors.New("refused")})
		m = updated.(Model)
	}
	settled := m.linkFor(testDest).attempt
	if settled < 3 {
		t.Fatalf("fixture only reached attempt %d, want >= 3", settled)
	}
	updated, _ = m.Update(redialResultMsg{gen: m.linkOf(testDest).gen, dest: testDest, client: &failingClient{err: errors.New("live")}})
	m = updated.(Model)

	// It dies again immediately — well inside reconnectFlapWindow.
	updated, _ = m.Update(linkLostMsg{gen: m.clientGen, dest: testDest, err: errors.New("EOF again")})
	got := updated.(Model)

	if got.linkOf(testDest).attempt <= 1 {
		t.Errorf("attempt reset to %d after a flap; the ladder restarts at 500ms and the "+
			"backoff never engages", got.linkOf(testDest).attempt)
	}
}

// A link that survived comfortably is a real recovery, so the next outage starts
// from scratch rather than inheriting a stale penalty.
func TestReconnect_SettledLinkResetsTheBackoff(t *testing.T) {
	m := Model{clientGen: 1, attached: attachedTo(testDest)}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("unused") })
	// Restored long enough ago to count as settled.
	*m.linkFor(testDest) = reconnectState{
		lastUpAt:       time.Now().Add(-2 * reconnectFlapWindow),
		settledAttempt: 7,
	}

	updated, _ := m.Update(linkLostMsg{gen: 1, dest: testDest, err: errors.New("EOF")})
	got := updated.(Model)

	if got.linkOf(testDest).attempt != 1 {
		t.Errorf("attempt = %d after a settled link dropped, want 1 (a fresh ladder)", got.linkOf(testDest).attempt)
	}
}

// A dialog open when the link drops must not keep taking keys. The freeze sits
// ahead of the whole type switch, so it runs before any dialog key handling —
// reachable in practice (Settings open, wifi blips) and previously untested.
func TestReconnect_FreezeAppliesWithADialogOpen(t *testing.T) {
	newM := func(active bool) Model {
		m := Model{cfg: config.Default(), width: 80, height: 24,
			dialog: dialogAbout, dialogCursor: 0}
		m.asRemote("gpu01")
		m.linkFor(testDest).active = active
		return m
	}

	down, _ := newM(true).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := down.(Model); got.dialogCursor != 0 {
		t.Errorf("dialog cursor moved to %d while the link was down", got.dialogCursor)
	}

	up, _ := newM(false).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := up.(Model); got.dialogCursor == 0 {
		t.Error("the same key did not move the cursor with the link up — this test " +
			"no longer observes the freeze")
	}
}

// The banner is drawn over row 0 regardless of what else is on screen, so a
// dialog open during an outage must not stop it rendering, and the frame must
// stay within its width.
func TestReconnect_BannerRendersOverADialog(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	*m.linkFor(testDest) = reconnectState{active: true, attempt: 2, lastErr: errors.New("timed out")}
	m.dialog = dialogAbout

	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "ctrl+q") {
		t.Error("the reconnect banner is absent while a dialog is open")
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Errorf("line %d measured %d cells, wider than the %d-cell frame", i, got, m.width)
		}
	}
}

// deliverGhost simulates the daemon replaying a chunk for one pane, which is
// what consumes an armed reattach reset.
func deliverGhost(t *testing.T, m *Model, paneID string, data string) {
	t.Helper()
	updated, _ := m.Update(PaneOutputMsg{PaneID: paneID, Data: []byte(data), Ghost: true})
	got := updated.(Model)
	*m = got
}

// armAndReplayAll simulates a full reattach in which the daemon replays every
// pane: arm the reset, then deliver a ghost chunk to each. Most reset tests mean
// this, since the reset is now consumed by the replay rather than predicted.
func armAndReplayAll(t *testing.T, m *Model, data string) {
	t.Helper()
	m.armReattachReset(testDest)
	for _, p := range reconnectTestPanes(m) {
		deliverGhost(t, m, p.ID, data)
	}
}

// TestReconnect_PermanentFailureParksInsteadOfRetrying pins the fail2ban guard.
//
// Every reconnect is a full authentication, so a rejected key retried forever
// is a stream of failed auths from the operator's own address — which a default
// sshd jail bans, locking them out of a host that was never unreachable.
func TestReconnect_PermanentFailureParksInsteadOfRetrying(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.linkFor(testDest).active = true
	m.linkFor(testDest).attempt = 3

	updated, cmd := m.Update(redialResultMsg{
		gen: m.linkOf(testDest).gen,
		dest: testDest,
		err: fmt.Errorf("Permission denied (publickey): %w", ErrLinkPermanent),
	})
	got := updated.(Model)

	if !got.linkOf(testDest).parked {
		t.Fatal("a permanent failure must park the loop")
	}
	if cmd != nil {
		t.Error("parking must not schedule another redial")
	}
	if !got.linkOf(testDest).active {
		t.Error("the banner must stay up while parked — the session is paused, not over")
	}
	if got.linkOf(testDest).attempt != 3 {
		t.Errorf("attempt = %d, want it left at 3: parking is not a retry", got.linkOf(testDest).attempt)
	}
}

// TestReconnect_TransientFailureStillRetries is the control arm. Without it the
// park test passes for a model that parks on EVERY failure.
func TestReconnect_TransientFailureStillRetries(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.linkFor(testDest).active = true
	m.linkFor(testDest).attempt = 3

	updated, cmd := m.Update(redialResultMsg{
		gen: m.linkOf(testDest).gen,
		dest: testDest,
		err: errors.New("ssh: connect to host gpu01 port 22: Connection timed out"),
	})
	got := updated.(Model)

	if got.linkOf(testDest).parked {
		t.Error("an unclassified failure must keep retrying, not park")
	}
	if cmd == nil {
		t.Error("a transient failure must schedule another redial")
	}
}

// TestReconnect_ResumeKeyRestartsAParkedLoop covers the way back. A parked
// session with no resume path would leave "quit and relaunch" as the only
// option, which is what the banner exists to avoid.
func TestReconnect_ResumeKeyRestartsAParkedLoop(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	// A dialer, because THIS park is only reachable with one. A destination
	// parked because it can never be redialled at all — the multi-daemon case —
	// has no resume path and deliberately offers no key.
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("refused") })
	m.linkFor(testDest).active = true
	m.linkFor(testDest).parked = true
	m.linkFor(testDest).attempt = 3
	m.linkFor(testDest).lastErr = errors.New("Permission denied (publickey)")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	got := updated.(Model)

	if got.linkOf(testDest).parked {
		t.Error("the resume key must clear the parked state")
	}
	if cmd == nil {
		t.Fatal("resuming must schedule a redial")
	}
	if got.linkOf(testDest).attempt < 3 {
		t.Errorf("attempt = %d, want it carried forward: resuming does not un-happen "+
			"the earlier failures, and restarting the ladder undoes the rate decay",
			got.linkOf(testDest).attempt)
	}
}

// TestReconnect_OrdinaryKeysStayFrozenWhileParked guards the freeze. Only the
// resume key and the quit escape may act.
func TestReconnect_OrdinaryKeysStayFrozenWhileParked(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.linkFor(testDest).active = true
	m.linkFor(testDest).parked = true

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	got := updated.(Model)

	if !got.linkOf(testDest).parked {
		t.Error("an ordinary key must not resume a parked loop")
	}
	if cmd != nil {
		t.Error("an ordinary key must produce no command while frozen")
	}
}

// TestBannerCandidates_ParkedRungsKeepBothKeys extends the exit-hint invariant
// to the parked phase. Asserting the bare letter "r" would be tautological —
// every rung already contains one in "ctrl+q" — so this pins the hint literal.
func TestBannerCandidates_ParkedRungsKeepBothKeys(t *testing.T) {
	m := Model{links: oneLink(reconnectState{active: true, attempt: 3, parked: true})}
	m.asRemote("gpu01")
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("refused") })

	got := m.bannerCandidates()
	if len(got) == 0 {
		t.Fatal("no candidates; renderReconnectBanner would have nothing to fall back to")
	}
	for i, c := range got {
		if !strings.Contains(c, "ctrl+q") {
			t.Errorf("parked rung %d (%q) has no exit hint", i, c)
		}
		if !strings.Contains(c, bannerResumeHint) {
			t.Errorf("parked rung %d (%q) has no resume hint", i, c)
		}
		if strings.Contains(c, "Connecting") {
			t.Errorf("parked rung %d (%q) claims a connection is in progress; "+
				"nothing is connecting until the operator acts", i, c)
		}
	}
}

// TestRenderReconnectBanner_ParkedNamesTheCause checks the rendered row, not
// just the ladder: two real banner defects on this component were invisible to
// passing unit tests and only showed up in the output.
func TestRenderReconnectBanner_ParkedNamesTheCause(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.SetRedialFunc(testDest, func(Client) (Client, error) { return nil, errors.New("refused") })
	m.linkFor(testDest).active = true
	m.linkFor(testDest).parked = true
	m.linkFor(testDest).lastErr = errors.New("Permission denied (publickey)")
	m.asRemote("gpu01")

	banner := stripANSI(m.renderReconnectBanner(80))
	for _, want := range []string{"Permission denied", bannerResumeHint, "ctrl+q"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner %q is missing %q", banner, want)
		}
	}
}

// twoDaemonModel builds a client holding one project per destination, each with
// a single-pane tab. The active project is the local one, so every assertion
// below distinguishes "the daemon that dropped" from "the daemon on screen".
func twoDaemonModel(t *testing.T) (*Model, *PaneModel, *PaneModel) {
	t.Helper()
	localTab, gpuTab := tabWithPane("tab-local", "p-local"), tabWithPane("tab-gpu", "p-gpu")
	local, gpu := localTab.Leaves()[0], gpuTab.Leaves()[0]
	t.Cleanup(local.Dispose)
	t.Cleanup(gpu.Dispose)
	m := &Model{
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{localTab}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{gpuTab}},
		},
		activeProject: 0,
		width:         80,
		height:        24,
		cfg:           config.Default(),
	}
	return m, local, gpu
}

// The reattach reset must reach only the daemon that came back. Only that one
// replays, so arming another daemon's panes waits for a chunk that never
// arrives — and zeroing their work counters clears a spinner on a machine that
// is still running the turn, corrected only by its next hook event.
func TestReconnect_ReattachResetIsScopedToTheDaemonThatReturned(t *testing.T) {
	m, local, gpu := twoDaemonModel(t)
	for _, p := range []*PaneModel{local, gpu} {
		p.working = true
		p.turnActive = true
		p.subagents = 2
	}

	m.armReattachReset("gpu01")
	m.resetWorkStateForReattach("gpu01")

	if !gpu.reattachReset {
		t.Error("the reconnected daemon's pane was not armed")
	}
	if gpu.working || gpu.subagents != 0 {
		t.Error("the reconnected daemon's work state was not reset")
	}
	if local.reattachReset {
		t.Error("a pane on a daemon that never dropped was armed for a replay it will never get")
	}
	if !local.working || local.subagents != 2 {
		t.Error("a live daemon's work state was zeroed by another daemon's reconnect; its " +
			"spinner stops until the next hook event")
	}
}

// A selection anchored on a daemon that did not drop must survive: its content
// is not being replaced, so the coordinates are still valid.
func TestReconnect_SelectionSurvivesAnotherDaemonsReconnect(t *testing.T) {
	m, _, _ := twoDaemonModel(t)
	m.selection = &Selection{PaneID: "p-local"}

	m.armReattachReset("gpu01")

	if m.selection == nil {
		t.Error("a selection on a daemon that never dropped was cleared")
	}

	m.armReattachReset("")
	if m.selection != nil {
		t.Error("the selection survived its OWN daemon's reconnect, so it now anchors into " +
			"content that is about to be replayed")
	}
}

// The reattach must be addressed to the daemon that returned. An unstamped one
// resolves to the ACTIVE project, so a background daemon reconnecting would make
// the foreground one replay its whole output buffer — doubling every pane's
// scrollback on the machine that never dropped.
func TestReconnect_ReattachIsAddressedToTheDaemonThatReturned(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	m, _, _ := twoDaemonModel(t)
	m.client = r

	runCmd(m.attachToDest("gpu01"))

	// Attach plus the plugin-list request that rides with it — both addressed
	// to gpu01, which is the point: the reconnected daemon is asked about its
	// OWN registry, not the foreground one's.
	if !sentTypes(gpu)[ipc.MsgAttach] {
		t.Errorf("the reconnected daemon got %v, want an %s among them", sentTypes(gpu), ipc.MsgAttach)
	}
	if !sentTypes(gpu)[ipc.MsgPluginListReq] {
		t.Errorf("the reconnected daemon was not asked for its own plugin list; the "+
			"foreground daemon's answer would be adopted as its. sent = %v", sentTypes(gpu))
	}
	if local.sentCount() != 0 {
		t.Error("the attach went to the ACTIVE daemon rather than the one that reconnected")
	}
}

// sentTypes summarises what a fake conn received, as a set. Used where the
// assertion is "this daemon was asked X" rather than "this daemon received
// exactly N messages" — a count breaks whenever a second, correct message is
// batched alongside.
func sentTypes(c *fakeConn) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.sent))
	for _, m := range c.sent {
		out[m.Type] = true
	}
	return out
}

// With a router there is no single client to swap: the fresh conn replaces ONE
// entry, and every other connection keeps running.
func TestReconnect_RouterSwapsOneConnectionNotTheClient(t *testing.T) {
	dead, other, fresh := newFakeConn(), newFakeConn(), newFakeConn()
	dead.err = errors.New("connection reset")
	r := NewRouter(map[string]Client{"gpu01": dead, "prod": other})
	// Drain the loss so the dead pump has retired its registration — the state
	// finishReconnect actually runs in.
	if lost, _ := r.Receive(); lost.Type != ipc.MsgLinkLost {
		t.Fatalf("fixture received %s, want a link loss", lost.Type)
	}

	m := Model{client: r, links: map[string]*reconnectState{"gpu01": {active: true, attempt: 2}}}
	updated, _ := m.finishReconnect("gpu01", fresh)
	got := updated.(Model)

	if got.client != Client(r) {
		t.Error("the router itself was replaced; every other daemon's connection went with it")
	}
	if r.Conn("gpu01") != Client(fresh) {
		t.Error("the fresh connection was not installed, so the reconnected daemon stays silent")
	}
	if r.Conn("prod") != Client(other) {
		t.Error("an unrelated daemon's connection was disturbed by another one's reconnect")
	}
	if got.linkOf("gpu01").active {
		t.Error("the reconnected destination is still marked down")
	}
}

// Two ladders can be climbing at once and their attempt numbers are unrelated,
// so a tick has to match on BOTH the destination and that destination's own
// attempt — matching on the counter alone lets one host's timer dial another.
func TestReconnect_TickForOneDestDoesNotDialAnother(t *testing.T) {
	gpuDials, prodDials := 0, 0
	m := Model{clientGen: 1, links: map[string]*reconnectState{
		"gpu01": {active: true, attempt: 1, gen: 1},
		"prod":  {active: true, attempt: 4, gen: 1},
	}}
	m.asRemote("gpu01")
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { gpuDials++; return nil, errors.New("refused") })
	m.SetRedialFunc("prod", func(Client) (Client, error) { prodDials++; return nil, errors.New("refused") })

	_, cmd := m.Update(redialTickMsg{gen: 1, dest: "prod", attempt: 4})
	if cmd == nil {
		t.Fatal("prod's own tick armed no dial")
	}
	cmd()
	if prodDials != 1 || gpuDials != 0 {
		t.Fatalf("prod's tick dialled prod %d time(s) and gpu01 %d, want 1/0", prodDials, gpuDials)
	}

	// prod's attempt number against gpu01's ladder, which is at 1.
	if _, stale := m.Update(redialTickMsg{gen: 1, dest: "gpu01", attempt: 4}); stale != nil {
		t.Error("a tick carrying another destination's attempt number started a dial")
	}
}

// routerModel builds a Model over a router holding the named destinations, each
// backed by a fake conn the test can drive. The conns are returned so a test can
// make one of them fail.
func routerModel(dests ...string) (*Model, map[string]*fakeConn) {
	conns := make(map[string]*fakeConn, len(dests))
	clients := make(map[string]Client, len(dests))
	for _, d := range dests {
		c := newFakeConn()
		conns[d] = c
		clients[d] = c
	}
	return &Model{client: NewRouter(clients), attached: attachedTo(dests...)}, conns
}

// While one destination's ladder climbs, EVERY other destination's messages
// still have to arrive — and the listen loop is the only reader of them. It
// stops when it returns a linkLostMsg, so the loss branch has to re-arm it, or
// a healthy daemon's output is parked behind a dead one's reconnect for as long
// as the reconnect takes.
func TestLinkLost_RearmsTheListenLoopForTheOtherDaemons(t *testing.T) {
	m, conns := routerModel("", "gpu01")
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, errors.New("refused") })

	_, cmd := m.Update(linkLostMsg{gen: m.clientGen, dest: "gpu01", err: errors.New("EOF")})
	if cmd == nil {
		t.Fatal("no command at all; nothing is listening and nothing is retrying")
	}

	// A message from the SURVIVING daemon must still reach the Model. Queue it,
	// then drain the batch looking for the listen command that picks it up.
	out, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "p-local", Data: []byte("alive")})
	conns[""].recv <- out

	if !batchYields(cmd, func(msg tea.Msg) bool {
		o, ok := msg.(PaneOutputMsg)
		return ok && o.PaneID == "p-local"
	}) {
		t.Fatal("the local daemon's output never arrived: the listen loop was not re-armed, " +
			"so every other daemon is silent until the reconnect finishes")
	}
}

// A single-connection client must NOT re-arm on a drop. Its client is dead, so a
// fresh Receive fails instantly and the re-arm is a hot loop. finishReconnect
// owns the re-arm there instead.
func TestLinkLost_SingleConnectionDoesNotRearmTheListenLoop(t *testing.T) {
	m := Model{clientGen: 1, client: &failingClient{err: errors.New("dead")}}
	m.SetRedialFunc("", func(Client) (Client, error) { return nil, errors.New("refused") })

	_, cmd := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	if cmd == nil {
		t.Fatal("no retry scheduled")
	}
	if batchYields(cmd, func(msg tea.Msg) bool { _, ok := msg.(linkLostMsg); return ok }) {
		t.Error("the listen loop was re-armed on a dead single connection; its Receive " +
			"fails instantly, so this spins a core reporting the same death forever")
	}
}

// One daemon dying with no way back must not end a session that still has
// others. The reverse — the only daemon — must still quit, or a dead local
// daemon leaves a client full of panes that no longer exist.
func TestLinkLost_QuitsOnlyWhenNothingElseIsLeft(t *testing.T) {
	t.Run("one of several keeps the session", func(t *testing.T) {
		m, conns := routerModel("", "gpu01")
		m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, errors.New("refused") })
		// The re-armed listen loop blocks on the router's channel, so give the
		// SURVIVING daemon something to say — otherwise draining the batch
		// parks forever waiting on a session that is working exactly as
		// intended.
		out, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "p-gpu", Data: []byte("alive")})
		conns["gpu01"].recv <- out

		updated, cmd := m.Update(linkLostMsg{gen: m.clientGen, dest: "", err: errors.New("EOF")})
		if batchYields(cmd, isQuit) {
			t.Fatal("the local daemon dying quit the whole client; the remote daemon's " +
				"work is still running and was still on screen")
		}
		got := updated.(Model)
		if !got.linkOf("").active || !got.linkOf("").parked {
			t.Errorf("link state = %+v, want active+parked: the banner must say the daemon "+
				"is gone rather than showing a countdown to a retry that never fires",
				got.linkOf(""))
		}
	})

	t.Run("the only daemon is fatal", func(t *testing.T) {
		m, _ := routerModel("")

		_, cmd := m.Update(linkLostMsg{gen: m.clientGen, dest: "", err: errors.New("EOF")})
		if !batchYields(cmd, isQuit) {
			t.Fatal("the only daemon died with no way back and the client stayed up, " +
				"showing panes that no longer exist")
		}
	})
}

// A parked destination with no dialer offers no resume key, and pressing it
// anyway must not reach redialCmd — the RedialFunc there is nil.
func TestParkedWithoutADialerOffersNoResume(t *testing.T) {
	m, _ := routerModel("", "gpu01")
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, errors.New("refused") })
	m.linkFor("").active = true
	m.linkFor("").parked = true

	for _, c := range m.bannerCandidates() {
		if strings.Contains(c, bannerResumeHint) {
			t.Errorf("rung %q offers a resume key for a destination that cannot be redialled", c)
		}
		if !strings.Contains(c, "ctrl+q") {
			t.Errorf("rung %q has no exit hint", c)
		}
	}

	// The guard belongs at the action too, not only at its affordance.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		t.Error("resuming an un-redialable destination produced a command")
	}
	if !updated.(Model).linkOf("").parked {
		t.Error("the resume key un-parked a destination with no dialer behind it")
	}
}

// Two ladders climb at once, and one completing must not retire the other's.
//
// The generation used to be a single Model-wide counter that finishReconnect
// bumped for whichever destination finished. The other destination's armed
// redialTickMsg and in-flight redialResultMsg then carried a number that no
// longer matched, so both were dropped: its `active` stayed true with no timer
// left to clear it and its banner stuck for the rest of the session.
func TestReconnect_OneDestCompletingDoesNotKillAnothersLadder(t *testing.T) {
	m, _ := routerModel("gpu01", "prod")
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, errors.New("refused") })
	m.SetRedialFunc("prod", func(Client) (Client, error) { return nil, errors.New("refused") })
	*m.linkFor("gpu01") = reconnectState{active: true, attempt: 2}
	*m.linkFor("prod") = reconnectState{active: true, attempt: 1}
	prodGen := m.linkOf("prod").gen

	// gpu01 comes back, which is what used to bump the shared counter.
	updated, _ := m.Update(redialResultMsg{gen: m.linkOf("gpu01").gen, dest: "gpu01", client: newFakeConn()})
	m = ptr(updated.(Model))

	// prod's own tick, armed before that, must still start its dial.
	_, cmd := m.Update(redialTickMsg{gen: prodGen, dest: "prod", attempt: 1})
	if cmd == nil {
		t.Fatal("prod's armed tick was discarded because gpu01 reconnected; prod is now " +
			"stuck showing a banner with no timer behind it")
	}
}

// ptr re-homes an Update result so a test can keep driving it through the
// pointer-receiver helpers.
func ptr(m Model) *Model { return &m }

// batchYields runs a command — flattening one level of tea.Batch — and reports
// whether any message it produces satisfies want. Batched children run on
// separate goroutines in the real program; here they are drained in order,
// which is enough to ask "was this command included at all".
func batchYields(cmd tea.Cmd, want func(tea.Msg) bool) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if batchYields(child, want) {
				return true
			}
		}
		return false
	}
	return want(msg)
}
