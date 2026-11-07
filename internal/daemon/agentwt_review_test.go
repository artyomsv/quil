package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
)

// The turn's START edge takes the baseline, so a worktree made in a
// repository nobody listed before is still recorded by the first probe.
func TestAgentWT_SeedRecordsBaselineBeforeTheAgentActs(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.seed("pane-a")
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/first", Branch: "first"}}
	repo.branches = []string{"master", "first"}
	s.probe(probeFor("pane-a", "/repo"))

	got := s.entriesSnapshot()
	if len(got) != 1 || got[0].Path != "/w/first" {
		t.Errorf("entries = %+v, want the first worktree recorded", got)
	}
}

// A seed runs once per pane and directory: later start edges cost nothing.
func TestAgentWT_SeedOncePerPaneAndDir(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.seed("pane-a")
	s.seed("pane-a")

	if repo.lists != 1 {
		t.Errorf("listed %d times for two seeds, want 1", repo.lists)
	}
}

// A pane whose AGENT works inside a worktree (Claude's EnterWorktree leaves
// the pane's CWD behind) counts as using it, so no cleanup removes it.
func TestAgentWT_PaneInDirSeesTheAgentsDirectory(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)

	s.probe(probeFor("pane-a", "/w/wt-x/sub"))

	if got := s.paneInDir("/w/wt-x"); got != "pane-a" {
		t.Errorf("paneInDir = %q, want pane-a", got)
	}
	if got := s.paneInDir("/w/wt-x2"); got != "" {
		t.Errorf("paneInDir(sibling) = %q, want none", got)
	}
	s.forgetPane("pane-a")
	if got := s.paneInDir("/w/wt-x"); got != "" {
		t.Errorf("paneInDir after forget = %q, want none", got)
	}
}

// Quil's own branch is scoped to its repository: the same name appearing in
// another repository is still an agent's.
func TestAgentWT_OwnBranchIsScopedToItsRepository(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, _ := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))

	s.noteOwnWorktree("/other", "/w/other-made", "feat/x")
	repo.branches = []string{"master", "feat/x"}
	s.probe(probeFor("pane-a", "/repo"))

	got := s.entriesSnapshot()
	if len(got) != 1 || got[0].Kind != agentWTKindBranch || got[0].Branch != "feat/x" {
		t.Errorf("entries = %+v, want feat/x recorded in /repo", got)
	}
}

// A recorded worktree removed WITHOUT its branch leaves a branch entry with
// the same attribution, so the branch can still be cleaned up.
func TestAgentWT_KeptBranchGetsItsOwnEntry(t *testing.T) {
	repo := &fakeRepo{branches: []string{"master"}}
	s, now := newTestAgentWT(t, repo, agentPanes)
	s.probe(probeFor("pane-a", "/repo"))
	repo.worktrees = []gitworktree.Worktree{{Path: "/w/x", Branch: "x"}}
	repo.branches = []string{"master", "x"}
	s.probe(probeFor("pane-a", "/repo"))

	*now = now.Add(2 * agentWTShortLived)
	repo.worktrees = nil
	s.probe(probeFor("pane-a", "/repo"))

	var wt, br int
	for _, e := range s.entriesSnapshot() {
		switch {
		case e.Kind == agentWTKindWorktree && e.Gone:
			wt++
		case e.Kind == agentWTKindBranch && !e.Gone && e.Branch == "x" && e.PaneID == "pane-a":
			br++
		}
	}
	if wt != 1 || br != 1 {
		t.Errorf("entries = %+v, want a gone worktree and a live branch entry for x", s.entriesSnapshot())
	}
}
