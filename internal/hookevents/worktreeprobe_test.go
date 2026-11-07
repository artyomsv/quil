package hookevents

import "testing"

// Fails if the pattern stops matching a creation or deletion shape, or starts
// matching an ordinary git command.
func TestIsWorktreeCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"git worktree add ../x -b y", true},
		{"cd a && git -C r worktree remove p", true},
		{"git switch -c feat", true},
		{"git checkout -b feat", true},
		{"git branch feat", true},
		{"git branch -D feat", true},
		{"git push origin --delete feat", true},
		{"git push origin :feat", true},
		{"GIT WORKTREE LIST", true},
		// Accepted false positive: the word appears in a file name. One
		// listing is the whole cost.
		{"ls worktrees.md", true},
		{"git status", false},
		{"git push origin feat", false},
		{"git branches-report", false},
		{"npm test", false},
	}
	for _, tt := range tests {
		if got := IsWorktreeCommand(tt.cmd); got != tt.want {
			t.Errorf("IsWorktreeCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// A probe is a trigger for the daemon, not a work-state edge: classifying it
// as one would light or clear a spinner on every git command an agent runs.
func TestClassify_WorktreeProbeIsNotWorkState(t *testing.T) {
	for _, src := range []string{"claude", "codex", "opencode"} {
		if k := ClassifyWorkEvent("hook." + src + "." + EventWorktreeProbe); k != WorkEventNone {
			t.Errorf("%s probe classified as %v, want WorkEventNone", src, k)
		}
	}
}
