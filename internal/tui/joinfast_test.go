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
		// The shape that mutation testing proved was missing: BOTH blocks match
		// on the first line and fail on the last (and the mirror image).
		//
		// Every other ragged case here pairs a malformed block with a uniform
		// partner, and since both must pass blockIsWidth for the fast path to
		// fire, the partner's correct `false` masks an inverted check's wrong
		// `true`. Inverting either comparison in blockIsWidth survived this whole
		// file until these two cases existed — while producing genuinely
		// unpadded output, not merely a lost optimisation.
		{"first matches, last does not — both blocks", 5, []string{"12345\nsh", "abcde\nxy"}},
		{"last matches, first does not — both blocks", 5, []string{"sh\n12345", "xy\nabcde"}},

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
		// The masking shape, horizontal side: both blocks match on the first
		// line and fail on the last. See the vertical table for why the other
		// ragged cases cannot catch an inverted check.
		{"first matches, last does not — both blocks", 3, 5, "abc\nx", "12345\ny"},
		{"last matches, first does not — both blocks", 3, 5, "x\nabc", "y\n12345"},
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
	// "sh" is 2 cells against a declared 5 — genuinely ragged in the INTERIOR.
	//
	// The first version of this test used "short", which is 5 cells: the fixture
	// was rectangular, blockIsWidth returned true for the right reason, and the
	// test logged "happens to agree" while asserting nothing. It named a hole it
	// did not exercise.
	ragged := "12345\nsh\n12345"

	if !blockIsWidth(ragged, 5) {
		t.Fatal("blockIsWidth now inspects interior lines — the joins' contract note " +
			"and the two comments in joinfast.go must be updated to match")
	}

	// The consequence, asserted rather than described: for a block ragged in its
	// interior the fast path DIFFERS from lipgloss, because lipgloss pads the
	// short line and a concatenation does not.
	got := joinVerticalWidth(5, ragged, "12345")
	want := lipgloss.JoinVertical(lipgloss.Left, ragged, "12345")
	if got == want {
		t.Errorf("fast path agreed with lipgloss on an interior-ragged block; either "+
			"blockIsWidth got stricter or lipgloss stopped padding — re-derive the "+
			"contract before trusting it\n got: %q", got)
	}

	// Same for the horizontal join, where the divergence is worse: the right
	// column shifts left on the short row rather than merely being unpadded.
	l, r := "abc\na\nabc", "12345\n67890\nABCDE"
	if gotH, wantH := joinHorizontalWidth(3, 5, l, r), lipgloss.JoinHorizontal(lipgloss.Top, l, r); gotH == wantH {
		t.Error("horizontal fast path agreed with lipgloss on an interior-ragged block")
	}
}

// The premise that makes the unsound guard acceptable, checked across the same
// geometry matrix the equivalence test uses rather than one default state.
//
// If a frame block ever becomes ragged, the fast path is WRONG for it — so this
// is the test that has to be broad, not the one that documents the hole.
func TestFrameBlocks_AreRectangularAcrossGeometries(t *testing.T) {
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

				tab := m.activeTabModel()
				if tab == nil {
					t.Skip("fixture has no active tab at this size")
				}
				tabH := m.height - chromeHeight
				tab.SetCanvas(m.paneAreaWidth(), tabH)
				tab.SetChrome(m.projectSidebarWidth())
				tab.Resize(m.paneAreaWidth(), tabH)

				check := func(label, block string, want int) {
					t.Helper()
					for i, line := range strings.Split(block, "\n") {
						if w := lipgloss.Width(line); w != want {
							t.Errorf("%s line %d is %d cells, want %d — a ragged block "+
								"makes joinVerticalWidth DISAGREE with lipgloss, which is "+
								"frame corruption, not a lost optimisation", label, i, w, want)
							return
						}
					}
				}
				check("tabBar", m.renderTabBar(), m.paneAreaWidth())
				check("tabContent", tab.View(), m.paneAreaWidth())
				if sw := m.projectSidebarWidth(); sw > 0 {
					check("sidebar", m.renderSidebar(m.sidebarContentHeight()), sw)
				}
			})
		}
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

				// The tab-bar + pane-area join in View().
				wantV := lipgloss.JoinVertical(lipgloss.Left, tabBar, tabContent)
				gotV := joinVerticalWidth(paneW, tabBar, tabContent)
				if gotV != wantV {
					t.Errorf("vertical join differs from lipgloss (paneW=%d)", paneW)
				}

				// The sidebar + pane-area join in View(), when the project sidebar is open.
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

