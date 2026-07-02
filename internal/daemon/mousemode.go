package daemon

import "bytes"

// Mouse-mode tracking lives in the daemon because it sees a pane's full output
// stream from spawn. A TUI client that attaches to an already-running pane
// (the normal case — the daemon outlives TUI restarts) never replays the
// one-time DEC mouse-enable sequences the app emitted at startup, so it cannot
// reconstruct the state from its own emulator. The daemon scans the stream and
// reports the authoritative state in the workspace snapshot; the TUI uses it to
// decide whether a mouse wheel event should be forwarded to the child app
// (opencode, vim, …) instead of scrolling Quil's local scrollback.

// privateModeIntro is the cheap gate: a chunk without a CSI private-mode
// introducer can't toggle any mouse mode, so the full scan is skipped.
var privateModeIntro = []byte("\x1b[?")

// mouseModeState is the set of DEC mouse modes a child app has enabled, tracked
// one bool per mode. Keeping them separate (rather than collapsing into a single
// "tracking" bool) mirrors the TUI's PaneModel flags and is what makes disabling
// a mode that was never set safe: `CSI ? 1000 l` cannot clear a `?1002`-driven
// tracking state. sgr (?1006) is an encoding modifier, not a tracking mode.
type mouseModeState struct {
	x10    bool // ?9   (X10 compatibility)
	normal bool // ?1000 (normal tracking)
	button bool // ?1002 (button-event tracking)
	any    bool // ?1003 (any-event tracking)
	sgr    bool // ?1006 (SGR extended encoding)
}

// tracking reports whether any mouse-tracking mode is active — i.e. the child
// app wants to handle mouse events itself. SGR alone is not tracking.
func (m mouseModeState) tracking() bool {
	return m.x10 || m.normal || m.button || m.any
}

// scanMouseModes updates the per-mode state from a raw output chunk. It walks
// every `CSI ? <params> (h|l)` sequence and applies each set/reset that touches
// a tracked mode. Tracking modes: 9 (X10), 1000 (normal), 1002 (button-event),
// 1003 (any-event). SGR encoding: 1006. Combined-parameter sequences (e.g.
// `CSI ? 1000 ; 1006 h`) are handled. Sequences split across chunk boundaries
// are ignored (negligible in practice — apps emit the mouse-enable burst as one
// write); a miss only falls back to no-forwarding, never a wrong-forwarding.
func scanMouseModes(m mouseModeState, data []byte) mouseModeState {
	if !bytes.Contains(data, privateModeIntro) {
		return m
	}
	i := 0
	for i < len(data) {
		if data[i] != 0x1b {
			i++
			continue
		}
		if i+2 >= len(data) || data[i+1] != '[' || data[i+2] != '?' {
			i++
			continue
		}
		// Parse the numeric/`;` parameter run up to the final byte.
		j := i + 3
		paramStart := j
		for j < len(data) && (data[j] == ';' || (data[j] >= '0' && data[j] <= '9')) {
			j++
		}
		if j >= len(data) {
			break // incomplete sequence at end of chunk
		}
		final := data[j]
		if final == 'h' || final == 'l' {
			set := final == 'h'
			for _, param := range bytes.Split(data[paramStart:j], []byte{';'}) {
				switch string(param) {
				case "9":
					m.x10 = set
				case "1000":
					m.normal = set
				case "1002":
					m.button = set
				case "1003":
					m.any = set
				case "1006":
					m.sgr = set
				}
			}
		}
		i = j + 1
	}
	return m
}
