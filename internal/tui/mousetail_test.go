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
// must not be typed into the pane, while real keys around it must be.
func TestUpdate_SplitMouseReportNeverReachesThePane(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"split after the button code", []string{"\x1b[<3", "5;90;35M"}, ""},
		{"split before the final byte", []string{"\x1b[<35;90;35", "M"}, ""},
		{"release report", []string{"\x1b[<0;1", "2;7m"}, ""},
		{"typing after the tail", []string{"\x1b[<3", "5;90;35Mab"}, "ab"},
		{"real key interrupts the tail", []string{"\x1b[<3", "5;9x"}, "x"},
		{"digits with no head", []string{"5;90;35M"}, "5;90;35M"},
		{"whole report is not typed", []string{"\x1b[<35;90;35Mq"}, "q"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pm, _ := inputOrderTestModel(t, "p1", true)
			var m tea.Model = *pm
			for _, msg := range decodeAsReader(t, tt.chunks...) {
				m, _ = m.Update(msg)
			}
			if got := drainAll(pm); got != tt.want {
				t.Errorf("typed into pane = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMouseTailGuard_StaleHeadDoesNotEatLaterTyping pins the time bound: a
// head whose tail never came must not swallow digits typed long after it.
func TestMouseTailGuard_StaleHeadDoesNotEatLaterTyping(t *testing.T) {
	t.Parallel()
	var g mouseTailGuard
	now := time.Unix(0, 0)
	g.arm(uv.UnknownEvent("\x1b[<3"), now)
	if g.swallow(tea.KeyPressMsg{Code: '5', Text: "5"}, now.Add(mouseTailWindow+time.Millisecond)) {
		t.Fatal("a key arriving after mouseTailWindow was swallowed")
	}
	if g.swallow(tea.KeyPressMsg{Code: '5', Text: "5"}, now.Add(mouseTailWindow+2*time.Millisecond)) {
		t.Fatal("the guard stayed armed after timing out")
	}
}

// TestMouseTailGuard_OnlyMouseHeadsArm keeps other unknown sequences from
// arming the guard.
func TestMouseTailGuard_OnlyMouseHeadsArm(t *testing.T) {
	t.Parallel()
	for _, head := range []string{"\x1b[3", "\x1b[?1", "\x1b]11;", "\x1b[<35;90;35M", "\x1b[<;"} {
		var g mouseTailGuard
		now := time.Unix(0, 0)
		g.arm(uv.UnknownEvent(head), now)
		if g.swallow(tea.KeyPressMsg{Code: '5', Text: "5"}, now) {
			t.Errorf("head %q armed the guard", head)
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
