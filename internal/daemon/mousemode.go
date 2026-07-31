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

// mouseModeState is the set of DEC private modes a child app has enabled,
// tracked one bool per mode. Keeping them separate (rather than collapsing into
// a single "tracking" bool) mirrors the TUI's PaneModel flags and is what makes
// disabling a mode that was never set safe: `CSI ? 1000 l` cannot clear a
// `?1002`-driven tracking state. sgr (?1006) is an encoding modifier, not a
// tracking mode. bracketedPaste (?2004) is the one non-mouse mode tracked here:
// it needs the same daemon-side lifetime as the mouse modes (see the header
// comment) and rides the same scanner, so it lives in this struct rather than
// a parallel one.
type mouseModeState struct {
	x10            bool // ?9   (X10 compatibility)
	normal         bool // ?1000 (normal tracking)
	button         bool // ?1002 (button-event tracking)
	any            bool // ?1003 (any-event tracking)
	sgr            bool // ?1006 (SGR extended encoding)
	bracketedPaste bool // ?2004 (bracketed paste)
}

// tracking reports whether any mouse-tracking mode is active — i.e. the child
// app wants to handle mouse events itself. SGR alone is not tracking.
func (m mouseModeState) tracking() bool {
	return m.x10 || m.normal || m.button || m.any
}

// maxModeSeqLen bounds both how far back a chunk is searched for the start of
// an unterminated sequence and how many bytes are carried into the next scan.
// It must exceed the longest sequence we care about, or a split inside a long
// one is silently dropped: every tracked mode combined is
// `ESC [ ? 9;1000;1002;1003;1006;2004` = 29 bytes before the final byte, and a
// real app may list untracked modes (1049, 25, 2026, …) in the same run. 64
// leaves room for that while still bounding the carry — a longer digit/`;` run
// is dropped rather than carried indefinitely. Do not shrink this below 29
// without re-deriving it: the previous value of 24 sat under the all-modes
// figure and lost exactly those splits.
const maxModeSeqLen = 64

// scanMouseModes updates the per-mode state from a raw output chunk. It walks
// every `CSI ? <params> (h|l)` sequence and applies each set/reset that touches
// a tracked mode. Tracking modes: 9 (X10), 1000 (normal), 1002 (button-event),
// 1003 (any-event). SGR encoding: 1006. Bracketed paste: 2004.
// Combined-parameter sequences (e.g. `CSI ? 1000 ; 1006 h`) are handled.
//
// A sequence split across chunk boundaries is returned as tail — the trailing
// unterminated `CSI ?` prefix, if any — so the caller can prepend it to the
// next chunk and the enable is not lost. This matters most for ?2004: a missed
// mouse enable merely falls back to no-forwarding, but a missed paste enable
// would make a reattached client inject a paste as raw keystrokes. The
// returned tail aliases data; callers that retain it across the call must
// copy it.
func scanMouseModes(m mouseModeState, data []byte) (_ mouseModeState, tail []byte) {
	tail = incompleteModeSeq(data)
	if !bytes.Contains(data, privateModeIntro) {
		return m, tail
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
			break // incomplete sequence at end of chunk — carried via tail
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
				case "2004":
					m.bracketedPaste = set
				}
			}
		}
		i = j + 1
	}
	return m, tail
}

// incompleteModeSeq returns the trailing bytes of data that form an
// unterminated private-mode sequence prefix — `ESC`, `ESC [`, `ESC [ ?`, or
// `ESC [ ? <digits/;>` with no final byte yet — or nil when the chunk ends
// cleanly. Only the last maxModeSeqLen bytes are considered: an ESC further
// back either completed its sequence or heads a parameter run too long to be
// a tracked mode. A carried non-mode prefix (e.g. a bare ESC that turns out
// to start a color sequence) is harmless — rescanning it with the next chunk
// matches nothing.
func incompleteModeSeq(data []byte) []byte {
	start := len(data) - maxModeSeqLen
	if start < 0 {
		start = 0
	}
	esc := bytes.LastIndexByte(data[start:], 0x1b)
	if esc < 0 {
		return nil
	}
	rest := data[start+esc:]
	for k := 1; k < len(rest); k++ {
		var ok bool
		switch k {
		case 1:
			ok = rest[k] == '['
		case 2:
			ok = rest[k] == '?'
		default:
			ok = rest[k] == ';' || (rest[k] >= '0' && rest[k] <= '9')
		}
		if !ok {
			return nil // terminated (final byte) or not a private-mode sequence
		}
	}
	return rest
}
