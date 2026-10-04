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
// delivered in order (Model.deliverHeld), each to where it was typed.
type mouseTailGuard struct {
	seq  string    // the report so far, starting with ESC[<; empty = disarmed
	at   time.Time // when the head arrived
	held []heldKey // keys that continued the report, in arrival order
	gen  uint64    // bumped per head, so a stale expiry is ignored

	// redirecting is true while deliverHeld replays keys to the pane they were
	// typed into after focus moved. Those keys predate the move, so Update
	// must not treat them as the user acknowledging the newly focused pane.
	redirecting bool
}

// heldKey is a key the guard held, with where it would have gone when it was
// typed: the input focus, its pane resolved through any typing guard active at
// that moment (Model.effectiveFocus).
type heldKey struct {
	key    tea.KeyPressMsg
	target localFocusKey
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

// armed reports whether a head is waiting for its tail.
func (g *mouseTailGuard) armed() bool { return g.seq != "" }

// feed classifies key against the armed report; target is where key would
// go if it were not held. On mouseTailRelease the returned keys are what was
// held, to be delivered before key itself; the guard is disarmed, so key then
// passes through as ordinary input.
func (g *mouseTailGuard) feed(key tea.KeyPressMsg, now time.Time, target localFocusKey) (mouseTailVerdict, []heldKey) {
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
			g.held = append(g.held, heldKey{key: key, target: target})
			return mouseTailHold, nil
		}
	}
	return mouseTailRelease, g.reset()
}

// expire hands back the keys held for generation gen when its report never
// completed. A stale gen — the report completed, broke, or a newer head took
// over — returns nothing.
func (g *mouseTailGuard) expire(gen uint64) []heldKey {
	if g.seq == "" || gen != g.gen {
		return nil
	}
	return g.reset()
}

// mouseTailFromTerminal reports whether msg is terminal input that ends an
// armed report before msg is handled. The terminal reader delivers input
// strictly in order, so once a click, a paste or ANY other mouse report
// arrives — buttonless motion included — the head's tail can no longer
// follow, and keys held so far were typed BEFORE that input, so they go
// first. Key presses are left out because feed classifies them, and key
// releases because win32-input-mode interleaves one after every key of a real
// tail. Anything not from the terminal (a daemon broadcast, a Cmd result) may
// land in the middle of a tail and must not break it apart.
func mouseTailFromTerminal(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg,
		tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg:
		return true
	}
	return false
}

// reset disarms the guard and returns what it was holding.
func (g *mouseTailGuard) reset() []heldKey {
	held := g.held
	g.seq, g.held = "", nil
	return held
}

// expireCmd schedules the release of keys held for the current head.
func (g *mouseTailGuard) expireCmd() tea.Cmd {
	gen := g.gen
	return tea.Tick(mouseTailWindow, func(time.Time) tea.Msg { return mouseTailExpireMsg{gen: gen} })
}

// effectiveFocus is where a typed key would go right now: the input focus,
// with its pane resolved through the typing guard.
func (m Model) effectiveFocus() localFocusKey {
	f := m.localFocus()
	f.pane = m.guardedInputTarget(f.pane)
	return f
}

// deliverHeld delivers keys the mouse-tail guard held, each where it was
// typed. They always replay through Update, so whatever surface owns the
// keyboard now — a dialog, a rename, the palette — still gets them first. A
// key whose target is still where typing goes replays as is. If a
// non-terminal message moved focus after the key was held (another client
// switching tabs, a notification jump), or the typing guard that aimed it has
// since expired, replaying alone would send it elsewhere — so that key
// replays under the typing guard aimed at its own pane: the same redirect,
// with the same fallback to the current pane when that one is gone, that
// protects keys typed through a remote tab switch. The guard's own state is
// put back afterwards, so this borrows it without arming or retiring it for
// the user's next key.
func (m Model) deliverHeld(keys []heldKey) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, h := range keys {
		var cmd tea.Cmd
		if h.target.pane == "" || h.target == m.effectiveFocus() {
			m, cmd = m.replayKeys([]tea.KeyPressMsg{h.key})
		} else {
			prevPane, prevAt := m.guardPaneID, m.remoteSwitchAt
			m.guardPaneID, m.remoteSwitchAt = h.target.pane, m.clock()
			m.mouseTail.redirecting = true
			m, cmd = m.replayKeys([]tea.KeyPressMsg{h.key})
			m.mouseTail.redirecting = false
			m.guardPaneID, m.remoteSwitchAt = prevPane, prevAt
		}
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
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
