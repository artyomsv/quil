package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// The sidebar's state glyphs must each measure exactly one cell, and must not
// be emoji-capable codepoints.
//
// The width half is what every row builder's arithmetic assumes. The
// emoji-capable half is not expressible as a width assertion at all, which is
// why it is pinned as an explicit codepoint list: a terminal that font-falls
// back to a colour emoji face draws about two cells and advances one, so the
// glyph overpaints whatever follows it while every measurement here stays
// happy. That is how the project badge rendered its blocked count underneath
// its own icon (U+26A0 WARNING SIGN, replaced by U+25B2).
func TestSidebarGlyphs_OneCellAndNotEmojiCapable(t *testing.T) {
	t.Parallel()
	// Codepoints with Emoji=Yes that a font may render as a colour emoji.
	// Not exhaustive — it names the ones a state vocabulary would plausibly
	// reach for, so a future edit that reintroduces one fails here.
	emojiCapable := map[rune]string{
		0x26A0:  "U+26A0 WARNING SIGN",
		0x26A1:  "U+26A1 HIGH VOLTAGE",
		0x2757:  "U+2757 EXCLAMATION MARK",
		0x2753:  "U+2753 QUESTION MARK",
		0x2B55:  "U+2B55 HEAVY LARGE CIRCLE",
		0x274C:  "U+274C CROSS MARK",
		0x2705:  "U+2705 WHITE HEAVY CHECK MARK",
		0x23F3:  "U+23F3 HOURGLASS",
		0x1F534: "U+1F534 RED CIRCLE",
	}
	for name, g := range map[string]string{
		"glyphBlocked": glyphBlocked,
		"glyphWorking": glyphWorking,
		"glyphDone":    glyphDone,
		"glyphIdle":    glyphIdle,
		"glyphPinned":  glyphPinned,
	} {
		if got := lipgloss.Width(g); got != 1 {
			t.Errorf("%s (%q) measures %d cells, want exactly 1 — "+
				"every row builder budgets it as one", name, g, got)
		}
		if n := len([]rune(g)); n != 1 {
			t.Errorf("%s (%q) is %d runes, want 1 — a multi-rune glyph "+
				"(a variation-selector pair) can be split by truncateCells, "+
				"which changes its width", name, g, n)
		}
		for _, r := range g {
			if which, bad := emojiCapable[r]; bad {
				t.Errorf("%s uses %s, which a font may render as a wide colour "+
					"emoji while advancing one cell — it will overpaint the "+
					"text after it", name, which)
			}
		}
	}
}

// remoteTextSamples are strings a REMOTE daemon can put into a sidebar row.
// sanitizeRemoteText strips control characters and bidi overrides but
// deliberately preserves printable non-ASCII byte-identically, so every one of
// these reaches the row builders intact.
var remoteTextSamples = []string{
	"quil",
	"",
	"a-very-long-project-name-that-will-certainly-truncate",
	"构建服务",    // wide CJK
	"⚠️",      // the exact pair that made truncateCells overflow
	"a️b",     // a variation selector after a narrow ASCII rune
	"️",       // a bare variation selector
	"🔥️",      // emoji plus an explicit presentation request
	"feat/中文", // mixed-width branch name
	strings.Repeat("⚡", 30),
}

// Every sidebar row is padded to an exact cell count, because renderSidebar's
// closing .Width(w) WRAPS an over-wide line onto a new one rather than cutting
// it — which shifts every row below while sidebarRowAt still maps screen row y
// to rows[y-1], so the user clicks one project and selects its neighbour.
//
// The widths swept include ones far below the default: sidebarWidth() clamps a
// configured value against the terminal, so a narrow terminal genuinely
// produces them.
func TestSidebarRows_MeasureExactlyTheirWidth(t *testing.T) {
	t.Parallel()
	widths := []int{4, 8, 12, defaultSidebarWidth, 40}
	counts := [][3]int{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {1, 2, 3}, {12, 345, 6}}

	for _, w := range widths {
		for _, name := range remoteTextSamples {
			for _, c := range counts {
				for _, link := range []string{"", "⚡", "⟳"} {
					got := projectRow(name, c[0], c[1], c[2], link, true, w)
					if n := lipgloss.Width(got); n != w {
						t.Errorf("projectRow(%q, working=%d blocked=%d done=%d, link=%q, w=%d) "+
							"measures %d cells, want exactly %d",
							name, c[0], c[1], c[2], link, w, n, w)
					}
				}
			}
			if n := lipgloss.Width(projectDestRow(name, w)); n != w {
				t.Errorf("projectDestRow(%q, %d) measures %d cells, want %d", name, w, n, w)
			}
		}
	}
}

