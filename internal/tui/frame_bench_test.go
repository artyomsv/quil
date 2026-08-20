package tui

import (
	"fmt"
	"testing"
)

// Frame-cost attribution.
//
// The estimate this branch started from said `ansi.StringWidth` and grapheme
// segmentation were ~73% of frame cost, and proposed a width cache on that
// basis. That figure is not trustworthy as an argument for a cache, because
// PaneModel.View() caches on renderKey() — so a profile mixing cache-hit and
// cache-miss frames attributes the cost of two different workloads to one
// number.
//
// These two benchmarks separate them:
//
//   - ColdPane dirties the active pane every iteration, so its View() cache
//     misses and the frame includes the pane's own render. This is what a frame
//     costs while a pane is streaming output.
//   - WarmPane leaves the cache hot, so the pane's View() returns a cached
//     string and what remains is CHROME plus FRAME ASSEMBLY — the tab bar, the
//     sidebar, the status bar, and the joins that stitch them together.
//
// WarmPane is the only part a width cache or a faster join could touch. Its
// absolute cost, and the share of it spent in width measurement, is what decides
// whether those optimisations are worth their risk.
//
// Deliberately NOT assuming the answer: the assembly path re-measures every line
// of every already-cached pane string on every frame (renderTabBar calls
// lipgloss.Width per tab; JoinVertical/JoinHorizontal/Place re-measure their
// inputs), so width cost CAN legitimately dominate a warm frame. Expect the gate
// to pass as readily as fail.

// realisticPaneLines approximates what an AI pane actually holds, because the
// package's default filler is uniform ASCII and would under-report exactly the
// cost these benchmarks exist to measure.
//
// Synthetic filler, like benchPaneLine — it only drives the VT grid. The point
// is the SHAPE: box drawing and block glyphs from TUI framing, CJK and emoji at
// two cells each (and a ZWJ sequence, which forces real cluster segmentation),
// combining marks, and SGR runs, mixed with the plain ASCII that is still most
// of any real pane.
var realisticPaneLines = []string{
	"  \x1b[38;5;244m│\x1b[0m Reading \x1b[1minternal/daemon/session.go\x1b[0m … 412 lines\r\n",
	"  ╭──────────────────────────────────────────────────────────────╮\r\n",
	"  │ ✻ Working — 漢字とカタカナ mixed with ASCII, 2 cells each     │\r\n",
	"  │ 👨‍👩‍👧‍👦 a ZWJ cluster, and a combining mark: é é             │\r\n",
	"  ╰──────────────────────────────────────────────────────────────╯\r\n",
	"  \x1b[32m✓\x1b[0m ok  github.com/artyomsv/quil/internal/tui  18.4s\r\n",
	"  plain ascii row so the mix matches a real pane rather than a stress test\r\n",
	"  ▁▂▃▄▅▆▇█ block glyphs ██▓▒░ and arrows → ← ↑ ↓ ⇒ ⟶\r\n",
}

// frameBenchTabCounts spans the range this work cares about: one tab (the
// trivial case), ten (a normal workspace), and 41 (the measured production one).
var frameBenchTabCounts = []int{1, 10, 41}

// BenchmarkFrame_ColdPane measures a frame whose active pane must re-render.
//
// contentGen++ is what PaneModel.View()'s cache keys on, so bumping it forces a
// real pane render. Measuring WITHOUT this is the mistake that produced the
// unreliable 73% figure: every frame served the pane from cache, so the profile
// described chrome while reading like it described everything.
func BenchmarkFrame_ColdPane(b *testing.B) {
	for _, tabs := range frameBenchTabCounts {
		b.Run(fmt.Sprintf("tabs=%d", tabs), func(b *testing.B) {
			m := benchModelContent(tabs, 3, realisticPaneLines)
			m.viewCache = &viewCacheBox{}
			active := m.activeTabModel().Leaves()[0]
			_ = m.View()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				active.contentGen++
				_ = m.View()
			}
		})
	}
}

// BenchmarkFrame_WarmPane measures a frame whose panes are all cache hits.
//
// The delta against ColdPane is the active pane's own render. What this one
// reports is chrome plus assembly — the only part a width cache or a manual join
// can reach.
func BenchmarkFrame_WarmPane(b *testing.B) {
	for _, tabs := range frameBenchTabCounts {
		b.Run(fmt.Sprintf("tabs=%d", tabs), func(b *testing.B) {
			m := benchModelContent(tabs, 3, realisticPaneLines)
			m.viewCache = &viewCacheBox{}
			_ = m.View() // prime every cache
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

// BenchmarkFrame_ChromeOnly isolates the two pieces that walk EVERY pane in the
// workspace rather than just the visible one, so their cost grows with tabs the
// user cannot see. If width measurement matters anywhere, it is most likely
// here: renderTabBar calls lipgloss.Width once per tab, every frame.
func BenchmarkFrame_ChromeOnly(b *testing.B) {
	for _, tabs := range frameBenchTabCounts {
		b.Run(fmt.Sprintf("tabs=%d", tabs), func(b *testing.B) {
			m := benchModelContent(tabs, 3, realisticPaneLines)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.renderTabBar()
				_ = m.renderStatusBar()
			}
		})
	}
}
