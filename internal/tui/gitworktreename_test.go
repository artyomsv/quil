package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// Copied unconditionally, like every other git_* field: an ABSENT key is
// MEANINGFUL. A pane that moves out of a worktree, or a daemon restart that
// has not re-probed yet, must CLEAR the name rather than keep showing the last
// one it had — the same reason the block around it does not guard on empty.
func TestSyncPaneMeta_ClearsTheWorktreeName(t *testing.T) {
	pane := &PaneModel{ID: "p1", GitWorktree: true, GitWorktreeName: "feat-x"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1"}, false, 0)

	if pane.GitWorktreeName != "" {
		t.Errorf("GitWorktreeName = %q after an update with no git keys, want it cleared", pane.GitWorktreeName)
	}
}

func TestSyncPaneMeta_CarriesTheWorktreeName(t *testing.T) {
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", GitWorktree: true, GitWorktreeName: "feat-x"}, false, 0)

	if pane.GitWorktreeName != "feat-x" {
		t.Errorf("GitWorktreeName = %q, want \"feat-x\"", pane.GitWorktreeName)
	}
}

func TestGitRow_WorktreeName(t *testing.T) {
	tests := []struct {
		name    string
		pane    PaneModel
		want    string // substring the row must contain
		wantNot string // substring the row must NOT contain
	}{
		{
			name: "a name that differs from the branch is shown",
			pane: PaneModel{GitBranch: "feat/refactor", GitWorktree: true, GitWorktreeName: "wt-1"},
			want: "wt-1",
		},
		{
			name:    "the branch with separators swapped is suppressed",
			pane:    PaneModel{GitBranch: "feat/x", GitWorktree: true, GitWorktreeName: "feat-x"},
			want:    "wt",
			wantNot: "feat-x",
		},
		{
			name:    "an identical name is suppressed",
			pane:    PaneModel{GitBranch: "hotfix", GitWorktree: true, GitWorktreeName: "hotfix"},
			want:    "hotfix wt",
			wantNot: "hotfix hotfix",
		},
		{
			name:    "the main checkout names nothing",
			pane:    PaneModel{GitBranch: "master"},
			want:    "master",
			wantNot: "wt",
		},
		{
			name: "a worktree with no name still marks itself",
			pane: PaneModel{GitBranch: "feat/x", GitWorktree: true},
			want: "wt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(gitRow(&tt.pane, 40))
			if !strings.Contains(got, tt.want) {
				t.Errorf("row %q does not contain %q", got, tt.want)
			}
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Errorf("row %q contains %q, which should be suppressed", got, tt.wantNot)
			}
		})
	}
}

// The row must measure EXACTLY its budget at every width. renderSidebar's
// closing .Width(w) WRAPS rather than cuts, and sidebarRowAt maps screen row y
// to rows[y-1] — so one cell of overflow shifts every row below it and the hit
// test starts resolving to the neighbour.
func TestGitRow_WithAWorktreeNameMeasuresExactly(t *testing.T) {
	pane := PaneModel{
		GitBranch:       "feat/some/deeply/nested/branch",
		GitWorktree:     true,
		GitWorktreeName: "a-very-long-worktree-directory-name",
		GitUpstream:     true,
		GitAhead:        12,
		GitBehind:       345,
		GitStale:        true,
	}
	for w := 10; w <= 80; w++ {
		if got := lipgloss.Width(gitRow(&pane, w)); got != w {
			t.Errorf("gitRow(w=%d) measured %d cells", w, got)
		}
	}
}

// The name arrives from a daemon the user may not control under --remote.
// sanitizeRemoteText must run BEFORE any width measurement: lipgloss measures
// an escape as zero cells, so truncation is not a sanitiser.
func TestGitRow_SanitizesTheWorktreeName(t *testing.T) {
	pane := PaneModel{
		GitBranch:       "master",
		GitWorktree:     true,
		GitWorktreeName: "ok\x1b]52;c;cGF5bG9hZA==\x07evil",
	}
	got := gitRow(&pane, 40)
	if strings.Contains(got, "\x1b]52") {
		t.Error("an OSC 52 in the worktree name survived to the rendered row")
	}
}

// The suppression rule is about the BRANCH, so it must compare against the
// branch actually rendered — a detached checkout has none, and a name is all
// the row can offer there.
func TestGitRow_DetachedWorktreeStillNamesItself(t *testing.T) {
	pane := PaneModel{GitDetached: true, GitWorktree: true, GitWorktreeName: "wt-1"}
	got := stripANSI(gitRow(&pane, 40))
	if !strings.Contains(got, "wt-1") {
		t.Errorf("row %q does not name the worktree of a detached checkout", got)
	}
}
