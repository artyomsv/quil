package gitinfo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGit swaps the command seam for a table of canned answers keyed by the
// joined argv, so these tests never need a repository on disk — and so a
// wrong ARGUMENT is a test failure rather than a silently different answer.
func fakeGit(t *testing.T, answers map[string]string, fails map[string]bool) *[]string {
	t.Helper()
	var calls []string
	orig := runGit
	t.Cleanup(func() { runGit = orig })
	runGit = func(_ context.Context, _ string, args ...string) (string, error) {
		key := strings.Join(args, " ")
		calls = append(calls, key)
		if fails[key] {
			return "", errors.New("git failed")
		}
		out, ok := answers[key]
		if !ok {
			t.Errorf("unexpected git invocation: git %s", key)
			return "", errors.New("unexpected")
		}
		return out, nil
	}
	return &calls
}

const (
	argCommonDir  = "rev-parse --git-dir --git-common-dir"
	argBranch     = "rev-parse --abbrev-ref HEAD"
	argDivergence = "rev-list --left-right --count @{u}...HEAD"
)

func TestProbe_BranchAndDivergence(t *testing.T) {
	fakeGit(t, map[string]string{
		argCommonDir: "/repo/.git\n/repo/.git\n",
		argBranch:    "feat/projects-sidebar\n",
		// left = @{u} = behind, right = HEAD = ahead.
		argDivergence: "3\t7\n",
	}, nil)

	got, ok := Probe(context.Background(), "/repo")
	if !ok {
		t.Fatal("Probe reported not-a-repository")
	}
	if got.Branch != "feat/projects-sidebar" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.LinkedWorktree {
		t.Error("a plain checkout must not report a linked worktree")
	}
	if !got.HasUpstream {
		t.Fatal("HasUpstream = false with a successful rev-list")
	}
	// The pair is the one thing here that is silently invertible.
	if got.Behind != 3 || got.Ahead != 7 {
		t.Errorf("behind/ahead = %d/%d, want 3/7 — left is @{u} (behind), right is HEAD (ahead)",
			got.Behind, got.Ahead)
	}
}

func TestProbe_LinkedWorktree(t *testing.T) {
	fakeGit(t, map[string]string{
		argCommonDir:  "/repo/.git/worktrees/wt1\n/repo/.git\n",
		argBranch:     "wt-branch\n",
		argDivergence: "0\t0\n",
	}, nil)

	got, ok := Probe(context.Background(), "/wt1")
	if !ok {
		t.Fatal("Probe reported not-a-repository")
	}
	if !got.LinkedWorktree {
		t.Error("git-dir differing from git-common-dir must report a linked worktree")
	}
}

// A detached HEAD has no branch to name and no @{u} to resolve, so the
// divergence command must not run at all — it would fail on every tick.
func TestProbe_DetachedHeadSkipsDivergence(t *testing.T) {
	calls := fakeGit(t, map[string]string{
		argCommonDir: "/repo/.git\n/repo/.git\n",
		argBranch:    "HEAD\n",
	}, nil)

	got, _ := Probe(context.Background(), "/repo")
	if !got.Detached {
		t.Error("literal \"HEAD\" from --abbrev-ref means detached")
	}
	if got.Branch != "" {
		t.Errorf("Branch = %q, want empty for a detached HEAD", got.Branch)
	}
	for _, c := range *calls {
		if c == argDivergence {
			t.Error("divergence was probed on a detached HEAD; @{u} cannot resolve there")
		}
	}
}

// No upstream is an ANSWER, not a failure: the branch exists and is rendered,
// only the counts are unmeasured. Reporting 0/0 would claim it is in sync.
func TestProbe_NoUpstreamIsNotZeroZero(t *testing.T) {
	fakeGit(t, map[string]string{
		argCommonDir: "/repo/.git\n/repo/.git\n",
		argBranch:    "local-only\n",
	}, map[string]bool{argDivergence: true})

	got, ok := Probe(context.Background(), "/repo")
	if !ok {
		t.Fatal("a branch without an upstream is still a repository")
	}
	if got.Branch != "local-only" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.HasUpstream {
		t.Error("HasUpstream must stay false when rev-list fails")
	}
}

func TestDirs_NotARepository(t *testing.T) {
	fakeGit(t, nil, map[string]bool{argCommonDir: true})

	if _, _, ok := Dirs(context.Background(), "/tmp"); ok {
		t.Error("a directory outside any repository must report ok=false")
	}
}

