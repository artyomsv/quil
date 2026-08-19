package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The only acceptable specification for these helpers is "byte-identical to
// lipgloss". They exist to skip measurement, not to change output, so every test
// here compares against lipgloss rather than against an expected string.
//
// If one of these ever fails, DELETE the helper rather than adjusting the test.
// lipgloss is the width authority in this codebase; a join that disagrees with
// it produces a frame whose padding does not match the .Width that painted the
// row, which is corruption rather than a cosmetic difference.

func TestJoinVerticalWidth_MatchesLipgloss(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		blocks []string
	}{
		{"single", 10, []string{"1234567890"}},
		{"two uniform", 10, []string{"1234567890", "abcdefghij"}},
		{"three uniform multiline", 5, []string{"12345\n12345", "abcde", "ABCDE\nABCDE\nABCDE"}},
		{"styled", 6, []string{"\x1b[31mred123\x1b[0m", "plain1"}},
		{"wide glyphs", 6, []string{"漢字漢", "abcdef"}},
		{"emoji cluster", 8, []string{"👨‍👩‍👧‍👦abcdef", "12345678"}},
		// Fallback cases: the helper must defer to lipgloss, and the result must
		// still match it exactly.
		{"ragged — must fall back", 10, []string{"1234567890", "short"}},
		{"width mismatch — must fall back", 10, []string{"12345", "12345"}},
		{"empty block — must fall back", 10, []string{"1234567890", ""}},
		{"zero width — must fall back", 0, []string{"a", "b"}},
		{"no blocks", 10, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := lipgloss.JoinVertical(lipgloss.Left, tc.blocks...)
			got := joinVerticalWidth(tc.width, tc.blocks...)
			if got != want {
				t.Errorf("differs from lipgloss\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestJoinHorizontalWidth_MatchesLipgloss(t *testing.T) {
	cases := []struct {
		name          string
		leftW, rightW int
		left, right   string
	}{
		{"uniform same height", 3, 5, "abc\ndef", "12345\n67890"},
		{"single line", 3, 5, "abc", "12345"},
		{"styled", 3, 6, "\x1b[32mabc\x1b[0m\nabc", "plain1\nplain2"},
		{"wide glyphs", 4, 4, "漢字\n漢字", "abcd\nabcd"},
		// Fallbacks.
		{"height mismatch — must fall back", 3, 5, "abc\ndef", "12345"},
		{"ragged left — must fall back", 3, 5, "abc\nd", "12345\n67890"},
		{"wrong declared width — must fall back", 9, 5, "abc\ndef", "12345\n67890"},
		{"empty — must fall back", 3, 5, "", "12345"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := lipgloss.JoinHorizontal(lipgloss.Top, tc.left, tc.right)
			got := joinHorizontalWidth(tc.leftW, tc.rightW, tc.left, tc.right)
			if got != want {
				t.Errorf("differs from lipgloss\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// The interior-line check blockIsWidth deliberately skips: a block whose first
// and last lines are the right width but whose middle is not. The helper is
// allowed to be wrong here ONLY if no such block reaches it, which is what the
// real-frame test below establishes. Pinned so the limitation is deliberate.
func TestBlockIsWidth_OnlyChecksFirstAndLastLine(t *testing.T) {
	ragged := "12345\nshort\n12345"
	if !blockIsWidth(ragged, 5) {
		t.Skip("blockIsWidth now checks interior lines — update the joins' contract note")
	}
	// Documented consequence: for THIS input the fast path would disagree with
	// lipgloss. Nothing in the frame assembly produces it (see the real-frame
	// test), and detecting it costs exactly what the optimisation saves.
	if joinVerticalWidth(5, ragged, "12345") == lipgloss.JoinVertical(lipgloss.Left, ragged, "12345") {
		t.Log("fast path happens to agree here, but the contract does not promise it")
	}
}

// The load-bearing test: run the ACTUAL frame blocks through both paths at
// several workspace sizes and terminal geometries, and require identical output.
// The unit cases above cover the shapes; this covers what production produces.
func TestFrameJoins_MatchLipglossOnRealFrames(t *testing.T) {
	for _, tabs := range []int{1, 6, 41} {
		for _, geom := range [][2]int{{200, 50}, {100, 30}, {80, 24}} {
			name := fmt.Sprintf("tabs=%d/%dx%d", tabs, geom[0], geom[1])
			t.Run(name, func(t *testing.T) {
				m := benchModelContent(tabs, 3, realisticPaneLines)
				m.width, m.height = geom[0], geom[1]
				t.Cleanup(func() {
					for _, proj := range m.projects {
						for _, tab := range proj.tabs {
							for _, p := range tab.Leaves() {
								if p != nil {
									p.Dispose()
								}
							}
						}
					}
				})

				tabH := m.height - chromeHeight
				tab := m.activeTabModel()
				if tab == nil {
					t.Skip("fixture has no active tab at this size")
				}
				tab.SetCanvas(m.paneAreaWidth(), tabH)
				tab.SetChrome(m.projectSidebarWidth())
				tab.Resize(m.paneAreaWidth(), tabH)

				tabBar := m.renderTabBar()
				tabContent := tab.View()
				paneW := m.paneAreaWidth()

				// The model.go:4100 join.
				wantV := lipgloss.JoinVertical(lipgloss.Left, tabBar, tabContent)
				gotV := joinVerticalWidth(paneW, tabBar, tabContent)
				if gotV != wantV {
					t.Errorf("vertical join differs from lipgloss (paneW=%d)", paneW)
				}

				// The model.go:4102 join, when the project sidebar is open.
				if sw := m.projectSidebarWidth(); sw > 0 {
					sidebar := m.renderSidebar(m.sidebarContentHeight())
					wantH := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, wantV)
					gotH := joinHorizontalWidth(sw, paneW, sidebar, gotV)
					if gotH != wantH {
						t.Errorf("horizontal join differs from lipgloss (sidebarW=%d paneW=%d)", sw, paneW)
					}
				}
			})
		}
	}
}

// Guards the claim the whole optimisation rests on. If the frame's blocks stop
// being rectangular, the fast path stops firing — output stays correct via the
// fallback, but the win silently disappears, and a perf regression that fails
// nothing is exactly what this branch exists to prevent.
func TestFrameBlocks_AreRectangular(t *testing.T) {
	m := benchModelContent(41, 3, realisticPaneLines)
	t.Cleanup(func() {
		for _, proj := range m.projects {
			for _, tab := range proj.tabs {
				for _, p := range tab.Leaves() {
					if p != nil {
						p.Dispose()
					}
				}
			}
		}
	})

	tabH := m.height - chromeHeight
	tab := m.activeTabModel()
	tab.SetCanvas(m.paneAreaWidth(), tabH)
	tab.SetChrome(m.projectSidebarWidth())
	tab.Resize(m.paneAreaWidth(), tabH)

	check := func(name, block string, want int) {
		t.Helper()
		for i, line := range strings.Split(block, "\n") {
			if w := lipgloss.Width(line); w != want {
				t.Errorf("%s line %d is %d cells, want %d — the frame is no longer "+
					"rectangular, so joinVerticalWidth falls back and the frame-cost "+
					"win is gone", name, i, w, want)
				return
			}
		}
	}
	check("tabBar", m.renderTabBar(), m.paneAreaWidth())
	check("tabContent", tab.View(), m.paneAreaWidth())
	if sw := m.projectSidebarWidth(); sw > 0 {
		check("sidebar", m.renderSidebar(m.sidebarContentHeight()), sw)
	}
}
