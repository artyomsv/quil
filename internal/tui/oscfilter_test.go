package tui

import (
	"bytes"
	"testing"
)

func TestOSCTitleFilter_SingleChunk(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "hello world", "hello world"},
		{"CSI sequences untouched", "\x1b[1;5H\x1b[31mX\x1b[0m", "\x1b[1;5H\x1b[31mX\x1b[0m"},
		{"OSC0 BEL stripped", "a\x1b]0;title\x07b", "ab"},
		{"OSC1 BEL stripped", "a\x1b]1;icon\x07b", "ab"},
		{"OSC2 ST stripped", "a\x1b]2;title\x1b\\b", "ab"},
		{"bare OSC0 (empty number) stripped", "a\x1b]0;\x07b", "ab"},
		// The real bug: title with a UTF-8 char containing 0x9C must be dropped
		// whole, not leaked. ✳ = U+2733 = E2 9C B3.
		{"OSC0 with 0x9C char stripped whole", "a\x1b]0;✳ Claude Code\x07b", "ab"},
		// Non-title OSCs must pass through unchanged.
		{"OSC7 cwd passes through", "a\x1b]7;file:///tmp\x07b", "a\x1b]7;file:///tmp\x07b"},
		{"OSC11 color passes through", "a\x1b]11;?\x07b", "a\x1b]11;?\x07b"},
		{"OSC10 color passes through", "\x1b]10;rgb:1/2/3\x07", "\x1b]10;rgb:1/2/3\x07"},
		{"OSC52 clipboard passes through", "\x1b]52;c;AAAA\x07", "\x1b]52;c;AAAA\x07"},
		{"OSC104 passes through", "\x1b]104;\x07", "\x1b]104;\x07"},
		{"OSC8 hyperlink passes through", "\x1b]8;;https://x\x07link\x1b]8;;\x07", "\x1b]8;;https://x\x07link\x1b]8;;\x07"},
		{"ESC not starting OSC untouched", "\x1bM\x1b=X", "\x1bM\x1b=X"},
		{"lone ESC ] with non-digit passes", "\x1b]P123456\x07", "\x1b]P123456\x07"},
		{"over-long OSC number flushed (not a title)", "\x1b]1234567;x\x07Y", "\x1b]1234567;x\x07Y"},
		{"OSC0 ST split written as ESC-then-backslash", "a\x1b]0;t\x1b\\b", "ab"},
		{"OSC0 with ESC ESC backslash in body", "a\x1b]0;t\x1b\x1b\\b", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f oscTitleFilter
			got := f.Filter([]byte(tc.in))
			if !bytes.Equal(got, []byte(tc.want)) {
				t.Fatalf("Filter(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOSCTitleFilter_SplitAcrossChunks(t *testing.T) {
	// A title OSC split at every possible boundary must still be fully stripped.
	full := "before\x1b]0;✳ Claude Code\x07after"
	want := "beforeafter"
	for split := 0; split <= len(full); split++ {
		var f oscTitleFilter
		got := append([]byte{}, f.Filter([]byte(full[:split]))...)
		got = append(got, f.Filter([]byte(full[split:]))...)
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("split at %d: got %q, want %q", split, got, want)
		}
	}
}

func TestOSCTitleFilter_STTerminatedSplit(t *testing.T) {
	// ST-terminated (ESC \) title, split at every boundary — exercises the
	// oscDropEsc path including a split between the ESC and the '\'.
	full := "x\x1b]2;✳ Claude Code\x1b\\y"
	want := "xy"
	for split := 0; split <= len(full); split++ {
		var f oscTitleFilter
		got := append([]byte{}, f.Filter([]byte(full[:split]))...)
		got = append(got, f.Filter([]byte(full[split:]))...)
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("split at %d: got %q, want %q", split, got, want)
		}
	}
}

func TestOSCTitleFilter_UnterminatedTitleBounded(t *testing.T) {
	// A title OSC that never sends a terminator (e.g. a hostile stream that closes
	// the title with a bare C1 ST 0x9C, which we deliberately do NOT honor) must
	// not swallow output forever. The filter caps the drop at maxOSCBody and
	// resyncs, so trailing output (TAIL) is reachable. Post-cap bytes DO pass
	// through — bounded leakage is the accepted tradeoff vs. unbounded swallow;
	// well-formed BEL/ST-terminated titles never hit the cap.
	var f oscTitleFilter
	body := bytes.Repeat([]byte("Z"), maxOSCBody+500)
	in := append([]byte("\x1b]0;"), body...)
	in = append(in, []byte("TAIL")...)
	got := f.Filter(in)
	if !bytes.Contains(got, []byte("TAIL")) {
		t.Fatalf("unbounded over-drop: TAIL was swallowed (got %d bytes)", len(got))
	}
	// Most of the body must have been dropped — output is bounded near the cap
	// remainder, not the whole ~8.7 KiB input.
	if len(got) > 1024 {
		t.Fatalf("over-drop not bounded: emitted %d bytes, expected the cap to drop most of the body", len(got))
	}
}

func TestOSCTitleFilter_ColorSplitPreserved(t *testing.T) {
	// A non-title OSC split across chunks must be preserved byte-for-byte.
	full := "\x1b]11;rgb:aa/bb/cc\x07tail"
	for split := 0; split <= len(full); split++ {
		var f oscTitleFilter
		got := append([]byte{}, f.Filter([]byte(full[:split]))...)
		got = append(got, f.Filter([]byte(full[split:]))...)
		if !bytes.Equal(got, []byte(full)) {
			t.Fatalf("split at %d: got %q, want %q", split, got, full)
		}
	}
}