// Two linked worktrees of one repository share a common dir and sit on
// DIFFERENT branches — that is why anyone creates one. A cache keyed on the
// common dir would report both as being on whichever branch was probed first,
// so Dirs must expose the per-checkout dir for callers to key on.
func TestDirs_WorktreesAreDistinctKeys(t *testing.T) {
	fakeGit(t, map[string]string{
		argCommonDir: "/repo/.git/worktrees/wt1\n/repo/.git\n",
	}, nil)
	wtDir, common, ok := Dirs(context.Background(), "/wt1")
	if !ok {
		t.Fatal("ok = false")
	}
	if wtDir == common {
		t.Fatal("a linked worktree must not resolve to its common dir")
	}
	if filepath.Base(filepath.Dir(wtDir)) != "worktrees" {
		t.Errorf("per-checkout dir = %q, want the worktree's own git dir", wtDir)
	}
}

// git answers relatively when the command runs at the top level, so two panes
// in the same repository would otherwise cache under ".git" and an absolute
// path — the same repository probed twice.
func TestDirs_ResolvesRelativeAnswers(t *testing.T) {
	fakeGit(t, map[string]string{argCommonDir: ".git\n.git\n"}, nil)

	gitDir, common, ok := Dirs(context.Background(), filepath.FromSlash("/repo"))
	if !ok {
		t.Fatal("ok = false")
	}
	if gitDir != common {
		t.Error("identical answers must resolve to identical paths, or a plain checkout reads as a worktree")
	}
	if want := filepath.Join(filepath.FromSlash("/repo"), ".git"); common != want {
		t.Errorf("common dir = %q, want %q — a relative answer must be resolved against the CWD", common, want)
	}
}

func TestParseLeftRight(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		behind, ahead int
		ok            bool
	}{
		{"tab separated", "2\t5\n", 2, 5, true},
		{"space separated", "2 5", 2, 5, true},
		{"zero divergence", "0\t0", 0, 0, true},
		{"one field", "2", 0, 0, false},
		{"empty", "", 0, 0, false},
		{"non-numeric", "a\tb", 0, 0, false},
		{"negative", "-1\t2", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			behind, ahead, ok := parseLeftRight(tc.in)
			if ok != tc.ok || behind != tc.behind || ahead != tc.ahead {
				t.Errorf("parseLeftRight(%q) = %d,%d,%v want %d,%d,%v",
					tc.in, behind, ahead, ok, tc.behind, tc.ahead, tc.ok)
			}
		})
	}
}

func TestInfo_Empty(t *testing.T) {
	if !(Info{}).Empty() {
		t.Error("the zero Info must be empty")
	}
	if (Info{Branch: "main"}).Empty() {
		t.Error("a named branch is not empty")
	}
	if (Info{Detached: true}).Empty() {
		t.Error("a detached HEAD is worth rendering")
	}
	// Ahead/behind alone cannot make it non-empty: without a branch there is
	// nothing to hang the counts on.
	if !(Info{HasUpstream: true, Ahead: 3}).Empty() {
		t.Error("counts with no branch must not read as renderable")
	}
}

// git spells a linked checkout's git dir <common>/worktrees/<name>, so the
// worktree's NAME is already in hand at the line that decides LinkedWorktree.
// This is the entire reason the sidebar can name it without a command of its
// own — the three plumbing calls per checkout stay three.
func TestProbe_NamesTheLinkedWorktree(t *testing.T) {
	calls := fakeGit(t, map[string]string{
		argCommonDir:  "/repo/.git/worktrees/feat-x\n/repo/.git\n",
		argBranch:     "feat/x\n",
		argDivergence: "0\t0\n",
	}, nil)

	got, ok := Probe(context.Background(), "/wt/feat-x")
	if !ok {
		t.Fatal("Probe reported not-a-repository")
	}
	if got.WorktreeName != "feat-x" {
		t.Errorf("WorktreeName = %q, want \"feat-x\"", got.WorktreeName)
	}
	// The name must cost nothing: exactly the three plumbing calls this
	// package has always made.
	if len(*calls) != 3 {
		t.Errorf("git ran %d times (%v), want the same 3 calls as before", len(*calls), *calls)
	}
}

// The MAIN checkout has no worktree name. Reporting one would put a directory
// name on every ordinary pane's row — noise on exactly the rows the feature is
// not about.
func TestProbe_MainCheckoutHasNoWorktreeName(t *testing.T) {
	fakeGit(t, map[string]string{
		argCommonDir:  "/repo/.git\n/repo/.git\n",
		argBranch:     "master\n",
		argDivergence: "0\t0\n",
	}, nil)

	got, ok := Probe(context.Background(), "/repo")
	if !ok {
		t.Fatal("Probe reported not-a-repository")
	}
	if got.WorktreeName != "" {
		t.Errorf("WorktreeName = %q for the main checkout, want empty", got.WorktreeName)
	}
}
