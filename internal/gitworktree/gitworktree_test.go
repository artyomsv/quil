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
	if _, err := Add(context.Background(), "/repo", "/repo-worktrees/feat-x", "feat/x"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	call := addCall(t, *calls)
	if call[0] != "/repo" {
		t.Errorf("ran in %q, want the repository directory", call[0])
	}
	for _, arg := range call {
		if arg == "--force" || arg == "-f" {
			t.Fatalf("Add passed a force flag: %v", call)
		}
	}
	// No start-point: the stub answers every resolution probe with empty
	// output, which is what a repository with no default branch looks like,
	// and git's own HEAD default is the right answer there.
	wantArgs := []string{"worktree", "add", "-b", "feat/x", "/repo-worktrees/feat-x"}
	if !reflect.DeepEqual(call[1:], wantArgs) {
		t.Errorf("args = %v, want %v", call[1:], wantArgs)
	}
}

// addCall picks the `worktree add` invocation out of the calls a single Add
// makes. Add resolves the base branch first, so the write is no longer the only
// git command it runs and an index into the slice would pin the number of
// resolution probes — a detail no caller depends on.
func addCall(t *testing.T, calls [][]string) []string {
	t.Helper()
	for _, c := range calls {
		if len(c) >= 3 && c[1] == "worktree" && c[2] == "add" {
			return c
		}
	}
	t.Fatalf("no `git worktree add` among %v", calls)
	return nil
}

// stubGitScript answers each invocation from fn, which sees the args and
// returns git's output and error. The canned-response stubGit cannot express
// the resolution order — every probe would answer identically, so a test could
// not tell "asked origin/HEAD first" from "asked everything and took the last".
func stubGitScript(t *testing.T, fn func(args []string) (string, error)) *[][]string {
	t.Helper()
	var calls [][]string
	prev := runGit
	runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		calls = append(calls, append([]string{dir}, args...))
		return fn(args)
	}
	t.Cleanup(func() { runGit = prev })
	return &calls
}

// origin/HEAD is the repository's own recorded answer to "what is the default
// branch", so it outranks every convention — a repo whose default is `develop`
// must not be branched off a `master` that happens to exist beside it.
func TestDefaultBranch_PrefersOriginHEAD(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "refs/remotes/origin/develop\n", nil
		}
		if args[len(args)-1] == "refs/remotes/origin/develop^{commit}" {
			return "cafebabe\n", nil
		}
		return "", errors.New("no conventional candidate should be probed")
	})
	if got := defaultBranch(context.Background(), "/repo"); got != "refs/remotes/origin/develop" {
		t.Errorf("defaultBranch = %q, want %q", got, "refs/remotes/origin/develop")
	}
	// Two calls: read the symref, then verify it. Anything more means the
	// conventional loop ran anyway and the answer happened to come out right.
	if len(*calls) != 2 {
		t.Errorf("origin/HEAD answered, but %d git calls ran: %v", len(*calls), *calls)
	}
}

// A DANGLING origin/HEAD must fall through, not be adopted. `git symbolic-ref`
// reads the symref without resolving its target, so it exits 0 naming a ref
// that no longer exists — the ordinary state of a clone whose remote renamed
// its default branch. Adopting it hands `worktree add` a reference git refuses,
// which fails the whole create for a branch the user never typed.
func TestDefaultBranch_FallsThroughWhenOriginHEADDoesNotResolve(t *testing.T) {
	stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "refs/remotes/origin/gone\n", nil
		}
		if args[len(args)-1] == "refs/remotes/origin/master^{commit}" {
			return "cafebabe\n", nil
		}
		return "", errors.New("exit status 128")
	})
	if got := defaultBranch(context.Background(), "/repo"); got != "refs/remotes/origin/master" {
		t.Errorf("defaultBranch = %q, want the fallback %q", got, "refs/remotes/origin/master")
	}
}

// symbolic-ref answering empty-with-no-error must also fall through. Every
// other test here has it return an ERROR, so without this case a mutation that
// drops the emptiness check and returns the trimmed value unconditionally still
// passes — it would return "" where a real repository has an answer.
func TestDefaultBranch_FallsThroughWhenOriginHEADIsEmpty(t *testing.T) {
	stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "", nil
		}
		if args[len(args)-1] == "refs/heads/master^{commit}" {
			return "cafebabe\n", nil
		}
		return "", errors.New("exit status 128")
	})
	if got := defaultBranch(context.Background(), "/repo"); got != "refs/heads/master" {
		t.Errorf("defaultBranch = %q, want %q", got, "refs/heads/master")
	}
}

