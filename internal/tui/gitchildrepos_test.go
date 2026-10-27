package tui

import (
	"strings"
	"testing"
)

// A pane whose folder is not a checkout but holds some: the daemon folds the
// children (gitinfo.Combine) and the row shows the shared branch, or the count
// when there is none. The count is a new omitempty key, so like every other
// git_* field it is copied unconditionally — an absent key must clear it.
func TestSyncPaneMeta_CarriesAndClearsTheChildRepoCount(t *testing.T) {
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", GitChildRepos: 2}, false, 0, false, false)
	if pane.GitChildRepos != 2 {
		t.Fatalf("GitChildRepos = %d, want 2", pane.GitChildRepos)
	}
	syncPaneMeta(pane, &PaneInfo{ID: "p1"}, false, 0, false, false)
	if pane.GitChildRepos != 0 {
		t.Errorf("GitChildRepos = %d after an update with no git keys, want it cleared", pane.GitChildRepos)
	}
}

func TestGitRow_ChildRepos(t *testing.T) {
	tests := []struct {
		name    string
		pane    PaneModel
		want    string
		wantNot string
	}{
		{
			name: "children on different branches show the count",
			pane: PaneModel{GitChildRepos: 2},
			want: "2 repos",
		},
		{
			name: "a single child whose probe has not landed is still a repo",
			pane: PaneModel{GitChildRepos: 1},
			want: "1 repo",
		},
		{
			name:    "children on one branch show that branch, not the count",
			pane:    PaneModel{GitBranch: "ticket/x", GitChildRepos: 2},
			want:    "ticket/x",
			wantNot: "repos",
		},
		{
			name: "summed divergence rides the count",
			pane: PaneModel{GitChildRepos: 3, GitUpstream: true, GitAhead: 4},
			want: "3 repos ↑4",
		},
		{
			name:    "all detached is detached, not a count",
			pane:    PaneModel{GitDetached: true, GitChildRepos: 2},
			want:    "detached",
			wantNot: "repos",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := stripANSI(gitRow(&tc.pane, 30))
			if !strings.Contains(row, tc.want) {
				t.Errorf("row %q does not contain %q", row, tc.want)
			}
			if tc.wantNot != "" && strings.Contains(row, tc.wantNot) {
				t.Errorf("row %q must not contain %q", row, tc.wantNot)
			}
		})
	}
}

// No branch, no count, no worktree: no row. The count must not make an
// ordinary non-repository pane grow one.
func TestGitRow_NoChildReposIsNoRow(t *testing.T) {
	if row := gitRow(&PaneModel{}, 30); row != "" {
		t.Errorf("gitRow for a pane with nothing to say = %q, want empty", row)
	}
}
