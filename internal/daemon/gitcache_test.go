package daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/gitinfo"
)

// fakeGitProbes swaps both cache seams for canned answers keyed by directory,
// and records every call so the tests can assert on how MANY probes ran — the
// property the cache exists for.
type fakeGitProbes struct {
	mu sync.Mutex
	// dirs maps a CWD to the (gitDir, commonDir) pair rev-parse would report.
	// A missing entry means "not a repository".
	dirs map[string][2]string
	// infos maps a probe dir to its state. A missing entry means the probe
	// did not answer.
	infos map[string]gitinfo.Info

	dirCalls   []string
	probeCalls []string
}

func installFakeGit(t *testing.T, f *fakeGitProbes) {
	t.Helper()
	origDirs, origProbe := gitDirsFn, gitProbeFn
	t.Cleanup(func() { gitDirsFn, gitProbeFn = origDirs, origProbe })

	gitDirsFn = func(_ context.Context, dir string) (string, string, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.dirCalls = append(f.dirCalls, dir)
		pair, ok := f.dirs[dir]
		if !ok {
			return "", "", false
		}
		return pair[0], pair[1], true
	}
	gitProbeFn = func(_ context.Context, dir string) (gitinfo.Info, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.probeCalls = append(f.probeCalls, dir)
		info, ok := f.infos[dir]
		return info, ok
	}
}

func (f *fakeGitProbes) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.probeCalls)
}

// The reason the cache is keyed by checkout at all: a session with several
// panes open in one repository must cost ONE git invocation per refresh, not
// one per pane.
func TestGitCache_PanesInOneCheckoutShareAProbe(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{
			"/repo":          {"/repo/.git", "/repo/.git"},
			"/repo/internal": {"/repo/.git", "/repo/.git"},
			"/repo/docs":     {"/repo/.git", "/repo/.git"},
		},
		infos: map[string]gitinfo.Info{"/repo": {Branch: "main"}},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/repo", "/repo/internal", "/repo/docs"})

	if n := f.probeCount(); n != 1 {
		t.Errorf("%d probes for three panes in one checkout, want 1", n)
	}
	for _, cwd := range []string{"/repo", "/repo/internal", "/repo/docs"} {
		info, ok, _ := c.lookup(cwd)
		if !ok || info.Branch != "main" {
			t.Errorf("lookup(%q) = %+v ok=%v, want the shared checkout's branch", cwd, info, ok)
		}
	}
}

// Linked worktrees share a common dir and sit on different branches. Keying on
// the common dir — which the design spec originally called for — would report
// both as being on whichever branch was probed first.
func TestGitCache_WorktreesGetTheirOwnBranch(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{
			"/repo": {"/repo/.git", "/repo/.git"},
			"/wt1":  {"/repo/.git/worktrees/wt1", "/repo/.git"},
		},
		infos: map[string]gitinfo.Info{
			"/repo": {Branch: "master"},
			"/wt1":  {Branch: "feature", LinkedWorktree: true},
		},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/repo", "/wt1"})

	main, _, _ := c.lookup("/repo")
	wt, _, _ := c.lookup("/wt1")
	if main.Branch != "master" || wt.Branch != "feature" {
		t.Errorf("branches = %q/%q, want master/feature — a common-dir key collapses worktrees",
			main.Branch, wt.Branch)
	}
	if !wt.LinkedWorktree {
		t.Error("the worktree checkout must be flagged as linked")
	}
}

// A plain shell pane is not in a repository. Re-resolving it every tick costs
// exactly as much as probing a real one, so the negative answer is cached.
func TestGitCache_NonRepositoryIsNotReprobed(t *testing.T) {
	f := &fakeGitProbes{dirs: map[string][2]string{}}
	installFakeGit(t, f)

	c := newGitCache()
	for i := 0; i < 3; i++ {
		c.refresh(context.Background(), []string{"/tmp"})
	}

	f.mu.Lock()
	resolves := len(f.dirCalls)
	f.mu.Unlock()
	if resolves != 1 {
		t.Errorf("%d resolutions of a non-repository across three refreshes, want 1", resolves)
	}
	if _, ok, _ := c.lookup("/tmp"); ok {
		t.Error("a non-repository must not report git state")
	}
}

