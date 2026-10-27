package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// prTokenRe finds a chip in the VISIBLE text: `#` + digits + an optional
// one-cell indicator. A row may carry the whole token or no `#` at all; a
// partial — `#27` for PR 277 — is a different pull request and the one
// failure these tests exist to rule out.
var prTokenRe = regexp.MustCompile(`#\d+[…~]?`)

func assertWholeChipOrNone(t *testing.T, vis string, number int) {
	t.Helper()
	if !strings.Contains(vis, "#") {
		return
	}
	toks := prTokenRe.FindAllString(vis, -1)
	if len(toks) != 1 {
		t.Fatalf("row %q carries %d chips, want exactly one", vis, len(toks))
	}
	digits := strings.TrimRight(toks[0], "…~")
	if digits != fmt.Sprintf("#%d", number) {
		t.Fatalf("row %q shows %q, a DIFFERENT pull request than #%d — the chip must be whole or absent", vis, digits, number)
	}
}

func TestGitRow_PRChip(t *testing.T) {
	long := "feat/projects-sidebar" // 21 cells; keeps minGitBranchCells (8)
	cases := []struct {
		name       string
		pane       PaneModel
		w          int
		wantTok    string // "" with wantNoHash: no chip at all
		wantNoHash bool
		alsoAbsent []string
	}{
		{"chip at default width", PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: "approved"}, 22, "#277", false, nil},
		{"running", PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: "running"}, 22, "#277…", false, nil},
		{"stale beats running", PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: "running", PRStale: true}, 22, "#277~", false, nil},
		{"git stale keeps the row tilde after the chip", PaneModel{GitBranch: "main", GitStale: true, PRNumber: 277, PRStatus: "open"}, 22, "#277 ~", false, nil},
		{"both stale", PaneModel{GitBranch: "main", GitStale: true, PRNumber: 277, PRStatus: "open", PRStale: true}, 22, "#277~ ~", false, nil},
		// Budget 10 after the floor: chip 5, ↑2 3, ↓1 does not fit the 2 left.
		{"default width, both counts, long branch: chip survives, the last count gives way",
			PaneModel{GitBranch: long, GitUpstream: true, GitAhead: 2, GitBehind: 1, PRNumber: 277, PRStatus: "open"}, 22, "#277", false, []string{"↓"}},
		// With a worktree marker as well, both counts give way before the chip.
		{"default width, wt and both counts: counts give way, chip and wt stay",
			PaneModel{GitBranch: long, GitWorktree: true, GitUpstream: true, GitAhead: 2, GitBehind: 1, PRNumber: 277, PRStatus: "open"}, 22, "#277", false, []string{"↑", "↓"}},
		{"worktree marker fits beside the chip at default width",
			PaneModel{GitBranch: long, GitWorktree: true, PRNumber: 277, PRStatus: "open"}, 22, "#277", false, nil},
		{"a worktree name differing from the branch yields before the chip",
			PaneModel{GitBranch: long, GitWorktree: true, GitWorktreeName: "worktree-one", PRNumber: 277, PRStatus: "open"}, 22, "#277", false, []string{"worktree-one"}},
		{"five digits", PaneModel{GitBranch: "main", PRNumber: 12345, PRStatus: "open"}, 22, "#12345", false, nil},
		{"six digits at default width with a long branch", PaneModel{GitBranch: long, PRNumber: 123456, PRStatus: "open"}, 22, "#123456", false, nil},
		{"at the fit boundary (4 prefix + 8 floor + 5 chip)", PaneModel{GitBranch: long, PRNumber: 277, PRStatus: "open"}, 17, "#277", false, nil},
		{"one below the fit boundary: whole chip gone, no digits", PaneModel{GitBranch: long, PRNumber: 277, PRStatus: "open"}, 16, "", true, nil},
		{"no number, no chip, whatever else is set", PaneModel{GitBranch: "main", PRStatus: "approved", PRStale: true}, 22, "", true, nil},
		{"a chip wider than the whole budget is omitted whole; the branch floor holds",
			PaneModel{GitBranch: long, PRNumber: 123456789, PRStatus: "open"}, 22, "", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gitRow(&tc.pane, tc.w)
			vis := stripANSI(got)
			if w := lipgloss.Width(got); w != tc.w {
				t.Errorf("row measures %d cells, want exactly %d: %q", w, tc.w, vis)
			}
			assertWholeChipOrNone(t, vis, tc.pane.PRNumber)
			if tc.wantTok != "" && !strings.Contains(vis, tc.wantTok) {
				t.Errorf("row %q lacks %q", vis, tc.wantTok)
			}
			if tc.wantNoHash && strings.Contains(vis, "#") {
				t.Errorf("row %q carries a chip, want none", vis)
			}
			for _, no := range tc.alsoAbsent {
				if strings.Contains(vis, no) {
					t.Errorf("row %q still carries %q, which must give way to the chip", vis, no)
				}
			}
		})
	}
}

