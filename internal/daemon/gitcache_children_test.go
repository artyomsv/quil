package daemon

import (
	"context"
	"testing"

	"github.com/artyomsv/quil/internal/gitinfo"
)

// The real case: a ticket folder that is not a repository, whose two children
// are linked worktrees on the same branch. The folder's pane reports that
// branch, and every child is probed in ITS OWN directory — never the parent,
// where git resolves nothing.
func TestGitCache_NonRepositoryReportsItsChildrensSharedBranch(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{},
		children: map[string][]gitinfo.ChildRepo{
			"/w/T-1": {
				{Dir: "/w/T-1/api", GitDir: "/api/.git/worktrees/T-1", CommonDir: "/api/.git"},
				{Dir: "/w/T-1/web", GitDir: "/web/.git/worktrees/T-1", CommonDir: "/web/.git"},
			},
		},
		infos: map[string]gitinfo.Info{
			"/w/T-1/api": {Branch: "T-1/feature", LinkedWorktree: true, WorktreeName: "T-1", HasUpstream: true, Ahead: 2},
			"/w/T-1/web": {Branch: "T-1/feature", LinkedWorktree: true, WorktreeName: "T-1", HasUpstream: true, Ahead: 1, Behind: 3},
		},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/w/T-1"})

	info, ok, stale := c.lookup("/w/T-1")
	if !ok {
		t.Fatal("a folder of child checkouts reported nothing")
	}
	if stale {
		t.Error("two answered probes must not read as stale")
	}
	want := gitinfo.Info{
		Branch: "T-1/feature", LinkedWorktree: true, WorktreeName: "T-1",
		HasUpstream: true, Ahead: 3, Behind: 3, ChildRepos: 2,
	}
	if info != want {
		t.Errorf("lookup = %+v\nwant     %+v", info, want)
	}
	f.mu.Lock()
	probes := append([]string(nil), f.probeCalls...)
	f.mu.Unlock()
	if len(probes) != 2 || probes[0] != "/w/T-1/api" || probes[1] != "/w/T-1/web" {
		t.Errorf("probes ran in %v, want each child in its own directory", probes)
	}
}

func TestGitCache_ChildrenOnDifferentBranchesReportOnlyTheCount(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{},
		children: map[string][]gitinfo.ChildRepo{
			"/w": {
				{Dir: "/w/a", GitDir: "/w/a/.git", CommonDir: "/w/a/.git"},
				{Dir: "/w/b", GitDir: "/w/b/.git", CommonDir: "/w/b/.git"},
			},
		},
		infos: map[string]gitinfo.Info{
			"/w/a": {Branch: "main"},
			"/w/b": {Branch: "dev"},
		},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/w"})

	info, ok, _ := c.lookup("/w")
	if !ok {
		t.Fatal("a folder of child checkouts reported nothing")
	}
	if info.Branch != "" || info.ChildRepos != 2 {
		t.Errorf("lookup = %+v, want no branch and ChildRepos=2", info)
	}
}

// A pane in the parent and a pane inside one child resolve to the SAME entry
// for that child: the per-checkout key collapses them, so the second pane
// costs no probe.
func TestGitCache_ParentAndChildPanesShareTheChildsProbe(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{
			"/w/a": {"/w/a/.git", "/w/a/.git"},
		},
		children: map[string][]gitinfo.ChildRepo{
			"/w": {{Dir: "/w/a", GitDir: "/w/a/.git", CommonDir: "/w/a/.git"}},
		},
		infos: map[string]gitinfo.Info{"/w/a": {Branch: "main"}},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/w", "/w/a"})

	if n := f.probeCount(); n != 1 {
		t.Errorf("%d probes for a parent pane and a child pane of one checkout, want 1", n)
	}
	parent, ok, _ := c.lookup("/w")
	if !ok || parent.Branch != "main" || parent.ChildRepos != 1 {
		t.Errorf("parent lookup = %+v ok=%v, want the child's branch over 1 repo", parent, ok)
	}
	child, ok, _ := c.lookup("/w/a")
	if !ok || child.Branch != "main" || child.ChildRepos != 0 {
		t.Errorf("child lookup = %+v ok=%v, want its own branch with no count", child, ok)
	}
}

