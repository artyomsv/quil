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
// discards them. Anything that breaks the report, a new head, or the window
// running out hands the held keys back to be delivered in order.
type mouseTailGuard struct {
	seq  string            // the report so far, starting with ESC[<; empty = disarmed
	at   time.Time         // when the head arrived
	held []tea.KeyPressMsg // keys that continued the report, in arrival order
	gen  uint64            // bumped per head, so a stale expiry is ignored
}

// arm starts tracking a broken mouse-report head and reports whether ev was
// one; when it was, the caller schedules expiry for g.gen. The caller must
// first take and deliver anything still held for an earlier head (reset), or
// those keys would be held again behind this one.
func (g *mouseTailGuard) arm(ev uv.UnknownEvent, now time.Time) bool {
	g.reset()
	s := string(ev)
	if prefix, complete := sgrMouseReport(s); prefix && !complete {
		g.seq, g.at = s, now
		g.gen++
		return true
	}
	return false
}

// feed classifies key against the armed report. On mouseTailRelease the
// returned keys — everything held, then key itself — must be delivered in
// order; they never pass through the guard again, because it is disarmed.
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
	return mouseTailRelease, append(g.reset(), key)
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

// mouseTailKeepsHolding lists the messages that may arrive while keys are held
// without the keys being released first. Held keys are delivered by replaying
// them through Update, so they go wherever input is focused at REPLAY time;
// any message that could move that focus — a click, a paste, a workspace
// broadcast switching tabs, a notification jump — must therefore see them
// delivered first, as if they had never been held. Only messages that cannot
// move focus are listed: the guard's own three, timer ticks, pane output, and
// buttonless motion (sidebar hover). Everything else releases, so a new
// message type is safe by default. The listed ones are what can land between
// a split report's head and its tail without breaking the tail apart.
func mouseTailKeepsHolding(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.KeyPressMsg, uv.UnknownEvent, mouseTailExpireMsg:
		return true
	case PaneOutputMsg, listenContinueMsg, spinnerTickMsg, workSpinnerTickMsg,
		sidebarTickMsg, notesTickMsg, resourceTickMsg, sizePollMsg, resizeTickMsg:
		return true
	case tea.MouseMotionMsg:
		return msg.Button == tea.MouseNone
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

// replayKeys delivers keys the mouse-tail guard held, in order, through the
// normal Update path. The guard is disarmed by then, so none is held again.
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
