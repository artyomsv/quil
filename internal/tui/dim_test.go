package tui

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// testPalette is the blend input every test in this file uses unless it needs
// something else: a mid-grey default foreground over a black background, dimmed
// halfway. Chosen so the expected values are exact and obvious — 0xc0 blended
// 50% toward 0x00 is 0x60, with no rounding to argue about.
func testPalette() dimPalette {
	return dimPalette{
		fg:     color.RGBA{R: 0xc0, G: 0xc0, B: 0xc0, A: 0xff},
		bg:     color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff},
		amount: 0.5,
	}
}

func TestDimFrame_UnstyledTextGetsDimmedDefaultForeground(t *testing.T) {
	// Text carrying no SGR of its own renders in the terminal's default
	// foreground, which the frame never names. Dimming it therefore means
	// naming it: without this the majority of a shell pane stays full
	// brightness and the whole feature reads as broken.
	got := dimFrame("hello", testPalette())
	want := "\x1b[38;2;96;96;96mhello\x1b[0m"
	if got != want {
		t.Errorf("dimFrame() = %q, want %q", got, want)
	}
}

func TestDimFrame_BlendsExplicitTrueColorForeground(t *testing.T) {
	// A cell that names its own color is the ordinary case for both quil's
	// chrome and any colorful child program. 255 halfway to 0 rounds to 128.
	got := dimFrame("\x1b[38;2;255;0;0mred", testPalette())
	want := "\x1b[38;2;128;0;0mred\x1b[0m"
	if got != want {
		t.Errorf("dimFrame() = %q, want %q", got, want)
	}
}

