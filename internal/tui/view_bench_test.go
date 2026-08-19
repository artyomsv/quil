package tui

import (
	"fmt"
	"sync"
	"testing"
)

// Benchmarks for the View() hot path.
//
// Production measurement 2026-08-18 (37 tabs, 37 panes): View ran 15-30x/s at
// 3.5-6 ms each, ~22% of one core, driven almost entirely by TIMER messages
// (the 10 Hz work spinner is ~65% of renders) rather than by pane output, which
// was only ~1.5 KB/s. Bubble Tea calls model.View() once per message — its FPS
// option throttles the terminal flush, not the View construction.
//
// These benchmarks exist to answer two questions with numbers rather than
// argument: which part of a frame costs the most, and how much of a frame could
// concurrency actually recover.

// benchPaneContent is deliberately synthetic filler — it only drives the VT
// grid so a pane has something to render. Width and line count roughly match a
// pane in a 3-column terminal.
const benchPaneLine = "  bench-pane row of terminal text for the vt grid to lay out and render\r\n"

// benchModel builds a Model shaped like the measured production workspace:
// tabs spread across projects, one pane each, every pane holding enough output
// that the emulator has a populated grid.
func benchModel(tabs, projects int) Model {
	return benchModelContent(tabs, projects, []string{benchPaneLine})
}

// benchModelContent is benchModel with the pane filler chosen by the caller.
//
// Split out because the ASCII line above under-reports width measurement: every
// rune is one cell, so lipgloss's fast path handles it and grapheme segmentation
// never does real work. A benchmark used to decide whether to CACHE width
// measurement has to run against content shaped like the real thing — see
// realisticPaneLines.
func benchModelContent(tabs, projects int, lines []string) Model {
	ps := make([]*ProjectModel, projects)
	for i := range ps {
		ps[i] = &ProjectModel{ID: fmt.Sprintf("proj-%d", i), Name: fmt.Sprintf("project-%d", i)}
	}
	for i := 0; i < tabs; i++ {
		pane := NewPaneModel(fmt.Sprintf("pane-%d", i), 64*1024)
		for l := 0; l < 40; l++ {
			pane.AppendOutput([]byte(lines[l%len(lines)]))
		}
		tab := tabWith(pane)
		tab.Name = fmt.Sprintf("tab-%d", i)
		p := ps[i%projects]
		p.tabs = append(p.tabs, tab)
	}
	return Model{
		projects:      ps,
		activeProject: 0,
		sidebarOpen:   true,
		sidebarWidth:  defaultSidebarWidth,
		width:         200,
		height:        50,
		termFocused:   true,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: map[string]bool{},
	}
}

// BenchmarkView_ByTabCount shows how a whole frame scales with tab count. The
// sidebar and tab bar both walk EVERY pane in the workspace, so a frame's cost
// is expected to grow with panes the user cannot see.
func BenchmarkView_ByTabCount(b *testing.B) {
	for _, n := range []int{1, 5, 10, 20, 37} {
		m := benchModel(n, 3)
		b.Run(fmt.Sprintf("tabs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

// BenchmarkViewParts splits a 37-tab frame into the three independent pieces
// View() joins, so we can see which one to attack.
func BenchmarkViewParts(b *testing.B) {
	m := benchModel(37, 3)

	b.Run("renderTabBar", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = m.renderTabBar()
		}
	})

	b.Run("renderSidebar", func(b *testing.B) {
		b.ReportAllocs()
		h := m.sidebarContentHeight()
		for i := 0; i < b.N; i++ {
			_ = m.renderSidebar(h)
		}
	})

	b.Run("activeTabView", func(b *testing.B) {
		b.ReportAllocs()
		tab := m.activeTabModel()
		tabH := m.height - chromeHeight
		for i := 0; i < b.N; i++ {
			tab.SetCanvas(m.paneAreaWidth(), tabH)
			tab.SetChrome(m.projectSidebarWidth())
			tab.Resize(m.paneAreaWidth(), tabH)
			_ = tab.View()
		}
	})

	b.Run("wholeView", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = m.View()
		}
	})
}

// BenchmarkCompose_SequentialVsParallel measures the CEILING on parallelising a
// frame: the three independent renders computed one after another, versus the
// same three on their own goroutines and joined.
//
// This is an upper bound, not an implementation proposal. The real View()
// mutates while it renders (SetCanvas/Resize on the tab, activeSel/focusMode on
// each pane), so a production version would need that mutation hoisted or
// synchronised — cost this benchmark does not pay. If the parallel number is
// not decisively better HERE, it cannot be better in production.
func BenchmarkCompose_SequentialVsParallel(b *testing.B) {
	m := benchModel(37, 3)
	h := m.sidebarContentHeight()
	tab := m.activeTabModel()
	tabH := m.height - chromeHeight
	tab.SetCanvas(m.paneAreaWidth(), tabH)
	tab.SetChrome(m.projectSidebarWidth())
	tab.Resize(m.paneAreaWidth(), tabH)

	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			bar := m.renderTabBar()
			side := m.renderSidebar(h)
			content := tab.View()
			_, _, _ = bar, side, content
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			var bar, side, content string
			wg.Add(3)
			go func() { defer wg.Done(); bar = m.renderTabBar() }()
			go func() { defer wg.Done(); side = m.renderSidebar(h) }()
			go func() { defer wg.Done(); content = tab.View() }()
			wg.Wait()
			_, _, _ = bar, side, content
		}
	})
}
