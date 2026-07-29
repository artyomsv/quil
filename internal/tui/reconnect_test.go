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