// paneRow's own sweep: its label and its suffix are independent remote-sourced
// strings, and the suffix is the one the label's floor truncates INTO — so the
// pair has to be swept together rather than one at a time.
func TestPaneRow_MeasuresExactlyItsWidth(t *testing.T) {
	t.Parallel()
	for _, w := range []int{4, 8, 12, defaultSidebarWidth, 40} {
		for _, label := range remoteTextSamples {
			for _, reason := range remoteTextSamples {
				for _, state := range []string{"blocked", "working", "unseen", "idle", "pinned", "pinned+blocked", "pinned+working"} {
					pane := &PaneModel{Name: label, ID: "pane-b16e3850"}
					switch state {
					case "blocked":
						pane.blockedSince = time.Now()
						pane.blockedReason = reason
					case "working":
						pane.working = true
						pane.subagents = map[string]int{"impl": 3}
					case "unseen":
						pane.unseen = true
					case "pinned":
						pane.pinnedAttention = true
					case "pinned+blocked":
						pane.pinnedAttention = true
						pane.blockedSince = time.Now()
						pane.blockedReason = reason
					case "pinned+working":
						pane.pinnedAttention = true
						pane.working = true
						pane.subagents = map[string]int{"impl": 3}
					}
					for _, focused := range []bool{true, false} {
						got := paneRow(pane, focused, w)
						if n := lipgloss.Width(got); n != w {
							t.Errorf("paneRow(state=%s label=%q reason=%q focused=%v, w=%d) "+
								"measures %d cells, want exactly %d",
								state, label, reason, focused, w, n, w)
						}
					}
				}
			}
		}
	}
}

// A long run of printable ZERO-WIDTH codepoints never advances the width
// budget, so neither cutter can exit its loop early on one — it walks the whole
// string. The first fix for the variation-selector overflow re-measured a
// growing prefix (and a growing suffix) on every step, which turned that walk
// quadratic: on a render path, over text supplied by another machine, with
// sanitizeRemoteText deliberately preserving printable non-ASCII. Segmenting
// into grapheme clusters and measuring each once makes it linear.
//
// U+200B ZERO WIDTH SPACE is the input because it is printable — a control
// filter does not touch it — and measures zero. The visible tail is what makes
// the string exceed the budget, so the early return is not taken and the loop
// really runs.
//
// Asserted as completion inside a generous bound rather than as a timing
// measurement. The gap is orders of magnitude — microseconds against minutes —
// so a coarse deadline is not flaky, and a regression fails here rather than
// hanging until the whole test binary times out.
func TestCellCutters_SurviveAZeroWidthFlood(t *testing.T) {
	t.Parallel()
	const w = 5
	flood := strings.Repeat("​", 50000) + "visible text"

	// elideMiddle is included rather than assumed. It delegates its head and
	// tail to the other two, so today it inherits their cost by composition —
	// but that is an inference about the current body, not a property anyone
	// enforces, and an edit that measures something over s before delegating
	// would reintroduce the blowup here alone.
	type result struct{ trunc, last, elide string }
	done := make(chan result, 1)
	go func() {
		done <- result{
			trunc: truncateCells(flood, w),
			last:  lastCellsToWidth(flood, w),
			elide: elideMiddle(flood, w),
		}
	}()

	select {
	case got := <-done:
		if n := lipgloss.Width(got.trunc); n > w {
			t.Errorf("truncateCells returned %d cells, over the %d budget", n, w)
		}
		if n := lipgloss.Width(got.last); n > w {
			t.Errorf("lastCellsToWidth returned %d cells, over the %d budget", n, w)
		}
		if n := lipgloss.Width(got.elide); n > w {
			t.Errorf("elideMiddle returned %d cells, over the %d budget", n, w)
		}
		// The tail is what is visible, so the backward cut must reach it
		// rather than stopping in the zero-width run.
		if !strings.Contains(got.last, "text") {
			t.Errorf("lastCellsToWidth = %q, want it to reach the visible tail", got.last)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cell cutters did not finish on a zero-width flood in 15s — " +
			"the per-step re-measurement is back and the cost is quadratic in len(s)")
	}
}

// truncateCells and lastCellsToWidth are the two ends of the same cut, and
// both summed runes INDEPENDENTLY. A variation selector measures 0 alone while
// making the pair it follows measure 2, so both returned strings WIDER than
// the budget they were handed — truncateCells("x⚠️", 2) was three cells.
//
// Asserted as an inequality over a sweep rather than as exact outputs: the
// contract is "never exceeds", and pinning exact strings for every sequence
// would freeze decisions (which half of a pair survives) that are not part of
// it. TestTruncateCellsDropsStraddlingWideGlyphs keeps the exact-output cases.
func TestCellCutters_NeverExceedTheirBudget(t *testing.T) {
	t.Parallel()
	for _, in := range remoteTextSamples {
		for w := 0; w <= 12; w++ {
			if got := truncateCells(in, w); lipgloss.Width(got) > w {
				t.Errorf("truncateCells(%q, %d) = %q, which is %d cells — over budget",
					in, w, got, lipgloss.Width(got))
			}
			if got := lastCellsToWidth(in, w); lipgloss.Width(got) > w {
				t.Errorf("lastCellsToWidth(%q, %d) = %q, which is %d cells — over budget",
					in, w, got, lipgloss.Width(got))
			}
			// elideMiddle composes both, so it is the one that would surface a
			// regression in either half as an overlapping, doubled-width row.
			if got := elideMiddle(in, w); lipgloss.Width(got) > w {
				t.Errorf("elideMiddle(%q, %d) = %q, which is %d cells — over budget",
					in, w, got, lipgloss.Width(got))
			}
		}
	}
}
