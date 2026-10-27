package gitinfo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeGitByDir answers the rev-parse call PER DIRECTORY, which fakeGit cannot:
// the child walk runs the same argv in several directories and the answer is
// what tells a repository from a plain folder. Any other argv fails, and every
// directory asked is recorded so a test can assert what was NOT examined.
func fakeGitByDir(t *testing.T, repos map[string]string) *[]string {
	t.Helper()
	var asked []string
	orig := runGit
	t.Cleanup(func() { runGit = orig })
	runGit = func(_ context.Context, dir string, args ...string) (string, error) {
		asked = append(asked, dir)
		if strings.Join(args, " ") != argCommonDir {
			return "", errors.New("unexpected argv")
		}
		out, ok := repos[dir]
		if !ok {
			return "", errors.New("not a repository")
		}
		return out, nil
	}
	return &asked
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
	}
}

func TestListChildRepos_OneLevelVisibleDirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "beta", "alpha", "plain", ".hidden", "alpha/nested")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	answer := func(name string) string {
		return filepath.Join(root, name, ".git") + "\n" + filepath.Join(root, name, ".git") + "\n"
	}
	asked := fakeGitByDir(t, map[string]string{
		filepath.Join(root, "alpha"):           answer("alpha"),
		filepath.Join(root, "beta"):            answer("beta"),
		filepath.Join(root, ".hidden"):         answer(".hidden"),
		filepath.Join(root, "link"):            answer("link"),
		filepath.Join(root, "alpha", "nested"): answer("alpha/nested"),
	})

	got := ListChildRepos(context.Background(), root)

	want := []ChildRepo{
		{Dir: filepath.Join(root, "alpha"), GitDir: filepath.Join(root, "alpha", ".git"), CommonDir: filepath.Join(root, "alpha", ".git")},
		{Dir: filepath.Join(root, "beta"), GitDir: filepath.Join(root, "beta", ".git"), CommonDir: filepath.Join(root, "beta", ".git")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListChildRepos = %+v\nwant %+v", got, want)
	}
	for _, dir := range *asked {
		switch filepath.Base(dir) {
		case ".hidden", "link", "nested", "file.txt":
			t.Errorf("git ran in %s — hidden entries, links, files and grandchildren must not be examined", dir)
		}
	}
	// "plain" IS examined (it is a visible directory) and simply is not a
	// repository; the only way to know is to ask.
	if !containsDir(*asked, filepath.Join(root, "plain")) {
		t.Error("a visible non-repository child was never examined, so it cannot have been ruled out")
	}
}

func containsDir(asked []string, dir string) bool {
	for _, a := range asked {
		if a == dir {
			return true
		}
	}
	return false
}

// A folder with more visible subdirectories than the cap is NOT walked, not
// walked partially: a prefix of ~/Projects reported as "N repos" is a wrong
// answer with the confidence of a right one, and the git launches it costs
// recur on every negative-TTL re-resolution.
func TestListChildRepos_RefusesAFolderPastTheCap(t *testing.T) {
	root := t.TempDir()
	names := make([]string, 0, MaxChildDirs+1)
	for i := 0; i <= MaxChildDirs; i++ {
		names = append(names, fmt.Sprintf("repo-%02d", i))
	}
	mkdirs(t, root, names...)
	asked := fakeGitByDir(t, nil)

	if got := ListChildRepos(context.Background(), root); got != nil {
		t.Errorf("ListChildRepos = %d entries past the cap, want nil", len(got))
	}
	if len(*asked) != 0 {
		t.Errorf("git ran %d times under a folder past the cap, want 0", len(*asked))
	}
}

// Exactly the cap is still walked — the constant is a ceiling, not a strict
// bound, or a folder of sixteen worktrees would be the one size refused.
func TestListChildRepos_WalksExactlyTheCap(t *testing.T) {
	root := t.TempDir()
	repos := map[string]string{}
	for i := 0; i < MaxChildDirs; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		mkdirs(t, root, name)
		repos[filepath.Join(root, name)] = ".git\n.git\n"
	}
	fakeGitByDir(t, repos)

	if got := ListChildRepos(context.Background(), root); len(got) != MaxChildDirs {
		t.Errorf("ListChildRepos = %d entries, want %d", len(got), MaxChildDirs)
	}
}

