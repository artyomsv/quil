package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skipPreflight lets a test that scripts ONE git call (the status itself)
// run Status without the visibility checks in front of it.
func skipPreflight(t *testing.T) {
	t.Helper()
	prev := statusPreflight
	statusPreflight = func(context.Context, string) error { return nil }
	t.Cleanup(func() { statusPreflight = prev })
}

// statusRepo is a real repository with one linked worktree, feat/s.
func statusRepo(t *testing.T) (repo, path string, ctx context.Context) {
	t.Helper()
	repo = realGitRepo(t)
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	path = DerivePath(repo, "feat/s")
	if _, err := Add(c, repo, path, "feat/s"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return repo, path, c
}

// An edit to a file git is told NOT to look at (assume-unchanged, also what
// core.ignoreStat sets; skip-worktree) counts as nothing in git status. The
// safety status refuses instead of reporting the checkout clean.
//
// Fails if Status trusts a status that cannot see every file.
func TestStatus_RealGit_RefusesFilesGitIsToldToSkip(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			_, path, ctx := statusRepo(t)
			if err := os.WriteFile(filepath.Join(path, "f.txt"), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitIn(t, path, "update-index", flag, "f.txt")

			if _, err := Status(ctx, path); !errors.Is(err, ErrHiddenState) {
				t.Errorf("err = %v, want ErrHiddenState", err)
			}
		})
	}
}

// A populated submodule is another repository: its edits and ignored files
// are filtered by its own settings. Refused rather than reported clean.
func TestStatus_RealGit_RefusesAPopulatedSubmodule(t *testing.T) {
	sub := realGitRepo(t)
	repo := realGitRepo(t)
	runGitIn(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	runGitIn(t, repo, "commit", "-q", "-m", "add submodule")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/sub")
	if _, err := Add(ctx, repo, path, "feat/sub"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	runGitIn(t, path, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")

	if _, err := Status(ctx, path); !errors.Is(err, ErrHiddenState) {
		t.Errorf("err = %v, want ErrHiddenState for a populated submodule", err)
	}
}

// A daemon started with GIT_WORK_TREE or GIT_INDEX_FILE set (a script, an
// IDE terminal) must still check the checkout it is about to remove: the
// selectors are stripped, so an ignored file in the linked worktree is
// counted.
//
// Fails if runGit inherits git's repository selectors.
func TestStatus_RealGit_IgnoresInheritedGitSelectors(t *testing.T) {
	repo, path, ctx := statusRepo(t)
	if err := os.WriteFile(filepath.Join(path, "note.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_WORK_TREE", repo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "other-index"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'status.showuntrackedfiles'='no'")

	st, err := Status(ctx, path)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Changes != 1 {
		t.Errorf("changes = %d, want 1: the check read another checkout or index", st.Changes)
	}
}

// The status itself turns fsmonitor off and never filters submodules.
func TestStatus_DisablesFsmonitorAndSubmoduleFiltering(t *testing.T) {
	skipPreflight(t)
	calls := stubGit(t, "", nil)
	if _, err := Status(context.Background(), "/w/feat-a"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	argv := strings.Join((*calls)[0][1:], " ")
	for _, want := range []string{"-c core.fsmonitor=false", "--ignore-submodules=none"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv = %q, want %q", argv, want)
		}
	}
}

// cleanGitEnv drops every selector and config injection, case-insensitively
// (Windows), and keeps the rest.
func TestCleanGitEnv_DropsSelectorsKeepsTheRest(t *testing.T) {
	got := cleanGitEnv([]string{
		"PATH=/bin", "HOME=/h", "GIT_DIR=/x", "git_work_tree=/y", "GIT_INDEX_FILE=/i",
		"GIT_COMMON_DIR=/c", "GIT_CONFIG_PARAMETERS=x", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=/z", "GIT_CONFIG_GLOBAL=/dev/null",
	})
	want := []string{"PATH=/bin", "HOME=/h", "GIT_CONFIG_GLOBAL=/dev/null"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("env = %v, want %v", got, want)
	}
}

// The index scan is STREAMED: a repository whose index listing is far past
// runGit's 1 MiB cap is still checked rather than refused.
//
// Fails if the scan is buffered whole.
func TestStatus_RealGit_LargeIndexIsStillChecked(t *testing.T) {
	_, path, ctx := statusRepo(t)
	// ~12k paths of ~100 bytes: well past 1 MiB of ls-files -v -s output.
	dir := filepath.Join(path, strings.Repeat("d", 60))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%06d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitIn(t, path, "add", ".")
	runGitIn(t, path, "commit", "-q", "-m", "many")

	if st, err := Status(ctx, path); err != nil || st.Changes != 0 {
		t.Errorf("status = %+v, %v; want a clean answer for a large index", st, err)
	}
}