// Every probe must be FULLY QUALIFIED. `rev-parse` applies ref_rev_parse_rules,
// which tries refs/heads/%s before refs/remotes/%s — so a short "origin/main"
// resolves a local branch literally named `refs/heads/origin/main` in
// preference to the remote-tracking ref, inverting the documented ordering.
// Asserted as a property over the argv rather than by listing the candidates,
// so adding one cannot quietly reintroduce a short name.
func TestDefaultBranch_ProbesOnlyFullyQualifiedRefs(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		return "", errors.New("exit status 128")
	})
	defaultBranch(context.Background(), "/repo")
	for _, c := range *calls {
		last := c[len(c)-1]
		switch c[1] {
		case "symbolic-ref":
			if !strings.HasPrefix(last, "refs/") {
				t.Errorf("symbolic-ref asked for %q, want a fully-qualified ref", last)
			}
		case "rev-parse":
			if !strings.HasPrefix(last, "refs/") {
				t.Errorf("rev-parse probed %q, want a fully-qualified ref", last)
			}
		}
	}
}

// The one case that actually decides main-vs-master. The table below never has
// both present at once, so reversing the whole candidate order passes it.
func TestDefaultBranch_PrefersMainOverMasterWhenBothExist(t *testing.T) {
	stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "", errors.New("exit status 1")
		}
		switch args[len(args)-1] {
		case "refs/remotes/origin/main^{commit}", "refs/remotes/origin/master^{commit}":
			return "cafebabe\n", nil
		}
		return "", errors.New("exit status 128")
	})
	if got := defaultBranch(context.Background(), "/repo"); got != "refs/remotes/origin/main" {
		t.Errorf("defaultBranch = %q, want %q", got, "refs/remotes/origin/main")
	}
}

