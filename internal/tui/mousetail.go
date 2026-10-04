package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// mouseTailWindow bounds how long the guard holds keys after a broken
// mouse-report head. The reader flushes the head after its 50 ms escape timeout
// and the tail is the very next input, so both reach Update back to back even
// when Update itself is running late. The window is also the longest a key the
// user really typed can be delayed, when a head's tail never comes.
const mouseTailWindow = 500 * time.Millisecond

// mouseTailExpireMsg releases the keys held for the head of generation gen if
// its report has not completed by then.
type mouseTailExpireMsg struct{ gen uint64 }

// mouseTailVerdict says what Update does with a key press the guard has seen.
type mouseTailVerdict int

const (
	mouseTailPass     mouseTailVerdict = iota // not the guard's business: handle normally
	mouseTailHold                             // may continue the report: held, deliver nothing yet
	mouseTailComplete                         // finished the report: drop it and everything held
	mouseTailRelease                          // the report is broken: deliver the returned keys in order
)

// mouseTailGuard drops the tail of an SGR mouse report that the terminal
// reader split in two.
//
// While the project sidebar or a context menu is painted, Quil asks for
// all-motion reporting, so every pointer move arrives as `ESC[<35;X;YM`. The
// reader waits only DefaultEscTimeout (50 ms) for a sequence to complete; when
// a report arrives in two reads further apart than that, it flushes the head
// (`ESC[<3`) as a uv.UnknownEvent and decodes the tail (`5;90;35M`) as plain
// key presses — which Quil then typed into the active pane. Observed as stray
// "5;90;35M" text in a pane's input line.
//
// A digit after the head is not proof of mouse data: the user may be typing
// numbers while a head's tail never comes. So keys that continue the report
// are HELD, not dropped, and only the final M/m — which completes the report —
// discards them. Anything that breaks the report, a new head, terminal input
// such as a click, or the window running out hands the held keys back to be
// delivered in order (Model.deliverHeld).
type mouseTailGuard struct {
	seq   string            // the report so far, starting with ESC[<; empty = disarmed
	at    time.Time         // when the head arrived
	held  []tea.KeyPressMsg // keys that continued the report, in arrival order
	gen   uint64            // bumped per head, so a stale expiry is ignored
	focus localFocusKey     // where input was focused when the head arrived
}

// arm starts tracking a broken mouse-report head and reports whether ev was
// one; when it was, the caller schedules expiry for g.gen. focus is where
// input is focused now — where held keys belong. The caller must first take
// and deliver anything still held for an earlier head (reset), or those keys
// would be held again behind this one.
func (g *mouseTailGuard) arm(ev uv.UnknownEvent, now time.Time, focus localFocusKey) bool {
	g.reset()
	s := string(ev)
	if prefix, complete := sgrMouseReport(s); prefix && !complete {
		g.seq, g.at, g.focus = s, now, focus
		g.gen++
		return true
	}
	return false
}

// feed classifies key against the armed report. On mouseTailRelease the
// returned keys are what was held, to be delivered before key itself; the
// guard is disarmed, so key then passes through as ordinary input.
func (g *mouseTailGuard) feed(key tea.KeyPressMsg, now time.Time) (mouseTailVerdict, []tea.KeyPressMsg) {
	if g.seq == "" {
		return mouseTailPass, nil
	}
	if now.Sub(g.at) <= mouseTailWindow && len(key.Text) == 1 && key.Mod&^tea.ModShift == 0 {
		next := g.seq + key.Text
		if prefix, complete := sgrMouseReport(next); prefix {
			if complete {
				g.reset()
				return mouseTailComplete, nil
			}
			g.seq = next
			g.held = append(g.held, key)
			return mouseTailHold, nil
		}
	}
	return mouseTailRelease, g.reset()
}

// expire hands back the keys held for generation gen when its report never
// completed. A stale gen — the report completed, broke, or a newer head took
// over — returns nothing.
func (g *mouseTailGuard) expire(gen uint64) []tea.KeyPressMsg {
	if g.seq == "" || gen != g.gen {
		return nil
	}
	return g.reset()
}

// holding reports whether keys are waiting on the armed report.
func (g *mouseTailGuard) holding() bool { return len(g.held) > 0 }

// mouseTailFromTerminal reports whether msg is terminal input that releases
// held keys before it is handled. The terminal reader delivers input strictly
// in order, so once a click, a paste or a drag arrives, the held keys' tail
// can no longer follow — and they were typed BEFORE that input, so they go
// first. Key presses are left out because feed classifies them, and key
// releases because win32-input-mode interleaves one after every key of a
// real tail. Anything not from the terminal (a daemon broadcast, a Cmd
// result) may land in the middle of a tail and must not break it apart.
func mouseTailFromTerminal(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg,
		tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg:
		return true
	case tea.MouseMotionMsg:
		return msg.Button != tea.MouseNone
	}
	return false
}

// reset disarms the guard and returns what it was holding.
func (g *mouseTailGuard) reset() []tea.KeyPressMsg {
	held := g.held
	g.seq, g.held = "", nil
	return held
}

// expireCmd schedules the release of keys held for the current head.
func (g *mouseTailGuard) expireCmd() tea.Cmd {
	gen := g.gen
	return tea.Tick(mouseTailWindow, func(time.Time) tea.Msg { return mouseTailExpireMsg{gen: gen} })
}

// deliverHeld delivers keys the mouse-tail guard held where they were typed.
// While a non-terminal message moved nothing, they replay through Update like
// any key. If one moved focus during the hold — another client switching
// tabs, a notification jump — replaying would type them into the new pane, so
// they go straight to the pane that was focused when the head arrived. Held
// keys are only ever single printable characters, so their bytes are their
// text.
func (m Model) deliverHeld(keys []tea.KeyPressMsg) (Model, tea.Cmd) {
	if len(keys) == 0 {
		return m, nil
	}
	if target := m.mouseTail.focus; m.localFocus() != target {
		for _, k := range keys {
			m.enqueueInput(target.pane, []byte(k.Text))
		}
		return m, nil
	}
	return m.replayKeys(keys)
}

// replayKeys delivers keys through the normal Update path. The guard is
// disarmed by then, so none is held again.
func (m Model) replayKeys(keys []tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, k := range keys {
		next, cmd := m.Update(k)
		if mm, ok := next.(Model); ok {
			m = mm
		}
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// sgrMouseReport classifies s against the SGR mouse grammar
// `ESC [ < digits ; digits ; digits (M|m)`: prefix is true when s is a prefix
// of such a report (or one in full), complete when it is a whole report.
func sgrMouseReport(s string) (prefix, complete bool) {
	const intro = "\x1b[<"
	if len(s) < len(intro) {
		return intro[:len(s)] == s, false
	}
	if s[:len(intro)] != intro {
		return false, false
	}
	field, digits := 0, 0 // field index 0..2, digits in the current field
	for i := len(intro); i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == ';' && digits > 0 && field < 2:
			field++
			digits = 0
		case (c == 'M' || c == 'm') && digits > 0 && field == 2:
			return i == len(s)-1, i == len(s)-1
		default:
			return false, false
		}
	}
	return true, false
}