// The walk runs when a CWD is resolved, which for a non-repository is once per
// negative TTL — not once per tick. Three refreshes, one walk.
func TestGitCache_ChildWalkRunsOncePerResolution(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{},
		children: map[string][]gitinfo.ChildRepo{
			"/w": {{Dir: "/w/a", GitDir: "/w/a/.git", CommonDir: "/w/a/.git"}},
		},
		infos: map[string]gitinfo.Info{"/w/a": {Branch: "main"}},
	}
	installFakeGit(t, f)

	c := newGitCache()
	for i := 0; i < 3; i++ {
		c.refresh(context.Background(), []string{"/w"})
	}

	f.mu.Lock()
	walks := len(f.childCalls)
	f.mu.Unlock()
	if walks != 1 {
		t.Errorf("%d child walks across three refreshes, want 1", walks)
	}
}

// A plain non-repository — no children either — is exactly as before: one
// walk at resolution, nothing reported.
func TestGitCache_NonRepositoryWithNoChildrenReportsNothing(t *testing.T) {
	f := &fakeGitProbes{dirs: map[string][2]string{}}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/tmp"})

	if _, ok, _ := c.lookup("/tmp"); ok {
		t.Error("a non-repository with no child checkouts must not report git state")
	}
	if n := f.probeCount(); n != 0 {
		t.Errorf("%d probes for a folder with nothing under it, want 0", n)
	}
}

// One child that stops answering makes the folder's row stale: it claims one
// thing about the whole folder, and cannot be current while a part is not.
func TestGitCache_OneStaleChildMakesTheFolderStale(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{},
		children: map[string][]gitinfo.ChildRepo{
			"/w": {
				{Dir: "/w/a", GitDir: "/w/a/.git", CommonDir: "/w/a/.git"},
				{Dir: "/w/b", GitDir: "/w/b/.git", CommonDir: "/w/b/.git"},
			},
		},
		infos: map[string]gitinfo.Info{
			"/w/a": {Branch: "main"},
			"/w/b": {Branch: "main"},
		},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/w"})
	if _, _, stale := c.lookup("/w"); stale {
		t.Fatal("two answered probes must not read as stale")
	}

	f.mu.Lock()
	delete(f.infos, "/w/b")
	f.mu.Unlock()
	c.refresh(context.Background(), []string{"/w"})

	info, ok, stale := c.lookup("/w")
	if !ok || !stale {
		t.Errorf("lookup ok=%v stale=%v after one child stopped answering, want ok and stale", ok, stale)
	}
	if info.Branch != "main" {
		t.Errorf("Branch = %q, want the last observed value kept", info.Branch)
	}
}

// The children are swept with the parent pane: a checkout no live pane is in,
// directly or through a parent folder, stops being probed and stored.
func TestGitCache_SweepsChildrenWithTheirParent(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{},
		children: map[string][]gitinfo.ChildRepo{
			"/w": {{Dir: "/w/a", GitDir: "/w/a/.git", CommonDir: "/w/a/.git"}},
		},
		infos: map[string]gitinfo.Info{"/w/a": {Branch: "main"}},
	}
	installFakeGit(t, f)

	c := newGitCache()
	ctx := context.Background()
	c.refresh(ctx, []string{"/w"})
	before := f.probeCount()

	c.refresh(ctx, nil) // the pane closed

	if got := f.probeCount(); got != before {
		t.Errorf("probes went %d→%d with no panes left, want no new work", before, got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byDir) != 0 || len(c.cwdChildren) != 0 {
		t.Errorf("byDir=%d cwdChildren=%d after the parent pane left, want both empty",
			len(c.byDir), len(c.cwdChildren))
	}
}
