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

// focusMoverMsg stands for any message that is not on the keeps-holding list —
// a click, a broadcast, a notification jump. Update ignores it; the test moves
// focus itself, the way such a message would while it is handled.
type focusMoverMsg struct{}

// TestUpdate_HeldKeysReachThePaneTheyWereTypedInto: keys held after an
// abandoned head are delivered before a message that may move focus, so a
// click on another pane within the hold window cannot redirect them.
func TestUpdate_HeldKeysReachThePaneTheyWereTypedInto(t *testing.T) {
	t.Parallel()
	p1, p2 := NewPaneModel("p1", 1024), NewPaneModel("p2", 1024)
	tab := NewTabModel("tab-1", "t")
	tab.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p1), Right: NewLeaf(p2)}
	tab.ActivePane = "p1"
	pm := &Model{projects: oneProject(tab), client: &fakeSender{}, inputCh: make(chan paneInput, inputForwardBuffer)}

	var m tea.Model = *pm
	for _, msg := range decodeAsReader(t, "\x1b[<3", "42") {
		m, _ = m.Update(msg)
	}
	m, _ = m.Update(focusMoverMsg{})
	tab.ActivePane = "p2" // the focus move that message made
	m, _ = m.Update(mouseTailExpireMsg{gen: m.(Model).mouseTail.gen})

	got := ""
	for {
		select {
		case in := <-pm.inputCh:
			got += in.paneID + ":" + string(in.data) + " "
			continue
		default:
		}
		break
	}
	if got != "p1:4 p1:2 " {
		t.Errorf("delivered = %q, want both keys in p1", got)
	}
}

// TestUpdate_HeldKeysGoBeforeAPaste keeps typed order across a release: the
// held digits reach the pane before the paste that released them.
func TestUpdate_HeldKeysGoBeforeAPaste(t *testing.T) {
	t.Parallel()
	pm, _ := inputOrderTestModel(t, "p1", true)
	var m tea.Model = *pm
	for _, msg := range decodeAsReader(t, "\x1b[<3", "42") {
		m, _ = m.Update(msg)
	}
	m, _ = m.Update(clipboardPastedMsg{text: "P", paneID: "p1"})
	if got := drainAll(pm); got != "42P" {
		t.Errorf("typed into pane = %q, want %q", got, "42P")
	}
	if guard := m.(Model).mouseTail; guard.holding() {
		t.Error("the guard still holds keys after releasing them")
	}
}

// TestMouseTailKeepsHolding pins which messages may land between a split
// report's head and tail without releasing what is held.
func TestMouseTailKeepsHolding(t *testing.T) {
	t.Parallel()
	keep := []tea.Msg{
		tea.KeyPressMsg{}, uv.UnknownEvent(""), mouseTailExpireMsg{}, PaneOutputMsg{},
		listenContinueMsg{}, spinnerTickMsg{}, sidebarTickMsg{}, resourceTickMsg{},
		tea.MouseMotionMsg{Button: tea.MouseNone},
	}
	for _, msg := range keep {
		if !mouseTailKeepsHolding(msg) {
			t.Errorf("%T released held keys; it cannot move focus", msg)
		}
	}
	release := []tea.Msg{
		tea.MouseClickMsg{}, tea.MouseReleaseMsg{}, tea.MouseWheelMsg{},
		tea.MouseMotionMsg{Button: tea.MouseLeft}, tea.PasteMsg{}, WorkspaceStateMsg{},
		clipboardPastedMsg{}, focusMoverMsg{},
	}
	for _, msg := range release {
		if mouseTailKeepsHolding(msg) {
			t.Errorf("%T kept keys held; it may move focus", msg)
		}
	}
}

var key5 = tea.KeyPressMsg{Code: '5', Text: "5"}

// TestMouseTailGuard_LateKeyReleasesWhatIsHeld pins the time bound: past the
// window, a key that would continue the report releases the held keys and
// itself instead of being held — the expiry tick may not have arrived yet.
func TestMouseTailGuard_LateKeyReleasesWhatIsHeld(t *testing.T) {
	t.Parallel()
	var g mouseTailGuard
	now := time.Unix(0, 0)
	g.arm(uv.UnknownEvent("\x1b[<3"), now)
	if v, _ := g.feed(key5, now); v != mouseTailHold {
		t.Fatalf("first key verdict = %v, want hold", v)
	}
	v, release := g.feed(key5, now.Add(mouseTailWindow+time.Millisecond))
	if v != mouseTailRelease || len(release) != 2 {
		t.Fatalf("late key = (%v, %d keys), want release of 2", v, len(release))
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
	g.arm(uv.UnknownEvent("\x1b[<3"), now)
	old := g.gen
	g.arm(uv.UnknownEvent("\x1b[<3"), now)
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
		if g.arm(uv.UnknownEvent(head), now) {
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
