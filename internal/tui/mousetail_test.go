package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// decodeAsReader turns raw terminal bytes into the messages Update receives,
// using the same decoder the terminal reader runs. Each chunk is one read that
// the reader flushed on its escape timeout, so an incomplete sequence at the
// end of a chunk comes out as the uv.UnknownEvent the real reader produces.
func decodeAsReader(t *testing.T, chunks ...string) []tea.Msg {
	t.Helper()
	var d uv.EventDecoder
	var msgs []tea.Msg
	for _, c := range chunks {
		b := []byte(c)
		for len(b) > 0 {
			n, ev := d.Decode(b)
			if n == 0 {
				t.Fatalf("decoder made no progress on %q", b)
			}
			switch e := ev.(type) {
			case uv.KeyPressEvent:
				msgs = append(msgs, tea.KeyPressMsg(e))
			case uv.MouseMotionEvent:
				msgs = append(msgs, tea.MouseMotionMsg(e))
			default:
				msgs = append(msgs, ev)
			}
			b = b[n:]
		}
	}
	return msgs
}

// drainAll returns everything queued for the pane, without blocking.
func drainAll(m *Model) string {
	got := ""
	for {
		select {
		case in := <-m.inputCh:
			got += string(in.data)
		default:
			return got
		}
	}
}

// TestUpdate_SplitMouseReportNeverReachesThePane is the regression guard for
// stray "5;90;35M" text in a pane. An all-motion report split after `ESC[<3`
// reaches Update as an unknown head plus key presses for its tail; the tail
// must not be typed into the pane, while real keys around it must be — in the
// order they were typed, even when they looked like the start of a tail.
func TestUpdate_SplitMouseReportNeverReachesThePane(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		chunks []string
		expire bool // deliver the guard's expiry after the last chunk
		want   string
	}{
		{"split after the button code", []string{"\x1b[<3", "5;90;35M"}, false, ""},
		{"split before the final byte", []string{"\x1b[<35;90;35", "M"}, false, ""},
		{"release report", []string{"\x1b[<0;1", "2;7m"}, false, ""},
		{"typing after the tail", []string{"\x1b[<3", "5;90;35Mab"}, false, "ab"},
		{"real key breaks the tail", []string{"\x1b[<3", "5;9x"}, false, "5;9x"},
		{"digits then a letter after an abandoned head", []string{"\x1b[<3", "42x"}, false, "42x"},
		{"digits then expiry after an abandoned head", []string{"\x1b[<3", "42"}, true, "42"},
		{"a new head releases the old one's keys", []string{"\x1b[<3", "4", "\x1b[<3"}, false, "4"},
		{"expiry after a completed report", []string{"\x1b[<3", "5;90;35M"}, true, ""},
		{"digits with no head", []string{"5;90;35M"}, false, "5;90;35M"},
		{"whole report is not typed", []string{"\x1b[<35;90;35Mq"}, false, "q"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pm, _ := inputOrderTestModel(t, "p1", true)
			var m tea.Model = *pm
			for _, msg := range decodeAsReader(t, tt.chunks...) {
				m, _ = m.Update(msg)
			}
			if tt.expire {
				m, _ = m.Update(mouseTailExpireMsg{gen: m.(Model).mouseTail.gen})
			}
			if got := drainAll(pm); got != tt.want {
				t.Errorf("typed into pane = %q, want %q", got, tt.want)
			}
		})
	}
}

// daemonMsg stands for any message that does not come from the terminal — a
// workspace broadcast, a Cmd result, a notification jump. Update ignores it;
// a test moves focus itself, the way such a message would.
type daemonMsg struct{}

// twoPaneModel builds a tab with panes p1 (active) and p2.
func twoPaneModel() (*Model, *TabModel) {
	p1, p2 := NewPaneModel("p1", 1024), NewPaneModel("p2", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p1), Right: NewLeaf(p2)}
	tab.ActivePane = "p1"
	return &Model{projects: oneProject(tab), client: &fakeSender{}, inputCh: make(chan paneInput, inputForwardBuffer)}, tab
}

