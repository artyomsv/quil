package tui

import (
	"image/color"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// dimPalette holds the blend inputs for one dimmed frame.
//
// bg is the blend TARGET, not merely a color to dim: everything fades toward
// the terminal's own background, which is what makes a cell carrying the
// default background need no work at all (blending a color toward itself is a
// no-op) and leaves foregrounds as the only thing that must be named.
type dimPalette struct {
	fg     color.Color // terminal default foreground
	bg     color.Color // terminal default background, and the blend target
	amount float64     // 0 = untouched, 1 = indistinguishable from bg
}

// dimFallbackFg/Bg stand in for a terminal that never answered OSC 10/11.
// They assume a dark theme, which is the safe guess here rather than a
// coin-flip: the dim only runs after a DEC 1004 blur, and a terminal that
// implements focus reporting but not color reporting is a narrow corner.
var (
	dimFallbackFg = color.RGBA{R: 0xc0, G: 0xc0, B: 0xc0, A: 0xff}
	dimFallbackBg = color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff}
)

// dimPalette builds the blend inputs for this frame, preferring what the
// terminal reported about itself over the assumed defaults.
func (m Model) dimPalette(amount float64) dimPalette {
	p := dimPalette{fg: dimFallbackFg, bg: dimFallbackBg, amount: amount}
	if m.termFg != nil {
		p.fg = m.termFg
	}
	if m.termBg != nil {
		p.bg = m.termBg
	}
	return p
}

// dimColor blends c toward target by amount, per channel, in straight RGB.
//
// Straight RGB rather than a perceptual space (lipgloss.Blend1D) for two
// reasons: Blend1D allocates a slice of N steps per call and this runs per
// color per frame, and "fade toward the background" is exactly what a linear
// ramp expresses — the perceptual smoothness that matters for a gradient
// buys nothing for a single blend against one target.
func dimColor(c, target color.Color, amount float64) color.RGBA {
	cr, cg, cb := rgb8(c)
	tr, tg, tb := rgb8(target)
	return color.RGBA{
		R: lerp8(cr, tr, amount),
		G: lerp8(cg, tg, amount),
		B: lerp8(cb, tb, amount),
		A: 0xff,
	}
}

// rgb8 flattens a color.Color to 8-bit channels. color.Color.RGBA reports
// alpha-premultiplied 16-bit values, so the high byte is the 8-bit channel.
func rgb8(c color.Color) (r, g, b uint8) {
	r16, g16, b16, _ := c.RGBA()
	return uint8(r16 >> 8), uint8(g16 >> 8), uint8(b16 >> 8)
}

func lerp8(from, to uint8, amount float64) uint8 {
	return uint8(math.Round(float64(from) + (float64(to)-float64(from))*amount))
}

// sgrSetFg is the truecolor foreground SGR for c.
func sgrSetFg(c color.RGBA) string {
	return "\x1b[" + sgrColorParams(38, c) + "m"
}

// sgrColorParams renders the truecolor parameter run for c — "38;2;R;G;B" for
// a foreground (lead 38), "48;2;R;G;B" for a background (lead 48).
func sgrColorParams(lead int, c color.RGBA) string {
	var b []byte
	b = strconv.AppendInt(b, int64(lead), 10)
	b = append(b, ";2;"...)
	b = strconv.AppendUint(b, uint64(c.R), 10)
	b = append(b, ';')
	b = strconv.AppendUint(b, uint64(c.G), 10)
	b = append(b, ';')
	b = strconv.AppendUint(b, uint64(c.B), 10)
	return string(b)
}

