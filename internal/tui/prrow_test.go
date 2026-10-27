package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestPRRow_Text(t *testing.T) {
	cases := []struct {
		name string
		pane PaneModel
		want string // visible text, trailing pad trimmed; "" = no row
	}{
		{"no PR, no row", PaneModel{GitBranch: "main"}, ""},
		{"open", PaneModel{PRNumber: 278, PRStatus: "open"}, "    #278 open"},
		{"approved", PaneModel{PRNumber: 278, PRStatus: "approved"}, "    #278 approved"},
		{"running is a word, not a glyph", PaneModel{PRNumber: 278, PRStatus: "running"}, "    #278 running"},
		{"attention", PaneModel{PRNumber: 278, PRStatus: "attention"}, "    #278 attention"},
		{"stale keeps the git row's mark", PaneModel{PRNumber: 278, PRStatus: "merged", PRStale: true}, "    #278 merged ~"},
		{"unknown status reads as open", PaneModel{PRNumber: 9, PRStatus: "no-such-status"}, "    #9 open"},
		{"empty status reads as open", PaneModel{PRNumber: 9}, "    #9 open"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prRow(&tc.pane, defaultSidebarWidth)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("prRow = %q, want no row", got)
				}
				return
			}
			if w := lipgloss.Width(got); w != defaultSidebarWidth {
				t.Errorf("row measures %d cells, want exactly %d", w, defaultSidebarWidth)
			}
			if vis := strings.TrimRight(stripANSI(got), " "); vis != tc.want {
				t.Errorf("row = %q, want %q", vis, tc.want)
			}
		})
	}
}

// The git row no longer carries the number: a chip there stole the width a
// long branch needs, which is why the PR has a row of its own.
func TestGitRow_CarriesNoPRChip(t *testing.T) {
	pane := PaneModel{GitBranch: "feat/x", GitWorktree: true, PRNumber: 278, PRStatus: "approved"}
	if got := stripANSI(gitRow(&pane, defaultSidebarWidth)); strings.Contains(got, "#") {
		t.Fatalf("git row %q carries the PR; it belongs on prRow", got)
	}
}

// Exact width at every width the sidebar can take; the number survives whole
// wherever it fits, the status word gives way first.
func TestPRRow_WidthAndNumberFirst(t *testing.T) {
	pane := PaneModel{PRNumber: 123456, PRStatus: "attention", PRStale: true}
	for w := minSidebarWidth; w <= 40; w++ {
		got := prRow(&pane, w)
		if n := lipgloss.Width(got); n != w {
			t.Errorf("w=%d: row is %d cells", w, n)
		}
		if !strings.Contains(stripANSI(got), "#123456") {
			t.Errorf("w=%d: row %q cut the number before the status word", w, stripANSI(got))
		}
	}
	for _, w := range []int{1, 2, 3, 4, 5, 8} {
		if n := lipgloss.Width(prRow(&pane, w)); n != w {
			t.Errorf("w=%d: row is %d cells", w, n)
		}
	}
}

// PRStale is the ONLY sign that the status is cached — a row in its normal
// status color with the marker cut off shows an old approval as current for
// the whole retry backoff. The marker is reserved beside the number; the
// status word is what gives way. Stale and fresh rows must differ wherever
// the number and the marker both fit.
func TestPRRow_StaleMarkerSurvivesTruncation(t *testing.T) {
	cases := []struct {
		name   string
		number int
		status string
	}{
		{"short number, long word", 278, "approved"},
		{"long number, long word", 123456, "attention"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stale := PaneModel{PRNumber: tc.number, PRStatus: tc.status, PRStale: true}
			fresh := PaneModel{PRNumber: tc.number, PRStatus: tc.status}
			num := "#" + strconv.Itoa(tc.number)
			for w := minSidebarWidth; w <= 40; w++ {
				s := strings.TrimRight(stripANSI(prRow(&stale, w)), " ")
				f := strings.TrimRight(stripANSI(prRow(&fresh, w)), " ")
				markerFits := lipgloss.Width(num)+lipgloss.Width(" ~") <= w-4
				if markerFits && !strings.HasSuffix(s, " ~") {
					t.Errorf("w=%d: stale row %q lost its marker although %q fits", w, s, num+" ~")
				}
				if markerFits && s == f {
					t.Errorf("w=%d: stale and fresh rows are identical: %q", w, s)
				}
				if !strings.Contains(s, num) {
					t.Errorf("w=%d: stale row %q cut the number", w, s)
				}
			}
		})
	}
	// The documented example: width 18, #278 approved, stale.
	p := PaneModel{PRNumber: 278, PRStatus: "approved", PRStale: true}
	if got := strings.TrimRight(stripANSI(prRow(&p, 18)), " "); got != "    #278 approve ~" {
		t.Fatalf("w=18 stale row = %q, want the word cut and the marker kept", got)
	}
}