// Most clones never get origin/HEAD set — git only writes it on clone, and a
// repository created with `git init` plus a remote has none — so the
// conventional names are the common path, not an exotic fallback.
func TestDefaultBranch_FallsBackToTheFirstConventionalRefThatExists(t *testing.T) {
	tests := []struct {
		name  string
		exist map[string]bool
		want  string
	}{
		{"remote master", map[string]bool{"refs/remotes/origin/master^{commit}": true}, "refs/remotes/origin/master"},
		{"remote main", map[string]bool{"refs/remotes/origin/main^{commit}": true}, "refs/remotes/origin/main"},
		{
			// A remote ref is the shared truth; a local branch of the same name
			// can be behind it, and branching a fix off a stale local master is
			// the near-miss version of the bug this resolution exists to fix.
			"remote wins over local",
			map[string]bool{"refs/remotes/origin/master^{commit}": true, "refs/heads/master^{commit}": true},
			"refs/remotes/origin/master",
		},
		{"local only", map[string]bool{"refs/heads/master^{commit}": true}, "refs/heads/master"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubGitScript(t, func(args []string) (string, error) {
				if args[0] == "symbolic-ref" {
					return "", errors.New("exit status 1")
				}
				ref := args[len(args)-1]
				if tt.exist[ref] {
					return "cafebabe\n", nil
				}
				return "", errors.New("exit status 128")
			})
			if got := defaultBranch(context.Background(), "/repo"); got != tt.want {
				t.Errorf("defaultBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

// An empty answer must NOT count as a resolved ref. `rev-parse --verify
// --quiet` prints the sha on success, so empty-with-no-error is a stub, a
// wrapper or a future git speaking a shape this code did not expect — and
// adopting it puts an empty string into argv where a commit-ish belongs, which
// git reads as the path argument.
func TestDefaultBranch_TreatsEmptyOutputAsUnresolved(t *testing.T) {
	stubGitScript(t, func(args []string) (string, error) { return "", nil })
	if got := defaultBranch(context.Background(), "/repo"); got != "" {
		t.Errorf("defaultBranch = %q, want %q", got, "")
	}
}

// A repository with no default branch is a real repository — `git init -b
// trunk` makes one — so this must be a clean "no answer" the caller can fall
// back to HEAD on, not an error that refuses to create the worktree.
func TestDefaultBranch_ReturnsEmptyWhenNothingResolves(t *testing.T) {
	stubGitScript(t, func(args []string) (string, error) {
		return "", errors.New("exit status 128")
	})
	if got := defaultBranch(context.Background(), "/repo"); got != "" {
		t.Errorf("defaultBranch = %q, want %q", got, "")
	}
}

// The start-point is the whole fix: without it git branches from the HEAD of
// whatever the main checkout is on, which is ambient state the user did not
// choose in this dialog.
// --no-track is asserted with the start-point rather than in a test of its own,
// because the two are one decision: a remote-tracking start-point configures an
// upstream (branch.autoSetupMerge defaults to true), and then `git push` in the
// new worktree fails with a remedy that pushes the work onto the default
// branch. Branching off HEAD set no upstream, so passing the base without
// --no-track changes behaviour the base fix never intended to touch.
func TestAdd_PassesTheDefaultBranchAsStartPointWithoutTracking(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "refs/remotes/origin/master\n", nil
		}
		return "cafebabe\n", nil
	})
	if _, err := Add(context.Background(), "/repo", "/repo-worktrees/feat-x", "feat/x"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	wantArgs := []string{"worktree", "add", "--no-track", "-b", "feat/x", "/repo-worktrees/feat-x", "refs/remotes/origin/master"}
	if got := addCall(t, *calls)[1:]; !reflect.DeepEqual(got, wantArgs) {
		t.Errorf("args = %v, want %v", got, wantArgs)
	}
}

// A resolved value that could be read as a flag must never reach argv, and git
// does NOT prevent this on its own: `git update-ref refs/heads/-evil HEAD`
// succeeds, and git permutes option parsing past positional arguments, so a
// dash-prefixed start-point is parsed as an option — `--force` as a trailing
// positional is accepted as the flag (both measured, git 2.53). Fully-qualified
// candidates cannot begin with a dash, so today this only fires on a symref
// pointing outside refs/, but the guard has to outlive that.
func TestAdd_RefusesAStartPointThatLooksLikeAFlag(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "--upstream\n", nil
		}
		return "", nil
	})
	if _, err := Add(context.Background(), "/repo", "/repo-worktrees/feat-x", "feat/x"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, arg := range addCall(t, *calls) {
		if strings.HasPrefix(arg, "--upstream") {
			t.Fatalf("a dash-prefixed start-point reached argv: %v", *calls)
		}
	}
	wantArgs := []string{"worktree", "add", "-b", "feat/x", "/repo-worktrees/feat-x"}
	if got := addCall(t, *calls)[1:]; !reflect.DeepEqual(got, wantArgs) {
		t.Errorf("args = %v, want %v", got, wantArgs)
	}
}

// A failed add must surface as an error rather than a silent no-op: the caller
// spawns a pane on the strength of this return, and the whole point of the
// design is that a pane never lands somewhere other than where the user asked.
func TestAdd_PropagatesFailure(t *testing.T) {
	stubGit(t, "", errors.New("exit status 128"))
	if _, err := Add(context.Background(), "/repo", "/occupied", "feat/x"); err == nil {
		t.Fatal("Add should return an error when git fails")
	}
}

// The path reaches git in OPTION position, and it is a value this daemon does
// not fully control: a pane's recorded worktree path comes from Quil, but the
// same helper is reachable with any path a caller holds. `--` terminates option
// parsing so a value that begins with a dash can never be read as a flag.
//
// Current git happens to reject `--force --x` on its own, so this is hardening
// rather than a live hole — the same reasoning that keeps usableStartPoint's
// dash guard around a value that cannot begin with one today.
func TestRemoveWorktree_TerminatesOptionsBeforeThePath(t *testing.T) {
	calls := stubGit(t, "", nil)

	if err := RemoveWorktree(context.Background(), "/repo", "/w/feat-a"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("git calls = %v, want one", *calls)
	}
	args := (*calls)[0]
	last := args[len(args)-1]
	if last != "/w/feat-a" {
		t.Fatalf("last arg = %q, want the path", last)
	}
	if before := args[len(args)-2]; before != "--" {
		t.Errorf("argv = %v; want %q immediately before the path", args, "--")
	}
}

// The status call must not take the index lock: it runs against a worktree the
// user may be working in at that moment, and a plain `git status` refreshes and
// REWRITES the index, contending with their own git. Quil is asking a question
// here, not doing work on their behalf.
func TestStatus_DoesNotTakeOptionalLocks(t *testing.T) {
	calls := stubGit(t, "", nil)

	if _, err := Status(context.Background(), "/w/feat-a"); err != nil {
		t.Fatalf("Status: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("git calls = %v, want one", *calls)
	}
	args := (*calls)[0]
	var sawNoOptionalLocks, sawIgnored bool
	for _, a := range args {
		switch a {
		case "--no-optional-locks":
			sawNoOptionalLocks = true
		case "--ignored":
			sawIgnored = true
		}
	}
	if !sawNoOptionalLocks {
		t.Errorf("argv = %v, want --no-optional-locks", args)
	}
	if !sawIgnored {
		t.Errorf("argv = %v, want --ignored — an ignored .env is destroyed by the removal and must be counted", args)
	}
}
