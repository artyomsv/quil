package gitworktree

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stubGit installs a runGit that returns canned output for the test's duration.
// Every test here drives the parser, never a real repository — the same seam
// gitinfo uses, and for the same reason: a test that needs `git worktree add`
// to have run is a test that cannot describe a bare repo, a dead mount, or a
// prunable entry.
func stubGit(t *testing.T, out string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	prev := runGit
	runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		calls = append(calls, append([]string{dir}, args...))
		return out, err
	}
	t.Cleanup(func() { runGit = prev })
	return &calls
}

// The porcelain format is blank-line-separated blocks. The FIRST block is the
// repository's main checkout; every later one is a linked worktree. That
// distinction is the whole point of the call for this feature — the main
// checkout is where a new worktree's path is derived from, and it is the one
// entry a user must not be offered as somewhere to attach a second pane.
func TestList_ParsesPorcelainBlocks(t *testing.T) {
	stubGit(t, strings.Join([]string{
		"worktree /repo",
		"HEAD 1111111111111111111111111111111111111111",
		"branch refs/heads/master",
		"",
		"worktree /repo-worktrees/feat-x",
		"HEAD 2222222222222222222222222222222222222222",
		"branch refs/heads/feat/x",
		"",
		"worktree /repo-worktrees/detached",
		"HEAD 3333333333333333333333333333333333333333",
		"detached",
		"",
	}, "\n"), nil)

	got, err := List(context.Background(), "/repo/internal/tui")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Worktree{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/repo-worktrees/feat-x", Branch: "feat/x"},
		{Path: "/repo-worktrees/detached", Detached: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() =\n%+v\nwant\n%+v", got, want)
	}
}

// A branch is reported as a full ref. Rendering "refs/heads/feat/x" in a
// 22-column sidebar field wastes eleven cells on a constant prefix, and the
// value is also compared against a user-typed branch name, which never carries
// it.
func TestList_StripsTheRefsHeadsPrefix(t *testing.T) {
	stubGit(t, "worktree /repo\nHEAD 1111\nbranch refs/heads/feat/nested/name\n\n", nil)
	got, err := List(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Branch != "feat/nested/name" {
		t.Errorf("Branch = %+v, want feat/nested/name with the ref prefix stripped", got)
	}
}

// Locked and prunable entries must survive parsing rather than being dropped.
// Both are attachable-looking and NOT attachable: a prunable worktree's
// directory is gone, and a locked one refuses operations. Dropping them would
// present a shorter list with no explanation; keeping them lets the caller say
// why a row is unavailable.
func TestList_KeepsLockedAndPrunableEntries(t *testing.T) {
	stubGit(t, strings.Join([]string{
		"worktree /repo",
		"HEAD 1111",
		"branch refs/heads/master",
		"",
		"worktree /repo-worktrees/locked",
		"HEAD 2222",
		"branch refs/heads/locked-one",
		"locked on a removable drive",
		"",
		"worktree /repo-worktrees/gone",
		"HEAD 3333",
		"branch refs/heads/gone-one",
		"prunable gitdir file points to non-existent location",
		"",
	}, "\n"), nil)

	got, err := List(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3 — locked/prunable must not be dropped", len(got))
	}
	if !got[1].Locked {
		t.Error("entry 1 should be Locked")
	}
	if !got[2].Prunable {
		t.Error("entry 2 should be Prunable")
	}
}

// A bare repository has a main "worktree" with no working tree at all. It
// cannot host a pane, and `git worktree add` from one behaves differently
// enough that the caller has to know. Marked, not dropped: dropping it would
// silently renumber the list and make the FIRST linked worktree look like the
// main checkout.
func TestList_MarksABareMainCheckout(t *testing.T) {
	stubGit(t, "worktree /repo.git\nbare\n\nworktree /wt\nHEAD 2222\nbranch refs/heads/x\n\n", nil)
	got, err := List(context.Background(), "/repo.git")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if !got[0].Bare || !got[0].Main {
		t.Errorf("entry 0 = %+v, want Bare and Main", got[0])
	}
	if got[1].Bare {
		t.Error("a linked worktree is never bare")
	}
}

// Not being in a repository is an ORDINARY answer, not an error worth
// surfacing: the setup dialog asks about every directory the user browses to,
// and most of them are not repositories.
func TestList_OutsideARepositoryReturnsNoEntriesAndNoError(t *testing.T) {
	stubGit(t, "", errors.New("exit status 128"))
	got, err := List(context.Background(), "/tmp")
	if err != nil {
		t.Errorf("List outside a repository should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want none", len(got))
	}
}

// List's "not a repository" collapse is deliberately narrow: it applies only
// to a plain non-zero exit, which is the shape that answer actually takes. A
// missing git binary and a call that ran out of time both mean "the answer is
// unknown", not "there is no repository here" — folding either into the same
// silent (nil, nil) would render a confidently wrong "not a git repository"
// for a directory that IS one, just unreachable right now.
func TestList_ClassifiesGitFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		ctx     func() context.Context
		wantErr bool
	}{
		{
			name:    "plain exit error is not a repository, not an error",
			err:     errors.New("exit status 128"),
			ctx:     context.Background,
			wantErr: false,
		},
		{
			name:    "missing git binary is an error",
			err:     exec.ErrNotFound,
			ctx:     context.Background,
			wantErr: true,
		},
		{
			name: "context deadline exceeded is an error",
			err:  errors.New("signal: killed"),
			ctx: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				cancel()
				return ctx
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubGit(t, "", tt.err)
			_, err := List(tt.ctx(), "/repo")
			if (err != nil) != tt.wantErr {
				t.Errorf("List() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Add must never pass --force. A refusal from git is a fact to report — the
// path is occupied, the branch exists, the repo has no commits — and forcing
// past it is how a pane lands on top of another pane's checkout.
func TestAdd_NeverForces(t *testing.T) {
	calls := stubGit(t, "", nil)
	if err := Add(context.Background(), "/repo", "/repo-worktrees/feat-x", "feat/x"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("got %d git invocations, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call[0] != "/repo" {
		t.Errorf("ran in %q, want the repository directory", call[0])
	}
	for _, arg := range call {
		if arg == "--force" || arg == "-f" {
			t.Fatalf("Add passed a force flag: %v", call)
		}
	}
	wantArgs := []string{"worktree", "add", "-b", "feat/x", "/repo-worktrees/feat-x"}
	if !reflect.DeepEqual(call[1:], wantArgs) {
		t.Errorf("args = %v, want %v", call[1:], wantArgs)
	}
}

// A failed add must surface as an error rather than a silent no-op: the caller
// spawns a pane on the strength of this return, and the whole point of the
// design is that a pane never lands somewhere other than where the user asked.
func TestAdd_PropagatesFailure(t *testing.T) {
	stubGit(t, "", errors.New("exit status 128"))
	if err := Add(context.Background(), "/repo", "/occupied", "feat/x"); err == nil {
		t.Fatal("Add should return an error when git fails")
	}
}