// drainByPane returns everything queued as "pane:data " entries.
func drainByPane(m *Model) string {
	got := ""
	for {
		select {
		case in := <-m.inputCh:
			got += in.paneID + ":" + string(in.data) + " "
		default:
			return got
		}
	}
}

// TestUpdate_DaemonMessageInsideATailDoesNotBreakIt: a broadcast can land
// between the keys of a real split report; the rest of the tail must still
// be recognised and dropped.
func TestUpdate_DaemonMessageInsideATailDoesNotBreakIt(t *testing.T) {
	t.Parallel()
	pm, _ := inputOrderTestModel(t, "p1", true)
	var m tea.Model = *pm
	msgs := decodeAsReader(t, "\x1b[<3", "5;9")
	msgs = append(msgs, daemonMsg{})
	msgs = append(msgs, decodeAsReader(t, "0;35M")...)
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	if got := drainAll(pm); got != "" {
		t.Errorf("typed into pane = %q, want nothing", got)
	}
}

// TestUpdate_HeldKeysReachThePaneTheyWereTypedInto: when a non-terminal
// message moves focus while keys are held, they still land in the pane that
// was focused when they were typed — and the key that breaks the report goes
// where focus is now.
func TestUpdate_HeldKeysReachThePaneTheyWereTypedInto(t *testing.T) {
	t.Parallel()
	for _, end := range []struct {
		name string
		msgs []tea.Msg
		want string
	}{
		{"expiry", []tea.Msg{mouseTailExpireMsg{gen: 1}}, "p1:4 p1:2 "},
		{"a breaking key", []tea.Msg{tea.KeyPressMsg{Code: 'x', Text: "x"}}, "p1:4 p1:2 p2:x "},
	} {
		t.Run(end.name, func(t *testing.T) {
			t.Parallel()
			pm, tab := twoPaneModel()
			var m tea.Model = *pm
			for _, msg := range decodeAsReader(t, "\x1b[<3", "42") {
				m, _ = m.Update(msg)
			}
			m, _ = m.Update(daemonMsg{})
			tab.ActivePane = "p2" // the focus move that message made
			for _, msg := range end.msgs {
				m, _ = m.Update(msg)
			}
			if got := drainByPane(pm); got != end.want {
				t.Errorf("delivered = %q, want %q", got, end.want)
			}
		})
	}
}

// TestUpdate_HeldKeysGoBeforeTerminalInput: terminal input is ordered, so a paste
// or click after held keys releases them first, into the pane they were typed
// into, before that input can move focus. (A click needs more Model than this
// fixture builds; mouseTailFromTerminal pins that it releases the same way.)
func TestUpdate_HeldKeysGoBeforeTerminalInput(t *testing.T) {
	t.Parallel()
	pm, _ := twoPaneModel()
	var m tea.Model = *pm
	for _, msg := range decodeAsReader(t, "\x1b[<3", "42") {
		m, _ = m.Update(msg)
	}
	m, _ = m.Update(tea.PasteStartMsg{})
	if got := drainByPane(pm); got != "p1:4 p1:2 " {
		t.Errorf("delivered = %q, want both keys in p1", got)
	}
	if guard := m.(Model).mouseTail; guard.holding() {
		t.Error("the guard still holds keys after releasing them")
	}
}

// TestMouseTailFromTerminal pins which messages release held keys first.
func TestMouseTailFromTerminal(t *testing.T) {
	t.Parallel()
	release := []tea.Msg{
		tea.MouseClickMsg{}, tea.MouseReleaseMsg{}, tea.MouseWheelMsg{},
		tea.MouseMotionMsg{Button: tea.MouseLeft}, tea.PasteMsg{}, tea.PasteStartMsg{}, tea.PasteEndMsg{},
	}
	for _, msg := range release {
		if !mouseTailFromTerminal(msg) {
			t.Errorf("%T kept keys held; terminal input after them proves the tail is not coming", msg)
		}
	}
	keep := []tea.Msg{
		tea.KeyPressMsg{}, tea.KeyReleaseMsg{}, uv.UnknownEvent(""), mouseTailExpireMsg{},
		tea.MouseMotionMsg{Button: tea.MouseNone}, PaneOutputMsg{}, WorkspaceStateMsg{},
		clipboardPastedMsg{}, daemonMsg{},
	}
	for _, msg := range keep {
		if mouseTailFromTerminal(msg) {
			t.Errorf("%T released held keys; it can land inside a real tail", msg)
		}
	}
}

