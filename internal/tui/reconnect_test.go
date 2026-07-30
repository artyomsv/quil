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

	updated, cmd := m.Update(redialResultMsg{gen: 7, client: fresh})
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
		m := Model{cfg: config.Default(), reconnect: reconnectState{active: true, attempt: 3}}
		m.SetRemoteDest("gpu01")
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
		m.SetRemoteDest("gpu01")
		m.reconnect.active = active
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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 4,
		lastErr: errors.New("connection refused"),
	}}
	m.SetRemoteDest("gpu01")

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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 12,
		lastErr: errors.New(strings.Repeat("very long ssh diagnostic ", 20)),
	}}
	m.SetRemoteDest(strings.Repeat("host", 30))

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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 2,
		lastErr: errors.New(strings.Repeat("diagnostic ", 30)),
	}}
	m.SetRemoteDest("gpu01")

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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 4,
		lastErr: errors.New("ssh: connect to host gpu01 port 22: Connection refused"),
	}}
	m.SetRemoteDest("gpu01")

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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 4,
		lastErr: errors.New("Connection refused by the remote host"),
	}}
	m.SetRemoteDest("gpu01")

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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 1,
		lastErr: errors.New("Permission denied (publickey).\nssh: connect failed\nmore"),
	}}
	m.SetRemoteDest("gpu01")

	if n := strings.Count(m.renderReconnectBanner(120), "\n"); n != 0 {
		t.Errorf("banner spans %d extra lines; the overlay would eat the tab bar and the row below", n)
	}
}

// Nothing is drawn when no reconnect is in flight.
func TestRenderReconnectBanner_EmptyWhenInactive(t *testing.T) {
	m := Model{}
	m.SetRemoteDest("gpu01")
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

	m := &Model{tabs: []*TabModel{tab}, width: 80, height: 24, cfg: config.Default()}
	m.SetRemoteDest("gpu01")
	return m
}

