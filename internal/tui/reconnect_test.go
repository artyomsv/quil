package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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

// A dead link in remote mode is a reconnectable event, not a quit.
func TestListenForMessages_RemoteLinkLoss_ReturnsLinkLostMsg(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}, clientGen: 3}
	m.SetRemoteDest("gpu01")

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
	m.SetRemoteDest("gpu01")

	if msg := m.listenForMessages()(); !isQuit(msg) {
		t.Fatalf("msg is %T, want tea.QuitMsg", msg)
	}
}

// Local mode keeps today's behaviour: a dead local daemon means dead panes, and
// quietly retrying would hide that.
func TestUpdate_LinkLost_LocalMode_Quits(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}}
	// no SetRemoteDest, no redial func

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
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, errors.New("unused") })

	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	got := updated.(Model)

	if !got.reconnect.active {
		t.Fatal("remote link loss did not begin a reconnect")
	}
	if got.reconnect.lastErr == nil {
		t.Error("reconnect state kept no cause")
	}
}

// Remote mode with no dialer installed is still fatal — there is nothing to
// retry with, and pretending otherwise would hang the session forever.
func TestUpdate_LinkLost_RemoteWithoutDialer_Quits(t *testing.T) {
	m := Model{}
	m.SetRemoteDest("gpu01")

	_, cmd := m.Update(linkLostMsg{gen: 0, err: errors.New("EOF")})
	if cmd == nil || !isQuit(cmd()) {
		t.Fatal("remote link loss without a dialer did not quit")
	}
}

// A link-loss report from a previous client must be ignored: the old listen
// loop is still parked in Receive when the new client is already live.
func TestUpdate_LinkLost_StaleGeneration_Ignored(t *testing.T) {
	m := Model{clientGen: 5}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, errors.New("must not dial") })

	updated, cmd := m.Update(linkLostMsg{gen: 4, err: errors.New("stale")})
	got := updated.(Model)

	if got.reconnect.active {
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
		{"caps at 30s", 12, 15 * time.Second, 30 * time.Second},
		{"stays capped past the shift width", 100, 15 * time.Second, 30 * time.Second},
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
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(d.dial)

	updated, cmd := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	got := updated.(Model)

	if !got.reconnect.active {
		t.Fatal("reconnect not active")
	}
	if got.reconnect.attempt != 1 {
		t.Errorf("attempt = %d, want 1", got.reconnect.attempt)
	}
	if cmd == nil {
		t.Fatal("no timer armed; the reconnect would never fire")
	}
	if got.reconnect.nextAt.IsZero() {
		t.Error("nextAt not set; the banner has no countdown to show")
	}
}

// A second link loss while already reconnecting must not start a parallel loop.
func TestReconnect_SecondLinkLossDoesNotStackLoops(t *testing.T) {
	d := &recordingDialer{err: errors.New("refused")}
	m := Model{clientGen: 1}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(d.dial)

	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	m = updated.(Model)
	updated, cmd := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF again")})
	got := updated.(Model)

	if got.reconnect.attempt != 1 {
		t.Errorf("attempt = %d, want 1 — a second loop was armed", got.reconnect.attempt)
	}
	if cmd != nil {
		t.Error("a second timer was armed")
	}
}

// The tick for the current generation runs the dialer.
func TestReconnect_TickRunsDialer(t *testing.T) {
	d := &recordingDialer{err: errors.New("refused")}
	old := &failingClient{err: errors.New("dead")}
	m := Model{clientGen: 4, client: old, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(d.dial)

	_, cmd := m.Update(redialTickMsg{gen: 4, attempt: 1})
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
	m := Model{clientGen: 9, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(d.dial)

	_, cmd := m.Update(redialTickMsg{gen: 8, attempt: 1})
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
	m := Model{clientGen: 7, reconnect: reconnectState{active: true, attempt: 2}}
	m.SetRemoteDest("gpu01")

	updated, cmd := m.Update(redialResultMsg{gen: 7, client: fresh})
	got := updated.(Model)

	if got.clientGen != 8 {
		t.Errorf("clientGen = %d, want 8", got.clientGen)
	}
	if got.reconnect.active {
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
	m := Model{clientGen: 2, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, errors.New("refused") })

	updated, cmd := m.Update(redialResultMsg{gen: 2, err: errors.New("refused")})
	got := updated.(Model)

	if !got.reconnect.active {
		t.Error("reconnect ended on a failed attempt")
	}
	if got.reconnect.attempt != 2 {
		t.Errorf("attempt = %d, want 2", got.reconnect.attempt)
	}
	if got.reconnect.lastErr == nil || got.reconnect.lastErr.Error() != "refused" {
		t.Errorf("lastErr = %v, want the dial error for the banner", got.reconnect.lastErr)
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
	m.SetRemoteDest("gpu01")

	updated, cmd := m.Update(redialResultMsg{gen: 8, client: live})
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
	m := Model{clientGen: 3, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, nil })

	updated, cmd := m.Update(redialResultMsg{gen: 3, client: nil, err: nil})
	got := updated.(Model)

	if got.clientGen != 3 {
		t.Errorf("clientGen = %d, want 3 — a nil client was installed", got.clientGen)
	}
	if !got.reconnect.active {
		t.Error("reconnect ended after a nil client was returned")
	}
	if cmd == nil {
		t.Error("no retry scheduled after a nil client")
	}
}

// m.attached must SURVIVE a reconnect. It gates the first-WindowSizeMsg attach
// path, so clearing it makes the next resize attach a second time — and the
// daemon replays the entire output buffer on every attach, so the cost is a
// doubled scrollback, not a redundant no-op. Invisible until you scroll up.
func TestReconnect_KeepsAttachedFlag(t *testing.T) {
	fresh := &failingClient{err: errors.New("unused")}
	m := Model{clientGen: 1, attached: true, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")

	updated, _ := m.Update(redialResultMsg{gen: 1, client: fresh})

	if got := updated.(Model); !got.attached {
		t.Error("attached was cleared by the reconnect; the next resize will attach again and double every pane's scrollback")
	}
}

// The generation must be bumped BEFORE the new listen loop is built, or that
// loop stamps its reports with the old number and its own link loss is
// discarded as stale — leaving a session that can never reconnect again.
func TestReconnect_NewListenLoopCarriesTheNewGeneration(t *testing.T) {
	fresh := &failingClient{err: errors.New("second death")}
	m := Model{clientGen: 7, attached: true, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")

	updated, _ := m.Update(redialResultMsg{gen: 7, client: fresh})
	got := updated.(Model)

	msg := got.listenForMessages()()
	lost, ok := msg.(linkLostMsg)
	if !ok {
		t.Fatalf("msg is %T, want linkLostMsg", msg)
	}
	if lost.gen != got.clientGen {
		t.Errorf("new listen loop stamped gen %d but the model is at %d — its own drop would be discarded as stale",
			lost.gen, got.clientGen)
	}
}
