package daemon

import "testing"

func TestScanMouseModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start mouseModeState
		data  string
		want  mouseModeState
	}{
		{"plain text no change", mouseModeState{}, "hello world\r\n", mouseModeState{}},
		{"colors only no change", mouseModeState{normal: true, sgr: true}, "\x1b[31mred\x1b[0m", mouseModeState{normal: true, sgr: true}},
		{"opencode startup burst separate", mouseModeState{},
			"\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h",
			mouseModeState{normal: true, button: true, any: true, sgr: true}},
		{"combined params", mouseModeState{}, "\x1b[?1000;1006h", mouseModeState{normal: true, sgr: true}},
		{"normal tracking only, no sgr", mouseModeState{}, "\x1b[?1000h", mouseModeState{normal: true}},
		{"x10 mode", mouseModeState{}, "\x1b[?9h", mouseModeState{x10: true}},
		{"reset tracking", mouseModeState{normal: true, sgr: true}, "\x1b[?1000l\x1b[?1006l", mouseModeState{}},
		{"reset only sgr keeps tracking", mouseModeState{normal: true, sgr: true}, "\x1b[?1006l", mouseModeState{normal: true}},
		// Regression guard for the per-mode design: resetting a mode that was
		// never set must NOT clear a different, active tracking mode.
		{"reset unset mode preserves others", mouseModeState{button: true}, "\x1b[?1000l", mouseModeState{button: true}},
		{"cursor-hide does not trigger", mouseModeState{}, "\x1b[?25l", mouseModeState{}},
		{"bracketed-paste does not trigger", mouseModeState{}, "\x1b[?2004h", mouseModeState{}},
		{"alt-screen does not trigger", mouseModeState{}, "\x1b[?1049h", mouseModeState{}},
		{"mouse set amid other output", mouseModeState{},
			"text\x1b[?25l more\x1b[?1002h\x1b[?1006h done", mouseModeState{button: true, sgr: true}},
		{"incomplete sequence at end ignored", mouseModeState{}, "\x1b[?1000", mouseModeState{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scanMouseModes(tt.start, []byte(tt.data))
			if got != tt.want {
				t.Errorf("scanMouseModes(%+v, %q) = %+v, want %+v",
					tt.start, tt.data, got, tt.want)
			}
		})
	}
}
