package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestGitRow(t *testing.T) {
	tests := []struct {
		name     string
		pane     PaneModel
		contains []string
		absent   []string
		empty    bool
	}{
		{
			name:  "no repository renders no row at all",
			pane:  PaneModel{},
			empty: true,
		},
		{
			name:     "branch only",
			pane:     PaneModel{GitBranch: "master"},
			contains: []string{"master"},
			absent:   []string{"↑", "↓", "wt"},
		},
		{
			// "0 ahead, 0 behind" and "no upstream to compare against" are
			// different facts; only one is true here.
			name:     "no upstream shows no counts",
			pane:     PaneModel{GitBranch: "local-only"},
			contains: []string{"local-only"},
			absent:   []string{"↑", "↓"},
		},
		{
			name:     "in sync with upstream shows no counts",
			pane:     PaneModel{GitBranch: "main", GitUpstream: true},
			contains: []string{"main"},
			absent:   []string{"↑", "↓"},
		},
		{
			name:     "divergence",
			pane:     PaneModel{GitBranch: "wip", GitUpstream: true, GitAhead: 2, GitBehind: 5},
			contains: []string{"wip", "↑2", "↓5"},
		},
		{
			name:     "linked worktree",
			pane:     PaneModel{GitBranch: "feature", GitWorktree: true},
			contains: []string{"feature", "wt"},
		},
		{
			name:     "detached head is named rather than blank",
			pane:     PaneModel{GitDetached: true},
			contains: []string{"detached"},
		},
		{
			name:     "stale is marked, not hidden",
			pane:     PaneModel{GitBranch: "main", GitStale: true},
			contains: []string{"main", "~"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gitRow(&tc.pane, defaultSidebarWidth)
			if tc.empty {
				if got != "" {
					t.Fatalf("gitRow = %q, want empty", got)
				}
				return
			}
			if got == "" {
				t.Fatal("gitRow returned empty")
			}
			if w := lipgloss.Width(got); w != defaultSidebarWidth {
				t.Errorf("row measures %d cells, want exactly %d", w, defaultSidebarWidth)
			}
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("row %q missing %q", got, want)
				}
			}
			for _, no := range tc.absent {
				if strings.Contains(got, no) {
					t.Errorf("row %q unexpectedly contains %q", got, no)
				}
			}
		})
	}
}

// The branch is the answer; the counts are detail about it. Subtracting the
// counts from the budget first leaves "fea…" beside a crisp "↑12↓34" — the
// same inversion paneRow already guards against.
func TestGitRow_BranchKeepsItsFloorAgainstLongCounts(t *testing.T) {
	pane := PaneModel{
		GitBranch:   "feat/projects-sidebar",
		GitWorktree: true,
		GitUpstream: true,
		GitAhead:    123,
		GitBehind:   456,
		GitStale:    true,
	}
	got := gitRow(&pane, defaultSidebarWidth)
	if !strings.Contains(got, "feat/pro") {
		t.Errorf("row %q dropped the branch name; the counts crowded it out", got)
	}
	if w := lipgloss.Width(got); w != defaultSidebarWidth {
		t.Errorf("row measures %d cells, want exactly %d", w, defaultSidebarWidth)
	}
}