// dimFrameCases covers one rewriting rule each. Every want value is written
// out by hand rather than computed, so a change to the blend or to the SGR
// grammar has to be justified against a number somebody chose.
func TestDimFrame_RewritingRules(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// 256-palette index 57 is #5f00ff.
			name: "256-palette foreground resolves and blends",
			in:   "\x1b[38;5;57mx",
			want: "\x1b[38;2;48;0;128mx\x1b[0m",
		},
		{
			// SGR 31 is palette 1, #800000.
			name: "basic foreground resolves and blends",
			in:   "\x1b[31mx",
			want: "\x1b[38;2;64;0;0mx\x1b[0m",
		},
		{
			// SGR 91 is palette 9, #ff0000 — bright red, not a brighter 31.
			name: "bright foreground resolves and blends",
			in:   "\x1b[91mx",
			want: "\x1b[38;2;128;0;0mx\x1b[0m",
		},
		{
			// SGR 44 is palette 4, #000080. The foreground is still the
			// terminal default here, so the stand-in follows the background.
			name: "background blends and default foreground is still named",
			in:   "\x1b[44mx",
			want: "\x1b[48;2;0;0;64m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			name: "attributes survive alongside a rewritten color",
			in:   "\x1b[1;31mx",
			want: "\x1b[1;38;2;64;0;0mx\x1b[0m",
		},
		{
			// The reset is what makes this stateful: after it the default is
			// back in effect and unnamed, so the stand-in must be re-emitted.
			name: "reset re-arms the dimmed default foreground",
			in:   "a\x1b[0mb",
			want: "\x1b[38;2;96;96;96ma\x1b[0m\x1b[38;2;96;96;96mb\x1b[0m",
		},
		{
			name: "SGR 39 re-arms the dimmed default foreground",
			in:   "\x1b[31ma\x1b[39mb",
			want: "\x1b[38;2;64;0;0ma\x1b[39m\x1b[38;2;96;96;96mb\x1b[0m",
		},
		{
			name: "bare CSI m is treated as a reset",
			in:   "\x1b[31ma\x1b[mb",
			want: "\x1b[38;2;64;0;0ma\x1b[m\x1b[38;2;96;96;96mb\x1b[0m",
		},
		{
			// OSC 8 hyperlinks carry a URL that must survive byte-identically;
			// they also occupy no cells, so they never trigger the stand-in.
			name: "OSC hyperlink passes through untouched",
			in:   "\x1b]8;;http://x\x07link\x1b]8;;\x07",
			want: "\x1b]8;;http://x\x07\x1b[38;2;96;96;96mlink\x1b]8;;\x07\x1b[0m",
		},
		{
			// Ends in 'm' but is xterm's modifyOtherKeys, not SGR. Rewriting
			// its parameters would corrupt it.
			name: "private CSI ending in m is not treated as SGR",
			in:   "\x1b[>4;2mx",
			want: "\x1b[>4;2m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			// Colon sub-parameters are passed through unparsed. A curly
			// underline says nothing about the foreground, so the text after
			// it must still get the dimmed default.
			name: "colon-form attribute still allows the default to be named",
			in:   "\x1b[4:3mx",
			want: "\x1b[4:3m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			// The dangerous half of the same rule: a colon-form FOREGROUND is
			// passed through undimmed, but naming the default after it would
			// overwrite the color the child actually asked for.
			name: "colon-form foreground suppresses the default stand-in",
			in:   "\x1b[38:2::255:0:0mx",
			want: "\x1b[38:2::255:0:0mx\x1b[0m",
		},
		{
			// SGR 58 sets the UNDERLINE color and takes the same extended
			// grammar as 38 and 48. Leaving it unconsumed re-reads its
			// sub-parameters as top-level codes — the 2 becomes faint, the
			// zeroes become resets, and the reset clobbers the foreground that
			// is still in effect. Emitted by Neovim and helix LSP diagnostics,
			// and anything else setting a colored underline — not by every
			// colorful tool (`rg --color` and bat emit 38/48 only).
			name: "underline color is consumed and leaves the foreground alone",
			in:   "\x1b[38;2;255;0;0m\x1b[58;2;0;255;0mtext",
			want: "\x1b[38;2;128;0;0m\x1b[58;2;0;128;0mtext\x1b[0m",
		},
		{
			name: "underline color in 256-palette form is consumed",
			in:   "\x1b[58;5;57mx",
			want: "\x1b[58;2;48;0;128m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			// The OTHER half of the unconsumed-58 failure, and the reason this
			// row picks index 31 rather than something arbitrary. Left
			// unconsumed, 31 matches the basic-foreground arm — so instead of
			// a spurious reset, the run sets an explicit foreground and
			// SUPPRESSES the stand-in, leaving every default-fg cell after it
			// at full brightness. An index that happens to miss 30-37/40-47/
			// 90-97/100-107 (58;5;9, say) passes even while the bug is present.
			// Palette 31 is #0087af.
			name: "underline color whose index collides with a color range",
			in:   "\x1b[58;5;31mx",
			want: "\x1b[58;2;0;68;88m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			// 59 restores the default underline color and takes no parameters,
			// so unlike 39 it must leave the foreground state untouched.
			name: "SGR 59 does not disturb the foreground",
			in:   "\x1b[38;2;255;0;0m\x1b[59mtext",
			want: "\x1b[38;2;128;0;0m\x1b[59mtext\x1b[0m",
		},
		{
			// SGR 101 is palette 9, #ff0000, as a background.
			name: "bright background resolves and blends",
			in:   "\x1b[101mx",
			want: "\x1b[48;2;128;0;0m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			name: "truecolor background blends",
			in:   "\x1b[48;2;0;0;255mx",
			want: "\x1b[48;2;0;0;128m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			// Named explicitly rather than left to the generic attribute path:
			// reverse video swaps fg and bg at render, and both are being
			// blended toward the background, so it is worth pinning that the
			// attribute itself survives untouched.
			name: "reverse video survives as an attribute",
			in:   "\x1b[7mx",
			want: "\x1b[7m\x1b[38;2;96;96;96mx\x1b[0m",
		},
		{
			// A 38 with no colour space after it. The run cannot be parsed, so
			// it is copied verbatim — and, like the colon form, must still count
			// as an explicit foreground: it may well have set one, and naming
			// the default over the top would be corruption rather than a miss.
			name: "truncated extended run passes through without clobbering",
			in:   "\x1b[38mx",
			want: "\x1b[38mx\x1b[0m",
		},
		{
			name: "out-of-range palette index passes through without clobbering",
			in:   "\x1b[38;5;999mx",
			want: "\x1b[38;5;999mx\x1b[0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dimFrame(tt.in, testPalette()); got != tt.want {
				t.Errorf("dimFrame(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDimFrame_ZeroAmountReturnsContentUnchanged(t *testing.T) {
	// The config value that disables the feature must cost nothing and, more
	// importantly, must not rewrite a single byte.
	in := "\x1b[31mred\x1b[0m plain"
	p := testPalette()
	p.amount = 0
	if got := dimFrame(in, p); got != in {
		t.Errorf("dimFrame() = %q, want it unchanged (%q)", got, in)
	}
}

func TestDimFrame_PreservesRenderedWidthOfEveryLine(t *testing.T) {
	// renderTabBar measures style.Render(name) to hit-test clicks, so a pass
	// that changed a rendered width would desync mouse targeting from what is
	// drawn. Rewriting only SGR parameters preserves width by construction —
	// this is the test that keeps it that way.
	frame := strings.Join([]string{
		"\x1b[1;38;5;230;48;5;57m Tab one \x1b[0m\x1b[38;5;250;48;5;238m Tab two \x1b[0m",
		"╭─ pane ─────╮",
		"│ \x1b[32m✓\x1b[0m done 日本語 │",
		"│ \x1b[38;2;255;128;0mwarn\x1b[39m plain │",
		"╰────────────╯",
	}, "\n")

	got := dimFrame(frame, testPalette())

	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(frame, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("line count = %d, want %d", len(gotLines), len(wantLines))
	}
	for i := range wantLines {
		if w, g := ansi.StringWidth(wantLines[i]), ansi.StringWidth(gotLines[i]); w != g {
			t.Errorf("line %d width = %d, want %d\n got %q\nfrom %q", i, g, w, gotLines[i], wantLines[i])
		}
	}
}

func TestDimFrame_SurvivesATruncatedEscapeSequence(t *testing.T) {
	// A frame is assembled from pane content that came off a PTY, so a
	// sequence cut mid-flight is ordinary rather than hostile. dimFrame's
	// decoder loop must not spin on it or swallow the text around it —
	// the visible characters are what the user is still entitled to see.
	for _, in := range []string{
		"before\x1b",      // a lone ESC at the very end
		"before\x1b[",     // a CSI introducer with nothing after it
		"before\x1b[38;2", // an extended colour run cut mid-parameter
		"\x1b[38;2;1;2;3", // the same with no text at all
	} {
		got := dimFrame(in, testPalette())
		if !strings.Contains(stripANSI(got), "before") && strings.Contains(in, "before") {
			t.Errorf("dimFrame(%q) = %q — dropped the visible text", in, got)
		}
	}
}

func TestSGRParams_RefusesABodyOutsideTheParameterByteRange(t *testing.T) {
	// dimFrame's no-injection property rests on an invariant of
	// ansi.DecodeSequence: a CSI it returns has a body drawn only from
	// 0x20-0x3F, so a parameter field copied through verbatim by dimSGRParams
	// can never carry an ESC, a second final byte, or a string terminator.
	//
	// dim.go cannot see that invariant, and nothing in this package would
	// notice if a future parser version started returning the raw span of a
	// malformed CSI — at which point the verbatim copy becomes an injection
	// primitive. So sgrParams checks the property itself rather than trusting
	// it, and this pins the check. A refused sequence is passed through
	// undimmed, which is the safe direction.
	for _, seq := range []string{
		"\x1b[31\x1b[0m", // an embedded ESC
		"\x1b[31\x07m",   // a BEL, which would terminate an OSC
		"\x1b[31Am",      // a second final byte inside the body
		"\x1b[31\x9bm",   // a C1 CSI introducer
	} {
		if body, ok := sgrParams(seq); ok {
			t.Errorf("sgrParams(%q) = (%q, true) — a body outside 0x20-0x3F must be refused", seq, body)
		}
	}

	// The ordinary forms must still be accepted, or the check has quietly
	// disabled the whole feature.
	for _, seq := range []string{"\x1b[0m", "\x1b[38;2;1;2;3m", "\x1b[4:3m", "\x1b[m"} {
		if _, ok := sgrParams(seq); !ok {
			t.Errorf("sgrParams(%q) = false — a well-formed SGR must still be accepted", seq)
		}
	}
}