// dimFrame rewrites a fully composed frame so every color fades toward the
// terminal background, leaving cell widths and every non-SGR sequence exactly
// as they were.
//
// This runs on the COMPOSED frame rather than on pane cells or on quil's own
// lipgloss styles, and that single seam is the whole design: chrome and pane
// content have already been flattened into one string here, so one pass dims
// both without touching the ~93 style definitions in the package or any of the
// render caches upstream. Being downstream of every cache is what makes a
// focus change need no invalidation — the caches keep storing undimmed content
// and this rewrites whatever they hand over.
func dimFrame(content string, p dimPalette) string {
	if p.amount <= 0 || content == "" {
		return content
	}
	dimmedDefaultFg := sgrSetFg(dimColor(p.fg, p.bg, p.amount))

	var b strings.Builder
	b.Grow(len(content) + len(content)/8)

	// fgIsDefault tracks whether the terminal's default foreground is in
	// effect; named records whether we have already emitted the dimmed
	// stand-in for it. They are two facts: after a reset the default is in
	// effect again but is no longer named, and text may follow with no SGR of
	// its own.
	fgIsDefault, named := true, false

	// A frame reuses a handful of SGR runs thousands of times — one per styled
	// cell, and a pane paints most of its cells in the same few colors. The
	// palette is fixed for the call, so a rewrite depends only on the parameter
	// text and can be memoised for the whole frame.
	//
	// Measured on BenchmarkFrame_UnfocusedDim, against the WarmPane baseline:
	// the dim went from +954 allocs / +45 KB per 41-tab frame to +93 / +24 KB,
	// and from +50% to +14% on the uncontended tabs=1 case. Allocations are the
	// number to trust here — the wall-clock figures at 41 tabs are noisy enough
	// on a shared runner to overlap between runs. The split/parse/join per
	// occurrence was the bulk of the cost, and most occurrences are duplicates.
	cache := make(map[string]sgrRewrite, 32)

	var state byte
	for i := 0; i < len(content); {
		seq, width, n, newState := ansi.DecodeSequence(content[i:], state, nil)
		if n == 0 { // malformed tail — copy the rest verbatim rather than spin
			b.WriteString(content[i:])
			break
		}
		state = newState
		i += n

		if params, ok := sgrParams(seq); ok {
			r, hit := cache[params]
			if !hit {
				r.out, r.setsDefaultFg, r.setsExplicitFg = dimSGRParams(params, p)
				cache[params] = r
			}
			b.WriteString("\x1b[")
			b.WriteString(r.out)
			b.WriteByte('m')
			switch {
			case r.setsExplicitFg:
				fgIsDefault, named = false, false
			case r.setsDefaultFg:
				fgIsDefault, named = true, false
			}
			continue
		}

		// Printable text about to render in the terminal default foreground is
		// the only thing that needs the stand-in named. Zero-width sequences
		// (cursor moves, OSC) never do.
		if width > 0 && fgIsDefault && !named {
			b.WriteString(dimmedDefaultFg)
			named = true
		}
		b.WriteString(seq)
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// sgrParams reports whether seq is an SGR sequence and, if so, returns its
// parameter text (the bytes between the CSI introducer and the final 'm').
//
// A CSI carrying a private prefix (`<=>?`) is deliberately NOT treated as SGR
// even when it ends in 'm' — those are private-mode sequences with unrelated
// grammar, and rewriting their parameters would corrupt them.
func sgrParams(seq string) (string, bool) {
	var body string
	switch {
	case len(seq) >= 3 && seq[0] == 0x1b && seq[1] == '[':
		body = seq[2:]
	case len(seq) >= 2 && seq[0] == 0x9b: // C1 CSI
		body = seq[1:]
	default:
		return "", false
	}
	if body[len(body)-1] != 'm' {
		return "", false
	}
	body = body[:len(body)-1]
	if body != "" && body[0] >= 0x3c && body[0] <= 0x3f {
		return "", false
	}
	return body, true
}

// sgrRewrite is one memoised dimSGRParams result. See the cache in dimFrame.
type sgrRewrite struct {
	out            string
	setsDefaultFg  bool
	setsExplicitFg bool
}

// dimSGRParams rewrites one SGR parameter run, blending every color it names
// toward the background and passing every attribute through untouched. It also
// reports what the run did to the foreground, which is what tells dimFrame
// whether the default is back in effect.
//
// Sub-parameter (colon) forms are passed through unchanged rather than parsed:
// they are rare, quil emits none of them, and a corrupted color is worse than
// an undimmed one.
func dimSGRParams(params string, p dimPalette) (out string, setsDefaultFg, setsExplicitFg bool) {
	if params == "" { // bare CSI m — an implicit reset
		return "", true, false
	}

	fields := strings.Split(params, ";")
	rewritten := make([]string, 0, len(fields))

	for i := 0; i < len(fields); i++ {
		v, err := strconv.Atoi(fields[i])
		if fields[i] == "" {
			v, err = 0, nil // an omitted parameter defaults to 0
		}
		if err != nil { // colon sub-parameters, or junk — leave it as it came
			rewritten = append(rewritten, fields[i])
			if strings.HasPrefix(fields[i], "38:") {
				// A colon-form foreground. It goes through undimmed, but it
				// must still count as an explicit foreground: naming the
				// dimmed default right after would overwrite the color the
				// child actually asked for. An attribute in colon form (a
				// curly underline, "4:3") says nothing about the foreground
				// and correctly leaves the state alone.
				setsExplicitFg, setsDefaultFg = true, false
			}
			continue
		}

		switch {
		case v == 0:
			rewritten = append(rewritten, "0")
			setsDefaultFg, setsExplicitFg = true, false
		case v == 39:
			rewritten = append(rewritten, "39")
			setsDefaultFg, setsExplicitFg = true, false
		case v >= 30 && v <= 37:
			rewritten = append(rewritten, dimBasic(38, v-30, p))
			setsExplicitFg, setsDefaultFg = true, false
		case v >= 90 && v <= 97:
			rewritten = append(rewritten, dimBasic(38, v-90+8, p))
			setsExplicitFg, setsDefaultFg = true, false
		case v >= 40 && v <= 47:
			rewritten = append(rewritten, dimBasic(48, v-40, p))
		case v >= 100 && v <= 107:
			rewritten = append(rewritten, dimBasic(48, v-100+8, p))
		case v == 38 || v == 48:
			consumed, text, ok := dimExtended(v, fields[i:], p)
			if !ok { // malformed run — copy the remainder verbatim
				rewritten = append(rewritten, fields[i:]...)
				return strings.Join(rewritten, ";"), setsDefaultFg, setsExplicitFg
			}
			rewritten = append(rewritten, text)
			i += consumed - 1
			if v == 38 {
				setsExplicitFg, setsDefaultFg = true, false
			}
		default:
			rewritten = append(rewritten, fields[i])
		}
	}
	return strings.Join(rewritten, ";"), setsDefaultFg, setsExplicitFg
}

// dimBasic blends one of the 16 palette colors and re-emits it as truecolor.
func dimBasic(lead, idx int, p dimPalette) string {
	return sgrColorParams(lead, dimColor(ansi.BasicColor(idx), p.bg, p.amount))
}

// dimExtended blends a 38/48 extended-color run — "5;n" (256-palette) or
// "2;r;g;b" (truecolor) — returning how many fields it consumed.
func dimExtended(lead int, fields []string, p dimPalette) (consumed int, out string, ok bool) {
	if len(fields) < 2 {
		return 0, "", false
	}
	switch fields[1] {
	case "5":
		if len(fields) < 3 {
			return 0, "", false
		}
		idx, err := strconv.Atoi(fields[2])
		if err != nil || idx < 0 || idx > 255 {
			return 0, "", false
		}
		return 3, sgrColorParams(lead, dimColor(ansi.IndexedColor(idx), p.bg, p.amount)), true
	case "2":
		if len(fields) < 5 {
			return 0, "", false
		}
		var c color.RGBA
		for n, dst := range []*uint8{&c.R, &c.G, &c.B} {
			v, err := strconv.Atoi(fields[2+n])
			if err != nil || v < 0 || v > 255 {
				return 0, "", false
			}
			*dst = uint8(v)
		}
		c.A = 0xff
		return 5, sgrColorParams(lead, dimColor(c, p.bg, p.amount)), true
	}
	return 0, "", false
}