// reconnectTestPanes returns every pane the reset must touch, overlay included.
func reconnectTestPanes(m *Model) []*PaneModel {
	var out []*PaneModel
	for _, tab := range m.tabs {
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
	p := m.tabs[0].Leaves()[0]
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
	ov := m.tabs[0].overlayPane
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
	m.tabs = append(m.tabs, bg)

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
	m.attached = true
	m.reconnect = reconnectState{active: true, attempt: 1}

	panes := reconnectTestPanes(m)
	for _, p := range panes {
		p.AppendOutput([]byte("PREDROP pre-reconnect content\r\n"))
	}
	m.selection = &Selection{PaneID: "p0"}

	updated, cmd := m.Update(redialResultMsg{gen: 3, client: &failingClient{err: errors.New("unused")}})
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
	p := m.tabs[0].Leaves()[0]

	m.applyWorkTransition(p.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(p.ID, "hook.claude.SubagentStart", map[string]string{"coalesced": "3"})
	if !p.working {
		t.Fatal("fixture did not put the pane into a working state")
	}
	if p.subagents != 3 {
		t.Fatalf("fixture subagents = %d, want 3 (coalesced burst)", p.subagents)
	}

	m.resetWorkStateForReattach()

	if p.working {
		t.Error("pane still working after reset")
	}
	if p.subagents != 0 {
		t.Errorf("subagents = %d, want 0 — a replayed start would stack on top of this", p.subagents)
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
	p := m.tabs[0].Leaves()[0]
	p.unseen = true

	m.resetWorkStateForReattach()

	if !p.unseen {
		t.Error("unseen mark cleared by reconnect; it reports unread work, not a live turn")
	}
}

// pinnedAttention is a user-set pin with no connection to execution state.
func TestReconnect_KeepsPinnedAttention(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	p := m.tabs[0].Leaves()[0]
	p.pinnedAttention = true

	m.resetWorkStateForReattach()

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

	m.resetWorkStateForReattach()

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
	m.tabs = append(m.tabs, bg)

	m.applyWorkTransition("bg0", "hook.claude.UserPromptSubmit", nil)
	if !p.working {
		t.Fatal("fixture did not start a turn on the background pane")
	}

	m.resetWorkStateForReattach()

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
	m.attached = true
	m.reconnect = reconnectState{active: true, attempt: 1}
	focused := m.tabs[0].Leaves()[0]
	other := m.tabs[0].Leaves()[1]
	if focused.ID != m.tabs[0].ActivePane {
		focused, other = other, focused
	}

	m.applyWorkTransition(focused.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(focused.ID, "hook.claude.SubagentStart", map[string]string{"coalesced": "2"})
	other.unseen = true

	m.Update(redialResultMsg{gen: 5, client: &failingClient{err: errors.New("unused")}})

	if focused.working {
		t.Error("pane still working after a successful reconnect")
	}
	if focused.subagents != 0 {
		t.Errorf("subagents = %d after reconnect, want 0", focused.subagents)
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
	m := Model{clientGen: 1, attached: true}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) {
		dials++
		return &failingClient{err: errors.New("unused")}, nil
	})

	// Link drops, then a redial succeeds — the generation moves to 2.
	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	m = updated.(Model)
	updated, _ = m.Update(redialResultMsg{gen: 1, client: &failingClient{err: errors.New("live")}})
	m = updated.(Model)
	if m.clientGen != 2 {
		t.Fatalf("clientGen = %d after a successful reconnect, want 2", m.clientGen)
	}
	if m.reconnect.active {
		t.Fatal("reconnect still active after success")
	}

	// Now the DEAD client's loop finally errors, reporting the old generation.
	updated, cmd := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	got := updated.(Model)

	if got.reconnect.active {
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
	m := Model{clientGen: 1, attached: true}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) {
		return &failingClient{err: errors.New("unused")}, nil
	})

	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	m = updated.(Model)
	updated, _ = m.Update(redialResultMsg{gen: 1, client: &failingClient{err: errors.New("live")}})
	m = updated.(Model)

	// The NEW client's own loop reports a drop, stamped with the new generation.
	updated, cmd := m.Update(linkLostMsg{gen: m.clientGen, err: errors.New("EOF again")})
	got := updated.(Model)

	if !got.reconnect.active {
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
// staged update), and canReconnect requires remote mode — so the two conditions
// never coincide. This pins the invariant for RD-027, which makes update
// controls remote-aware and would otherwise reintroduce it silently.
func TestReconnect_DoesNotReopenUpdateNotice(t *testing.T) {
	m := Model{clientGen: 1, attached: true, sawFirstState: true,
		reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")

	updated, _ := m.Update(redialResultMsg{gen: 1, client: &failingClient{err: errors.New("unused")}})

	if got := updated.(Model); !got.sawFirstState {
		t.Error("sawFirstState was cleared by the reconnect; the update notice would reopen " +
			"on the reattach broadcast once RD-027 makes it remote-aware")
	}
}

// The reconnect must not resurrect a dialog or leave one stranded. Dialog state
// is client-independent, so it is left exactly as the user had it.
func TestReconnect_LeavesDialogStateAlone(t *testing.T) {
	m := Model{clientGen: 1, attached: true, dialog: dialogAbout, dialogCursor: 3,
		reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")

	updated, _ := m.Update(redialResultMsg{gen: 1, client: &failingClient{err: errors.New("unused")}})
	got := updated.(Model)

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
	p := m.tabs[0].Leaves()[0]
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
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 6,
		nextAt:  time.Now().Add(8 * time.Second),
		lastErr: errors.New("connection timed out"),
	}}
	m.SetRemoteDest("gpu01")

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

	waiting := Model{reconnect: base}
	waiting.reconnect.nextAt = time.Now().Add(5 * time.Second)
	waiting.SetRemoteDest("gpu01")

	connecting := Model{reconnect: base} // nextAt zero = in the past
	connecting.SetRemoteDest("gpu01")

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
		m := Model{reconnect: reconnectState{active: true, attempt: 1, nextAt: time.Now().Add(remain)}}
		m.SetRemoteDest("gpu01")
		out := stripANSI(m.renderReconnectBanner(120))
		if strings.Contains(out, "in 0s") {
			t.Errorf("remain=%v rendered a zero countdown\ngot: %s", remain, out)
		}
	}
}

// Both phases keep the exit hint at the narrowest width the TUI will render at.
func TestRenderReconnectBanner_BothPhasesKeepExitHintAtMinWidth(t *testing.T) {
	for _, wait := range []time.Duration{0, 9 * time.Second} {
		m := Model{reconnect: reconnectState{
			active:  true,
			attempt: 12,
			lastErr: errors.New(strings.Repeat("diagnostic ", 20)),
		}}
		if wait > 0 {
			m.reconnect.nextAt = time.Now().Add(wait)
		}
		m.SetRemoteDest("some-rather-long-hostname.example.internal")

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

// THE CRITICAL REGRESSION. handleAttach replays only panes whose plugin has
// ghost_buffer = true; for the rest it sends NOTHING and falls through to
// redrawKick, which is itself a no-op without a redraw_key. Resetting such a
// pane destroys the only content it has and leaves a blank rectangle in front of
// a live process — indefinitely, if the pane is in a background tab.
//
// THE CRITICAL REGRESSION, and the P1 that followed the first fix.
//
// handleAttach replays only plugins with ghost_buffer = true; the rest get
// nothing and fall through to redrawKick, itself a no-op without a redraw_key.
// Resetting such a pane leaves a blank rectangle in front of a live process —
// indefinitely, in a background tab. That hit opencode, lazygit, k9s and lazysql.
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
		p := m.tabs[0].Leaves()[0]
		p.AppendOutput([]byte("stale content from before the drop\r\n"))
		if p.rawBuf.Len() == 0 {
			t.Fatal("fixture wrote nothing")
		}

		m.armReattachReset()
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
		replayed, silent := m.tabs[0].Leaves()[0], m.tabs[0].Leaves()[1]
		for _, p := range []*PaneModel{replayed, silent} {
			p.AppendOutput([]byte("content that predates the reconnect\r\n"))
		}

		m.armReattachReset()
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
		p := m.tabs[0].Leaves()[0]
		p.AppendOutput([]byte("before\r\n"))

		m.armReattachReset()
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
		m := Model{reconnect: reconnectState{active: true, attempt: 3}}
		if wait > 0 {
			m.reconnect.nextAt = time.Now().Add(wait)
		}
		m.SetRemoteDest("gpu01")

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
	m := Model{clientGen: 1, attached: true}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) {
		return &failingClient{err: errors.New("unused")}, nil
	})

	// Climb a few attempts, then succeed.
	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	m = updated.(Model)
	for i := 0; i < 2; i++ {
		updated, _ = m.Update(redialResultMsg{gen: m.clientGen, err: errors.New("refused")})
		m = updated.(Model)
	}
	settled := m.reconnect.attempt
	if settled < 3 {
		t.Fatalf("fixture only reached attempt %d, want >= 3", settled)
	}
	updated, _ = m.Update(redialResultMsg{gen: m.clientGen, client: &failingClient{err: errors.New("live")}})
	m = updated.(Model)

	// It dies again immediately — well inside reconnectFlapWindow.
	updated, _ = m.Update(linkLostMsg{gen: m.clientGen, err: errors.New("EOF again")})
	got := updated.(Model)

	if got.reconnect.attempt <= 1 {
		t.Errorf("attempt reset to %d after a flap; the ladder restarts at 500ms and the "+
			"backoff never engages", got.reconnect.attempt)
	}
}

// A link that survived comfortably is a real recovery, so the next outage starts
// from scratch rather than inheriting a stale penalty.
func TestReconnect_SettledLinkResetsTheBackoff(t *testing.T) {
	m := Model{clientGen: 1, attached: true}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, errors.New("unused") })
	// Restored long enough ago to count as settled.
	m.reconnect = reconnectState{
		lastUpAt:       time.Now().Add(-2 * reconnectFlapWindow),
		settledAttempt: 7,
	}

	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	got := updated.(Model)

	if got.reconnect.attempt != 1 {
		t.Errorf("attempt = %d after a settled link dropped, want 1 (a fresh ladder)", got.reconnect.attempt)
	}
}

// A dialog open when the link drops must not keep taking keys. The freeze sits
// ahead of the whole type switch, so it runs before any dialog key handling —
// reachable in practice (Settings open, wifi blips) and previously untested.
func TestReconnect_FreezeAppliesWithADialogOpen(t *testing.T) {
	newM := func(active bool) Model {
		m := Model{cfg: config.Default(), width: 80, height: 24,
			dialog: dialogAbout, dialogCursor: 0}
		m.SetRemoteDest("gpu01")
		m.reconnect.active = active
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
	m.reconnect = reconnectState{active: true, attempt: 2, lastErr: errors.New("timed out")}
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
	m.armReattachReset()
	for _, p := range reconnectTestPanes(m) {
		deliverGhost(t, m, p.ID, data)
	}
}