// A probe that does not answer keeps the last value and says so. Dropping it
// would blank a branch the user can still read from their own shell; replacing
// it with a guess would be worse.
func TestGitCache_TimeoutKeepsTheLastValueAndMarksItStale(t *testing.T) {
	f := &fakeGitProbes{
		dirs:  map[string][2]string{"/repo": {"/repo/.git", "/repo/.git"}},
		infos: map[string]gitinfo.Info{"/repo": {Branch: "main"}},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/repo"})
	if _, _, stale := c.lookup("/repo"); stale {
		t.Fatal("a successful probe must not be stale")
	}

	// The repository stops answering.
	f.mu.Lock()
	delete(f.infos, "/repo")
	f.mu.Unlock()
	c.refresh(context.Background(), []string{"/repo"})

	info, ok, stale := c.lookup("/repo")
	if !ok {
		t.Fatal("a timed-out probe must not drop the checkout")
	}
	if info.Branch != "main" {
		t.Errorf("Branch = %q, want the last observed value", info.Branch)
	}
	if !stale {
		t.Error("a timed-out probe must mark the entry stale, or an old branch reads as current")
	}
}

// A checkout whose last pane closed stops being probed — otherwise a long
// session accumulates work for repositories nobody is looking at.
func TestGitCache_UnreferencedCheckoutStopsBeingProbed(t *testing.T) {
	f := &fakeGitProbes{
		dirs:  map[string][2]string{"/repo": {"/repo/.git", "/repo/.git"}},
		infos: map[string]gitinfo.Info{"/repo": {Branch: "main"}},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/repo"})
	before := f.probeCount()

	c.refresh(context.Background(), nil) // the pane closed
	if got := f.probeCount(); got != before {
		t.Errorf("probes went %d→%d with no panes left, want no new work", before, got)
	}
}

// The broadcast path calls lookup on every pane of every state update, so it
// must never reach the filesystem.
func TestGitCache_LookupNeverProbes(t *testing.T) {
	f := &fakeGitProbes{dirs: map[string][2]string{"/repo": {"/repo/.git", "/repo/.git"}}}
	installFakeGit(t, f)

	c := newGitCache()
	for i := 0; i < 5; i++ {
		c.lookup("/repo")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dirCalls) != 0 || len(f.probeCalls) != 0 {
		t.Errorf("lookup touched git: %d resolutions, %d probes — the broadcast path must not block",
			len(f.dirCalls), len(f.probeCalls))
	}
}

// TestGitCache_SweepsCWDsNoPaneIsInAnyMore: referencedDirs bounds what gets
// PROBED, not what is stored, so all three maps only ever grew. OSC 7 rewrites
// a pane's CWD on every cd, so one shell roaming a monorepo adds an entry per
// directory it visits — and this daemon is expected to run for weeks.
func TestGitCache_SweepsCWDsNoPaneIsInAnyMore(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{
			"/repo/a": {"/repo/a/.git", "/repo/a/.git"},
			"/repo/b": {"/repo/b/.git", "/repo/b/.git"},
		},
		infos: map[string]gitinfo.Info{
			"/repo/a": {Branch: "main"},
			"/repo/b": {Branch: "dev"},
		},
	}
	installFakeGit(t, f)

	c := newGitCache()
	ctx := context.Background()

	// Two panes, two checkouts.
	c.refresh(ctx, []string{"/repo/a", "/repo/b"})
	if got := len(c.cwdToDir); got != 2 {
		t.Fatalf("cwdToDir = %d entries after resolving two CWDs, want 2", got)
	}

	// The second pane closes, or its shell cds away. Only /repo/a is live.
	c.refresh(ctx, []string{"/repo/a"})

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, still := c.cwdToDir["/repo/b"]; still {
		t.Error("cwdToDir still holds /repo/b, which no pane is in — the map " +
			"grows for the life of the daemon, one entry per directory any " +
			"shell has ever cd'd into")
	}
	if _, still := c.cwdAt["/repo/b"]; still {
		t.Error("cwdAt still holds /repo/b")
	}
	if _, still := c.cwdToDir["/repo/a"]; !still {
		t.Error("the sweep dropped /repo/a, which a pane IS in")
	}
	if len(c.byDir) != 1 {
		t.Errorf("byDir = %d checkouts, want 1 — a checkout whose last pane is "+
			"gone is kept forever", len(c.byDir))
	}
}

// The PLACEHOLDER entry — what the cache holds between resolving a CWD and the
// first probe answering — must name the worktree too. Without it the sidebar
// shows a nameless worktree for one tick on every new pane, which is precisely
// the tick the user is looking at just after creating one.
func TestGitCache_PlaceholderNamesTheWorktree(t *testing.T) {
	f := &fakeGitProbes{
		dirs: map[string][2]string{
			"/wt/feat-x": {"/repo/.git/worktrees/feat-x", "/repo/.git"},
		},
		// No infos entry: the probe never answers, so the placeholder stands.
		infos: map[string]gitinfo.Info{},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/wt/feat-x"})

	info, ok, _ := c.lookup("/wt/feat-x")
	if !ok {
		t.Fatal("no cache entry for the worktree cwd")
	}
	if !info.LinkedWorktree {
		t.Fatal("the placeholder did not flag a linked worktree")
	}
	if info.WorktreeName != "feat-x" {
		t.Errorf("WorktreeName = %q, want \"feat-x\"", info.WorktreeName)
	}
}

// The main checkout's placeholder names nothing, so an ordinary pane never
// carries a directory name it did not earn.
func TestGitCache_PlaceholderNamesNothingForTheMainCheckout(t *testing.T) {
	f := &fakeGitProbes{
		dirs:  map[string][2]string{"/repo": {"/repo/.git", "/repo/.git"}},
		infos: map[string]gitinfo.Info{},
	}
	installFakeGit(t, f)

	c := newGitCache()
	c.refresh(context.Background(), []string{"/repo"})

	info, _, _ := c.lookup("/repo")
	if info.WorktreeName != "" {
		t.Errorf("WorktreeName = %q for the main checkout, want empty", info.WorktreeName)
	}
}
