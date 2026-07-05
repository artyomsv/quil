package tui

import "bytes"

// OSC window/icon-title sequences (OSC 0, 1, 2) are stripped from a pane's PTY
// output before it reaches the charmbracelet/x/vt emulator.
//
// WHY: x/vt's OSC string parser terminates on byte 0x9C — the C1 "String
// Terminator" (ST) — even when 0x9C is a UTF-8 continuation byte. claude-code
// sets its window title to "✳ Claude Code" (✳ = U+2733 = E2 9C B3); the 0x9C ends
// the OSC early and the tail ("… Claude Code") spills into the VISIBLE grid,
// producing the doubled logo ("Claude CodClaude Code …") and the input-line leak
// ("AAAude Code") reported on macOS Terminal.app. Quil renders its own tab titles
// and never displays the child's window title, so dropping OSC 0/1/2 is safe and
// removes the leak at the source. OSC 7 (cwd) and every other OSC are passed
// through untouched — they don't feed the title and Quil's OSC 7 callback needs
// them. (A fully general fix belongs upstream in the ansi parser: raw C1 bytes
// must not be treated as controls inside a UTF-8 stream.)
//
// Scope note: only the 7-bit "ESC ]" OSC introducer is recognized, and DCS/APC/PM
// string sequences (ESC P / ESC _ / ESC ^) are NOT parsed — their payloads flow
// through the ground state, so a payload literally containing "ESC ] 0 ;" could be
// mis-stripped. claude-code emits neither, so this stays theoretical.
//
// The filter is a byte-stream state machine so a title OSC split across two
// coalesced output chunks is still stripped. One instance per pane (PaneModel).

type oscFilterState int

const (
	oscNormal  oscFilterState = iota // ground
	oscEsc                           // last byte was ESC
	oscMaybe                         // saw "ESC ]", buffering the Ps number to decide
	oscDrop                          // inside a title OSC (0/1/2), dropping to terminator
	oscDropEsc                       // in oscDrop, saw ESC (checking for "\" = ST)
)

// oscTitleFilter strips OSC 0/1/2 from a byte stream across chunk boundaries.
type oscTitleFilter struct {
	state   oscFilterState
	buf     []byte // buffered "ESC ] <digits>" while undecided
	dropped int    // bytes discarded in the current title OSC body (bounds over-drop)
}

// maxOSCPrefix bounds the undecided buffer: a real OSC number is at most a few
// digits before ';'. Anything longer is malformed — flush it and resync.
const maxOSCPrefix = 8

// maxOSCBody bounds how much a single title OSC may discard. A well-formed title
// ends in BEL or ESC-\; a stream that never sends one (e.g. a hostile child that
// closes the title with a bare C1 ST 0x9C — which we deliberately do NOT treat as
// a terminator, since 0x9C also occurs mid-UTF-8) would otherwise make the filter
// swallow unbounded downstream output. Cap it and resync to ground. 8 KiB is far
// beyond any real window title.
const maxOSCBody = 8 << 10

// Filter returns the bytes to hand the emulator, with OSC 0/1/2 removed.
func (f *oscTitleFilter) Filter(data []byte) []byte {
	// Fast path: in the ground state a chunk with no ESC cannot contain an OSC,
	// so there is nothing to strip — skip the alloc + byte-copy entirely. This is
	// the common case on a busy pane.
	if f.state == oscNormal && bytes.IndexByte(data, 0x1b) == -1 {
		return data
	}
	out := make([]byte, 0, len(data))
	for _, b := range data {
		switch f.state {
		case oscNormal:
			if b == 0x1b {
				f.state = oscEsc
			} else {
				out = append(out, b)
			}

		case oscEsc:
			if b == ']' {
				f.state = oscMaybe
				f.buf = append(f.buf[:0], 0x1b, ']')
			} else {
				// Not an OSC — emit the held ESC and reprocess this byte.
				out = append(out, 0x1b)
				f.state = oscNormal
				if b == 0x1b {
					f.state = oscEsc
				} else {
					out = append(out, b)
				}
			}

		case oscMaybe:
			switch {
			case b >= '0' && b <= '9':
				f.buf = append(f.buf, b)
				if len(f.buf) > maxOSCPrefix {
					out = append(out, f.buf...) // malformed — flush, resync
					f.buf = f.buf[:0]
					f.state = oscNormal
				}
			case b == ';':
				if n := oscNumber(f.buf[2:]); n == 0 || n == 1 || n == 2 {
					f.buf = f.buf[:0] // title OSC — drop the prefix and the body
					f.dropped = 0
					f.state = oscDrop
				} else {
					out = append(out, f.buf...) // other OSC — pass through
					out = append(out, b)
					f.buf = f.buf[:0]
					f.state = oscNormal
				}
			default:
				// Not a valid OSC number — flush buffered prefix + this byte.
				out = append(out, f.buf...)
				f.buf = f.buf[:0]
				f.state = oscNormal
				if b == 0x1b {
					f.state = oscEsc
				} else {
					out = append(out, b)
				}
			}

		case oscDrop:
			f.dropped++
			switch {
			case f.dropped > maxOSCBody:
				f.state = oscNormal // unterminated/pathological OSC — resync
			case b == 0x07: // BEL terminator
				f.state = oscNormal
			case b == 0x1b:
				f.state = oscDropEsc
			}
			// else: drop the byte (OSC body, including a stray 0x9C)

		case oscDropEsc:
			f.dropped++
			switch {
			case f.dropped > maxOSCBody:
				f.state = oscNormal
			case b == '\\': // ST terminator (ESC \)
				f.state = oscNormal
			case b == 0x1b:
				// stay in oscDropEsc
			default:
				f.state = oscDrop
			}
		}
	}
	return out
}

// oscNumber parses the decimal digits of an OSC identifier. Empty ⇒ 0
// (bare "ESC ] ;" means OSC 0). Returns -1 on non-digit input.
func oscNumber(digits []byte) int {
	if len(digits) == 0 {
		return 0
	}
	n := 0
	for _, d := range digits {
		if d < '0' || d > '9' {
			return -1
		}
		n = n*10 + int(d-'0')
	}
	return n
}