// The walk shares one deadline with everything else the refresh runs under.
// Once it has passed, no child is asked and nothing partial is returned.
func TestListChildRepos_ExpiredContextAnswersNothing(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	asked := fakeGitByDir(t, map[string]string{
		filepath.Join(root, "a"): ".git\n.git\n",
		filepath.Join(root, "b"): ".git\n.git\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := ListChildRepos(ctx, root); got != nil {
		t.Errorf("ListChildRepos = %+v under an expired context, want nil", got)
	}
	if len(*asked) != 0 {
		t.Errorf("git ran %d times under an expired context, want 0", len(*asked))
	}
}

func TestListChildRepos_UnreadableOrEmptyFolder(t *testing.T) {
	fakeGitByDir(t, nil)
	if got := ListChildRepos(context.Background(), filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Errorf("a missing folder answered %+v, want nil", got)
	}
	if got := ListChildRepos(context.Background(), t.TempDir()); got != nil {
		t.Errorf("an empty folder answered %+v, want nil", got)
	}
}

func TestCombine(t *testing.T) {
	tests := []struct {
		name string
		in   []Info
		want Info
	}{
		{
			name: "no children is the zero Info",
			in:   nil,
			want: Info{},
		},
		{
			name: "one child is that child, counted",
			in:   []Info{{Branch: "main", HasUpstream: true, Ahead: 2}},
			want: Info{Branch: "main", HasUpstream: true, Ahead: 2, ChildRepos: 1},
		},
		{
			name: "the same branch everywhere is reported as that branch",
			in: []Info{
				{Branch: "ticket/x", LinkedWorktree: true, WorktreeName: "ticket-x", HasUpstream: true, Ahead: 1, Behind: 2},
				{Branch: "ticket/x", LinkedWorktree: true, WorktreeName: "ticket-x", HasUpstream: true, Ahead: 3},
			},
			want: Info{Branch: "ticket/x", LinkedWorktree: true, WorktreeName: "ticket-x", HasUpstream: true, Ahead: 4, Behind: 2, ChildRepos: 2},
		},
		{
			name: "different branches leave only the count",
			in: []Info{
				{Branch: "main", HasUpstream: true, Ahead: 1},
				{Branch: "dev"},
			},
			want: Info{HasUpstream: true, Ahead: 1, ChildRepos: 2},
		},
		{
			name: "a detached child beside a branch is a disagreement",
			in:   []Info{{Branch: "main"}, {Detached: true}},
			want: Info{ChildRepos: 2},
		},
		{
			name: "all detached is detached",
			in:   []Info{{Detached: true}, {Detached: true}},
			want: Info{Detached: true, ChildRepos: 2},
		},
		{
			name: "a child whose probe has not landed still counts and still disagrees",
			in:   []Info{{Branch: "main"}, {}},
			want: Info{ChildRepos: 2},
		},
		{
			name: "worktree names that differ clear the name but keep the flag",
			in: []Info{
				{Branch: "x", LinkedWorktree: true, WorktreeName: "a"},
				{Branch: "x", LinkedWorktree: true, WorktreeName: "b"},
			},
			want: Info{Branch: "x", LinkedWorktree: true, ChildRepos: 2},
		},
		{
			name: "a main checkout beside a worktree is not a worktree",
			in: []Info{
				{Branch: "x", LinkedWorktree: true, WorktreeName: "x"},
				{Branch: "x"},
			},
			want: Info{Branch: "x", ChildRepos: 2},
		},
		{
			name: "no upstream anywhere stays unmeasured, not zero",
			in:   []Info{{Branch: "x"}, {Branch: "x"}},
			want: Info{Branch: "x", ChildRepos: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Combine(tc.in); got != tc.want {
				t.Errorf("Combine = %+v\nwant      %+v", got, tc.want)
			}
		})
	}
}

// The combined answer must be worth a row: a folder of repositories on
// different branches has nothing in Branch, and Empty must still say no.
func TestInfo_Empty_CountsChildRepos(t *testing.T) {
	if (Info{ChildRepos: 2}).Empty() {
		t.Error("a folder summarising two repositories is worth rendering")
	}
}

// Real git, end to end: two repositories under a folder that is not one. The
// stub tests agree on argv; only git itself can confirm that `rev-parse` in a
// child answers and in the parent does not.
func TestListChildRepos_RealGit_SiblingsOnOneBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	for _, name := range []string{"api", "web"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
		run("init", "-q", "-b", "ticket/one")
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A commit, because --abbrev-ref HEAD has no answer on an unborn
		// branch and the point here is the branch.
		run("add", "f.txt")
		run("commit", "-q", "-m", "init")
	}
	ctx := context.Background()

	if _, _, ok := Dirs(ctx, root); ok {
		t.Fatal("the parent folder must not resolve as a repository, or the walk is never reached")
	}
	children := ListChildRepos(ctx, root)
	if len(children) != 2 {
		t.Fatalf("ListChildRepos = %+v, want the two sibling repositories", children)
	}
	infos := make([]Info, 0, len(children))
	for _, c := range children {
		info, ok := Probe(ctx, c.Dir)
		if !ok {
			t.Fatalf("Probe(%s) reported not-a-repository", c.Dir)
		}
		infos = append(infos, info)
	}
	got := Combine(infos)
	if got.Branch != "ticket/one" || got.ChildRepos != 2 {
		t.Errorf("Combine = %+v, want branch ticket/one over 2 repos", got)
	}
}

// A deadline that runs out INSIDE the last child's probe has no next
// iteration to notice it: the answer is still the whole list or nothing.
func TestListChildRepos_DeadlineSpentInTheLastProbeAnswersNothing(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orig := runGit
	t.Cleanup(func() { runGit = orig })
	runGit = func(_ context.Context, dir string, _ ...string) (string, error) {
		if filepath.Base(dir) == "b" {
			cancel() // the shared deadline passes while b is being probed
			return "", errors.New("signal: killed")
		}
		return ".git\n.git\n", nil
	}
	if got := ListChildRepos(ctx, root); got != nil {
		t.Errorf("ListChildRepos = %+v after the deadline passed in the last probe, want nil", got)
	}
}