func TestPRRow_ColorByStatusAndUnknownIsOpen(t *testing.T) {
	if styleSGR(t, prRowStyle("approved")) == "" {
		t.Skip("lipgloss renders without color here — this assertion cannot discriminate")
	}
	for _, status := range []string{"merged", "closed", "attention", "running", "approved", "draft", "open"} {
		pane := PaneModel{PRNumber: 278, PRStatus: status}
		if got, want := prRow(&pane, defaultSidebarWidth), styleSGR(t, prRowStyle(status)); !strings.Contains(got, want) {
			t.Errorf("status %q: row %q is not painted in its own style", status, got)
		}
	}
	unknown := PaneModel{PRNumber: 278, PRStatus: "no-such-status"}
	open := PaneModel{PRNumber: 278, PRStatus: "open"}
	if prRow(&unknown, defaultSidebarWidth) != prRow(&open, defaultSidebarWidth) {
		t.Fatal("an unknown status renders differently from open")
	}
	if a, b := styleSGR(t, prRowStyle("attention")), styleSGR(t, prRowStyle("approved")); a == b {
		t.Fatal("attention and approved share a color")
	}
}

// Under a row highlight the row is ONE style, like every other sidebar row.
func TestPRRow_HighlightedRowIsOneStyle(t *testing.T) {
	pane := PaneModel{PRNumber: 278, PRStatus: "approved"}
	own := styleSGR(t, prRowStyle("approved"))
	if own == "" {
		t.Skip("lipgloss renders without color here")
	}
	got := prRowHL(&pane, defaultSidebarWidth, rowHighlightHover)
	if strings.Contains(got, own) {
		t.Fatalf("highlighted row %q keeps its own color", got)
	}
	if !strings.Contains(stripANSI(got), "#278 approved") {
		t.Fatalf("highlighted row %q lost its text", stripANSI(got))
	}
	if w := lipgloss.Width(got); w != defaultSidebarWidth {
		t.Fatalf("highlighted row measures %d, want %d", w, defaultSidebarWidth)
	}
}

// Through the Model: the PR row sits directly under the pane's git row, and a
// pane with no PR has no such row — the PANES section pays the row only
// where there is something to say.
func TestSidebar_PRRowFollowsTheGitRow(t *testing.T) {
	withPR := &PaneModel{ID: "p1", GitBranch: "feat/git-pr", GitWorktree: true, PRNumber: 278, PRStatus: "open"}
	without := &PaneModel{ID: "p2", GitBranch: "master"}
	tab := NewTabModel("tab-1", "Shell")
	tab.Root = &LayoutNode{Left: NewLeaf(withPR), Right: NewLeaf(without), Ratio: 0.5}
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "quil", tabs: []*TabModel{tab}}},
		sidebarOpen:  true,
		sidebarWidth: defaultSidebarWidth,
	}
	lines := strings.Split(stripANSI(m.renderSidebar(20)), "\n")
	gitAt, prAt, hashes := -1, -1, 0
	for i, l := range lines {
		if strings.Contains(l, "feat/git-pr") {
			gitAt = i
		}
		if strings.Contains(l, "#278 open") {
			prAt = i
		}
		if strings.Contains(l, "#") {
			hashes++
		}
	}
	if gitAt < 0 || prAt != gitAt+1 {
		t.Fatalf("PR row at %d, git row at %d; want the PR row directly under the branch:\n%s", prAt, gitAt, strings.Join(lines, "\n"))
	}
	if hashes != 1 {
		t.Fatalf("%d rows carry a #, want exactly one (the pane with a PR):\n%s", hashes, strings.Join(lines, "\n"))
	}
}
