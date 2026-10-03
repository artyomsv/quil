package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// mouseTailWindow bounds how long after a broken mouse-report head the guard
// still swallows its tail. The reader flushes the head after its 50 ms escape
// timeout and the tail is the very next input, so both reach Update back to
// back even when Update itself is running late; the window only has to stop a
// dangling head from eating digits the user types much later.
const mouseTailWindow = time.Second

// mouseTailGuard swallows the tail of an SGR mouse report that the terminal
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
// The head is the evidence: only a key that CONTINUES the report it started is
// swallowed, so a real key press disarms the guard and is handled normally.
type mouseTailGuard struct {
	seq string    // the report so far, starting with ESC[<; empty = disarmed
	at  time.Time // when the head arrived
}

// arm starts tracking a broken mouse-report head. Anything that is not a
// proper prefix of an SGR mouse report leaves the guard disarmed.
func (g *mouseTailGuard) arm(ev uv.UnknownEvent, now time.Time) {
	s := string(ev)
	if prefix, complete := sgrMouseReport(s); prefix && !complete {
		g.seq, g.at = s, now
		return
	}
	g.seq = ""
}

// swallow reports whether key is the next byte of the armed report and must
// be dropped. Any other key disarms the guard.
func (g *mouseTailGuard) swallow(key tea.KeyPressMsg, now time.Time) bool {
	if g.seq == "" {
		return false
	}
	if now.Sub(g.at) > mouseTailWindow || len(key.Text) != 1 || key.Mod&^tea.ModShift != 0 {
		g.seq = ""
		return false
	}
	next := g.seq + key.Text
	prefix, complete := sgrMouseReport(next)
	switch {
	case !prefix:
		g.seq = ""
		return false
	case complete:
		g.seq = ""
	default:
		g.seq = next
	}
	return true
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
