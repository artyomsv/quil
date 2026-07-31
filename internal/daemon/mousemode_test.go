package daemon

import "testing"

func TestScanMouseModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		start    mouseModeState
		data     string
		want     mouseModeState
		wantTail string
	}{
		{"plain text no change", mouseModeState{}, "hello world\r\n", mouseModeState{}, ""},
		{"colors only no change", mouseModeState{normal: true, sgr: true}, "\x1b[31mred\x1b[0m", mouseModeState{normal: true, sgr: true}, ""},
		{"opencode startup burst separate", mouseModeState{},
			"\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h",
			mouseModeState{normal: true, button: true, any: true, sgr: true}, ""},
		{"combined params", mouseModeState{}, "\x1b[?1000;1006h", mouseModeState{normal: true, sgr: true}, ""},
		{"normal tracking only, no sgr", mouseModeState{}, "\x1b[?1000h", mouseModeState{normal: true}, ""},
		{"x10 mode", mouseModeState{}, "\x1b[?9h", mouseModeState{x10: true}, ""},
		{"reset tracking", mouseModeState{normal: true, sgr: true}, "\x1b[?1000l\x1b[?1006l", mouseModeState{}, ""},
		{"reset only sgr keeps tracking", mouseModeState{normal: true, sgr: true}, "\x1b[?1006l", mouseModeState{normal: true}, ""},
		// Regression guard for the per-mode design: resetting a mode that was
		// never set must NOT clear a different, active tracking mode.
		{"reset unset mode preserves others", mouseModeState{button: true}, "\x1b[?1000l", mouseModeState{button: true}, ""},
		{"cursor-hide does not trigger", mouseModeState{}, "\x1b[?25l", mouseModeState{}, ""},
		{"bracketed-paste tracked", mouseModeState{}, "\x1b[?2004h", mouseModeState{bracketedPaste: true}, ""},
		{"bracketed-paste reset", mouseModeState{bracketedPaste: true}, "\x1b[?2004l", mouseModeState{}, ""},
		{"alt-screen does not trigger", mouseModeState{}, "\x1b[?1049h", mouseModeState{}, ""},
		{"mouse set amid other output", mouseModeState{},
			"text\x1b[?25l more\x1b[?1002h\x1b[?1006h done", mouseModeState{button: true, sgr: true}, ""},
		{"incomplete sequence at end carried as tail", mouseModeState{}, "\x1b[?1000", mouseModeState{}, "\x1b[?1000"},
		{"incomplete 2004 carried as tail", mouseModeState{}, "text\x1b[?20", mouseModeState{}, "\x1b[?20"},
		{"bare esc at end carried as tail", mouseModeState{}, "text\x1b", mouseModeState{}, "\x1b"},
		{"esc-bracket at end carried as tail", mouseModeState{}, "text\x1b[", mouseModeState{}, "\x1b["},
		{"complete sequence leaves no tail", mouseModeState{}, "\x1b[?2004h", mouseModeState{bracketedPaste: true}, ""},
		{"non-mode escape leaves no tail", mouseModeState{}, "\x1b[31mred", mouseModeState{}, ""},
		{"overlong param run dropped, not carried", mouseModeState{}, "\x1b[?12345678901234567890123456789", mouseModeState{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, tail := scanMouseModes(tt.start, []byte(tt.data))
			if got != tt.want {
				t.Errorf("scanMouseModes(%+v, %q) = %+v, want %+v",
					tt.start, tt.data, got, tt.want)
			}
			if string(tail) != tt.wantTail {
				t.Errorf("scanMouseModes(%+v, %q) tail = %q, want %q",
					tt.start, tt.data, tail, tt.wantTail)
			}
		})
	}
}

// TestScanMouseModes_SplitAcrossChunks exercises the carry contract end to
// end: an enable sequence split across two PTY output chunks must still set
// the mode once the caller prepends the returned tail to the next chunk. A
// missed ?2004 enable would make a reattached client inject pastes as raw
// keystrokes, so this is the split that must never be dropped.
func TestScanMouseModes_SplitAcrossChunks(t *testing.T) {
	t.Parallel()
	splits := []int{1, 2, 3, 5, 7} // every split point inside "\x1b[?2004h"
	const seq = "\x1b[?2004h"
	for _, at := range splits {
		m, tail := scanMouseModes(mouseModeState{}, []byte(seq[:at]))
		if m.bracketedPaste {
			t.Fatalf("split at %d: mode set from incomplete fragment", at)
		}
		m, tail = scanMouseModes(m, append(append([]byte{}, tail...), seq[at:]...))
		if !m.bracketedPaste {
			t.Errorf("split at %d: bracketedPaste not set after carry", at)
		}
		if len(tail) != 0 {
			t.Errorf("split at %d: leftover tail %q after complete sequence", at, tail)
		}
	}
}