var key5 = tea.KeyPressMsg{Code: '5', Text: "5"}

// TestMouseTailGuard_LateKeyReleasesWhatIsHeld pins the time bound: past the
// window, a key that would continue the report releases the held keys (it then
// passes as ordinary input) instead of being held — the expiry tick may not
// have arrived yet.
func TestMouseTailGuard_LateKeyReleasesWhatIsHeld(t *testing.T) {
	t.Parallel()
	var g mouseTailGuard
	now := time.Unix(0, 0)
	g.arm(uv.UnknownEvent("\x1b[<3"), now, localFocusKey{})
	if v, _ := g.feed(key5, now); v != mouseTailHold {
		t.Fatalf("first key verdict = %v, want hold", v)
	}
	v, release := g.feed(key5, now.Add(mouseTailWindow+time.Millisecond))
	if v != mouseTailRelease || len(release) != 1 {
		t.Fatalf("late key = (%v, %d keys), want release of the 1 held", v, len(release))
	}
	if v, _ := g.feed(key5, now.Add(mouseTailWindow+2*time.Millisecond)); v != mouseTailPass {
		t.Fatalf("after release verdict = %v, want pass — the guard stayed armed", v)
	}
}

// TestMouseTailGuard_StaleExpiryIsIgnored keeps an old head's tick from
// releasing keys held for a newer head.
func TestMouseTailGuard_StaleExpiryIsIgnored(t *testing.T) {
	t.Parallel()
	var g mouseTailGuard
	now := time.Unix(0, 0)
	g.arm(uv.UnknownEvent("\x1b[<3"), now, localFocusKey{})
	old := g.gen
	g.arm(uv.UnknownEvent("\x1b[<3"), now, localFocusKey{})
	g.feed(key5, now)
	if got := g.expire(old); got != nil {
		t.Fatalf("stale expiry released %d keys", len(got))
	}
	if got := g.expire(g.gen); len(got) != 1 {
		t.Fatalf("current expiry released %d keys, want 1", len(got))
	}
}

// TestMouseTailGuard_OnlyMouseHeadsArm keeps other unknown sequences from
// arming the guard.
func TestMouseTailGuard_OnlyMouseHeadsArm(t *testing.T) {
	t.Parallel()
	for _, head := range []string{"\x1b[3", "\x1b[?1", "\x1b]11;", "\x1b[<35;90;35M", "\x1b[<;"} {
		var g mouseTailGuard
		now := time.Unix(0, 0)
		if g.arm(uv.UnknownEvent(head), now, localFocusKey{}) {
			t.Errorf("head %q armed the guard", head)
		}
		if v, _ := g.feed(key5, now); v != mouseTailPass {
			t.Errorf("head %q: key verdict = %v, want pass", head, v)
		}
	}
}

func TestSGRMouseReport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in               string
		prefix, complete bool
	}{
		{"", true, false},
		{"\x1b[", true, false},
		{"\x1b[<", true, false},
		{"\x1b[<35", true, false},
		{"\x1b[<35;90;35", true, false},
		{"\x1b[<35;90;35M", true, true},
		{"\x1b[<0;1;2m", true, true},
		{"\x1b[<35;90;35Mx", false, false},
		{"\x1b[<35;90M", false, false},
		{"\x1b[<;", false, false},
		{"\x1b[<1;2;3;4", false, false},
		{"\x1b[A", false, false},
	}
	for _, tt := range tests {
		p, c := sgrMouseReport(tt.in)
		if p != tt.prefix || c != tt.complete {
			t.Errorf("sgrMouseReport(%q) = (%v, %v), want (%v, %v)", tt.in, p, c, tt.prefix, tt.complete)
		}
	}
}
