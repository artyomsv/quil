package tui

import (
	"image/color"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/config"
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

// maxDimCacheEntries caps dimFrame's per-frame memo. See the cache comment.
const maxDimCacheEntries = 512

// dimInputs builds the blend inputs for this frame, preferring what the
// terminal reported about itself over the assumed defaults.
func (m Model) dimInputs(amount float64) dimPalette {
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
// a foreground (lead 38), "48;2;R;G;B" for a background (lead 48), and
// "58;2;R;G;B" for an underline color (lead 58).
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
	// The cap bounds the LOSS, not the win. Hit rate is set by the content: a
	// pane painting every cell a distinct truecolor (a gradient, a rendered
	// image) yields one distinct key per cell — tens of thousands on a large
	// terminal, every one of them garbage the moment the frame ends. Past the
	// cap the rewrite is still computed, just not remembered, which is exactly
	// the behaviour before the cache existed. Anything with a real hit rate
	// saturates far below this.
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
				if len(cache) < maxDimCacheEntries {
					cache[params] = r
				}
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
//
// The byte-range check is what makes dimSGRParams' verbatim copies safe to
// emit, and it is deliberately a CHECK rather than an assumption. A CSI body
// returned by ansi.DecodeSequence is drawn only from 0x20-0x3F — parameter
// digits, ':', ';' and intermediates — so a field copied through untouched
// cannot carry an ESC, a second final byte, or a string terminator, and the
// rewriter therefore cannot emit a sequence that was not in its input. That
// property belongs to the parser, not to this file. If a future version
// returned the raw span of a malformed CSI instead, the verbatim copy would
// become an injection primitive and nothing here would notice. Checking costs
// one pass over a short string and turns that into a sequence that merely goes
// undimmed. Pinned by TestSGRParams_RefusesABodyOutsideTheParameterByteRange.
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
	for i := 0; i < len(body); i++ {
		if body[i] < 0x20 || body[i] > 0x3f {
			return "", false
		}
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
		// An omitted parameter defaults to 0, and checking that first spares
		// every empty field a guaranteed-failing Atoi.
		v, err := 0, error(nil)
		if fields[i] != "" {
			v, err = strconv.Atoi(fields[i])
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
		case v == 38 || v == 48 || v == 58:
			// 38, 48 and 58 are the COMPLETE set of codes that consume
			// following parameters, and consuming them is not optional.
			//
			// Complete over the EMITTERS, which is a stronger claim than
			// complete over the SGR standard and is the one that holds here:
			// pane bytes never reach this function. They die in the vt
			// emulator, and the frame's SGR is REGENERATED from uv.Style — so
			// the reachable alphabet is whatever x/ansi's style builder can
			// write, and that has exactly three multi-parameter emitters
			// (foregroundColorString, backgroundColorString,
			// underlineColorString). An SGR code the emulator does not
			// recognise is dropped rather than stored, so a child cannot
			// smuggle a fourth. The closure reopens only if x/ansi gains one,
			// or if raw pane bytes ever bypass the cell model.
			//
			// A code left out here does not merely go undimmed: its
			// sub-parameters fall back into this loop and are read as
			// top-level SGR values,
			// so "58;2;0;255;0" becomes faint + reset + reset and the reset
			// clobbers whatever foreground was in effect. Where the index
			// instead lands in 30-37/40-47/90-97/100-107 the failure inverts:
			// the run sets an explicit foreground, the stand-in is suppressed,
			// and the text after it stays at FULL brightness. 58 was missing.
			// It reaches quil from Neovim and helix LSP diagnostics, and from
			// anything else that sets a colored or curly underline. NOT from
			// every colorful tool — `rg --color` and bat emit 38/48 only, and
			// a reader who checks those first would wrongly conclude this case
			// is theoretical.
			consumed, text, ok := dimExtended(v, fields[i:], p)
			if !ok {
				// Malformed run — copy the remainder verbatim. A 38 still
				// counts as an explicit foreground, for the same reason the
				// colon form does: it may well have set one, and naming the
				// dimmed default over the top would be corruption where
				// leaving it undimmed is only a miss.
				rewritten = append(rewritten, fields[i:]...)
				if v == 38 {
					setsExplicitFg, setsDefaultFg = true, false
				}
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

// dimExtended blends a 38/48/58 extended-color run — "5;n" (256-palette) or
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

// --- Dim controls (F1 → Settings, command palette) -------------------------

// formatDimLevel renders a dim level for display. It formats
// UnfocusedDimLevel, never the raw config field, so the number shown is the
// one the renderer would blend with — a hand-edited 1.5 in config.toml reads
// as 0.90 here, per the rule the Sidebar width setter states.
//
// Two decimals because that is the resolution the level is worth setting at:
// MaxUnfocusedDim is 0.9, and a third digit changes nothing a human can see
// against a blended background.
func formatDimLevel(level float64) string {
	return strconv.FormatFloat(level, 'f', 2, 64)
}

// parseDimLevel accepts a typed dim level, reporting ok only for a value the
// renderer would honour verbatim.
//
// REFUSED rather than clamped, for the reason the Sidebar width setter gives:
// a stored value the renderer would not use must never be displayed back. 0
// and negatives are refused specifically because the toggle row owns "off" —
// accepting 0 here would leave the two Settings rows disagreeing about the
// same state, with the toggle reading "off" over a level the user believes
// they set.
//
// NaN is named rather than left to the comparisons: it fails BOTH bounds, so
// an ordinary range check passes it straight through. It is reachable from
// this row because the editor accepts any single characters the user types,
// and ParseFloat reads "NaN" happily.
func parseDimLevel(s string) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(n) || n <= 0 || n > config.MaxUnfocusedDim {
		return 0, false
	}
	return n, true
}

// toggleUnfocusedDim flips the dim between off and on, acting on the STATE the
// user can see rather than on the enabled flag alone.
//
// The distinction is load-bearing for a legacy `unfocused_dim = 0` install:
// the flag defaults true there while nothing dims, so a flag-flipping toggle
// would move an already-"off" row to a differently-off state and read as a
// dead control. Switching on therefore also supplies a level when none is
// usable — an on switch over a zero level is a toggle that never dims.
//
// Switching off leaves the level alone. That is the entire reason the switch
// is a separate config key; see UIConfig.UnfocusedDimEnabled.
func (m *Model) toggleUnfocusedDim() {
	if m.cfg.UI.UnfocusedDimAmount() > 0 {
		m.cfg.UI.UnfocusedDimEnabled = false
		m.configChanged = true
		return
	}
	m.cfg.UI.UnfocusedDimEnabled = true
	if m.cfg.UI.UnfocusedDimLevel() <= 0 {
		m.cfg.UI.UnfocusedDim = config.DefaultUnfocusedDim
	}
	m.configChanged = true
}

// dimLevelPreset is one palette-offered dim level.
type dimLevelPreset struct {
	name  string
	level float64
}

// dimLevelPresets are the levels the command palette offers. Three, not a
// continuum: the palette is a list of commands with no value-input mode, and
// the F1 → Settings level row is where an arbitrary level is typed.
//
// The set must include DefaultUnfocusedDim, or a user who moved off the
// default from the palette cannot get back to it from the palette — pinned by
// TestDimLevelPresets_AreWithinTheSettableRange, which also holds every preset
// to what the Settings row would accept, so the two front doors cannot
// disagree about which levels are legal.
//
// The top preset stops short of MaxUnfocusedDim: the clamp exists because a
// full blend is indistinguishable from a crashed TUI, and an offered preset
// should sit inside the usable range rather than on its boundary.
var dimLevelPresets = []dimLevelPreset{
	{name: "subtle", level: 0.30},
	{name: "normal", level: config.DefaultUnfocusedDim},
	{name: "strong", level: 0.85},
}

// dimToggleLabel names what Enter will DO, not what the state is — the rule
// sidebarToggleLabel states, and for the same reason: a row labelled with the
// current state leaves the user working out which direction Enter moves it.
//
// Derived from the effective state, so a legacy `unfocused_dim = 0` (flag on,
// nothing dimming) offers to turn the dim ON, matching what toggleUnfocusedDim
// will actually do.
func dimToggleLabel(dimming bool) string {
	if dimming {
		return "Turn unfocused dim off"
	}
	return "Turn unfocused dim on"
}

// setUnfocusedDimLevel applies a palette preset.
//
// It switches the dim ON as well as storing the level, deliberately: picking
// "strong" while the dim is off would otherwise store a number that never
// renders, and a command whose only effect is invisible is indistinguishable
// from one that failed. The Settings level row does NOT do this — there the
// toggle sits directly above and owns the switch, so a level edit that flipped
// it would make the dim impossible to keep off.
func (m *Model) setUnfocusedDimLevel(level float64) {
	m.cfg.UI.UnfocusedDimEnabled = true
	m.cfg.UI.UnfocusedDim = level
	m.configChanged = true
}