// The branch floor holds with a chip beside it, as it does against the counts:
// both ends of the branch survive, so a column of rows stays tellable apart.
func TestGitRow_BranchFloorHoldsWithChip(t *testing.T) {
	pane := PaneModel{GitBranch: "feat/projects-sidebar", GitUpstream: true, GitAhead: 2, GitBehind: 1, PRNumber: 277, PRStatus: "attention"}
	vis := stripANSI(gitRow(&pane, defaultSidebarWidth))
	if !strings.Contains(vis, "fea") || !strings.Contains(vis, "bar") {
		t.Fatalf("row %q lost an end of the branch to the chip", vis)
	}
	if !strings.Contains(vis, "#277") {
		t.Fatalf("row %q lost the chip to the branch", vis)
	}
}

// The chip is painted in its status color; a status the TUI does not know
// renders exactly as open does, so a newer daemon cannot blank an older
// client's chip or paint it a color it never chose.
func TestGitRow_ChipColorByStatusAndUnknownIsGrey(t *testing.T) {
	if styleSGR(t, prChipStyle("approved")) == "" {
		t.Skip("lipgloss renders without color here — this assertion cannot discriminate")
	}
	for _, status := range []string{"merged", "closed", "attention", "running", "approved", "draft", "open"} {
		pane := PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: status}
		want := prChipStyle(status).Render(prChip(&pane))
		if got := gitRow(&pane, defaultSidebarWidth); !strings.Contains(got, want) {
			t.Errorf("status %q: row %q lacks the chip rendered in its own style %q", status, got, want)
		}
	}
	unknown := PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: "no-such-status"}
	open := PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: "open"}
	if gitRow(&unknown, defaultSidebarWidth) != gitRow(&open, defaultSidebarWidth) {
		t.Fatal("an unknown status renders differently from open")
	}
	if a, b := styleSGR(t, prChipStyle("attention")), styleSGR(t, prChipStyle("approved")); a == b {
		t.Fatal("attention and approved share a color")
	}
}

// Under a row highlight the row is ONE style (rowHighlight.line), as every
// other sidebar row is: the chip's own color escape is absent and the text
// still carries the number.
func TestGitRow_HighlightedRowIsOneStyle(t *testing.T) {
	pane := PaneModel{GitBranch: "main", PRNumber: 277, PRStatus: "approved"}
	chipSGR := styleSGR(t, prChipStyle("approved"))
	if chipSGR == "" {
		t.Skip("lipgloss renders without color here")
	}
	got := gitRowHL(&pane, defaultSidebarWidth, rowHighlightHover)
	if strings.Contains(got, chipSGR) {
		t.Fatalf("highlighted row %q keeps the chip's own color; a highlighted row is one style", got)
	}
	if !strings.Contains(stripANSI(got), "#277") {
		t.Fatalf("highlighted row %q lost the chip text", stripANSI(got))
	}
	if w := lipgloss.Width(got); w != defaultSidebarWidth {
		t.Fatalf("highlighted row measures %d, want %d", w, defaultSidebarWidth)
	}
}

func TestPRChip_Token(t *testing.T) {
	cases := []struct {
		pane PaneModel
		want string
	}{
		{PaneModel{}, ""},
		{PaneModel{PRNumber: 7, PRStatus: "open"}, "#7"},
		{PaneModel{PRNumber: 7, PRStatus: "running"}, "#7…"},
		{PaneModel{PRNumber: 7, PRStatus: "running", PRStale: true}, "#7~"},
		{PaneModel{PRNumber: 7, PRStatus: "merged", PRStale: true}, "#7~"},
	}
	for _, tc := range cases {
		if got := prChip(&tc.pane); got != tc.want {
			t.Errorf("prChip(%+v) = %q, want %q", tc.pane, got, tc.want)
		}
	}
}
