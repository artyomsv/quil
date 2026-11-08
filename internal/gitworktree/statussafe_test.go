package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
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

// A filesystem call that never answers (a dead network mount) cannot hold the
// check past its deadline: fsWithin returns the context's error, which every
// caller treats as "could not check".
func TestFsWithin_HungCallReturnsAtTheDeadline(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := fsWithin(ctx, func() (bool, error) { <-hang; return true, nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a call that never answered was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check hung on a filesystem call that never returns")
	}
}

// A worker holds its permit until its filesystem call REALLY returns: with
// the one permit held by a call stuck on a dead mount, the next check is
// refused at once (ErrFSBusy) rather than starting another stuck worker; the
// permit comes back when the stuck call ends.
//
// Fails if fsWithin's worker runs without a permit, or gives it back when the
// caller gives up.
func TestFsWithin_PermitHeldUntilTheCallEnds(t *testing.T) {
	var held atomic.Int32
	SetFSPermits(func() bool {
		if held.Add(1) > 1 {
			held.Add(-1)
			return false
		}
		return true
	}, func() { held.Add(-1) })
	t.Cleanup(func() { fsPermits.Store(nil) })

	hang := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := fsWithin(ctx, func() (bool, error) { <-hang; return true, nil }); err == nil {
		t.Fatal("a stuck call was accepted")
	}
	if _, err := fsWithin(context.Background(), func() (bool, error) { return true, nil }); !errors.Is(err, ErrFSBusy) {
		t.Errorf("err = %v, want ErrFSBusy while the stuck call holds the only permit", err)
	}
	close(hang)
	deadline := time.Now().Add(5 * time.Second)
	for held.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the permit was never returned after the stuck call ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := fsWithin(context.Background(), func() (bool, error) { return true, nil }); err != nil {
		t.Errorf("after the stuck call ended: %v", err)
	}
}

// A submodule that was never initialised leaves its folder in the checkout.
// git status never lists files put there, so the folder must be EMPTY: one
// with a file in it is refused, and the file is untouched.
//
// Fails if only a .git inside the folder counts as content.
func TestStatus_RealGit_UninitialisedSubmoduleFolderMustBeEmpty(t *testing.T) {
	sub := realGitRepo(t)
	repo := realGitRepo(t)
	runGitIn(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	runGitIn(t, repo, "commit", "-q", "-m", "add submodule")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/uninit")
	if _, err := Add(ctx, repo, path, "feat/uninit"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	folder := filepath.Join(path, "sub")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	if st, err := Status(ctx, path); err != nil || st.Changes != 0 {
		t.Fatalf("empty submodule folder: status = %+v, %v; want clean", st, err)
	}

	if err := os.WriteFile(filepath.Join(folder, "local.env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(ctx, path); !errors.Is(err, ErrHiddenState) {
		t.Errorf("err = %v, want ErrHiddenState for a file in an uninitialised submodule folder", err)
	}
	if got, err := os.ReadFile(filepath.Join(folder, "local.env")); err != nil || string(got) != "SECRET=1\n" {
		t.Errorf("the file = %q, %v; want it untouched", got, err)
	}
}

// The record reader is bounded WHILE reading and refuses anything that is not
// a complete list: an over-long record (memory never grows past the limit), a
// final record with no NUL, a read error. A refusal from fn stops the scan.
//
// Fails if the size is checked only after a record is fully buffered, or a
// truncated or failed stream is accepted as complete.
func TestScanNULRecords(t *testing.T) {
	collect := func(r io.Reader, max int) ([]string, error) {
		var got []string
		err := scanNULRecords(r, max, func(s string) error { got = append(got, s); return nil })
		return got, err
	}

	got, err := collect(strings.NewReader("a\x00bb\x00\x00ccc\x00"), 64)
	if err != nil || strings.Join(got, ",") != "a,bb,ccc" {
		t.Errorf("well-formed: %v, %v", got, err)
	}

	// One record far longer than the limit, never terminated: refused, and
	// the reader never hands over more than the limit.
	huge := io.MultiReader(strings.NewReader("ok\x00"), strings.NewReader(strings.Repeat("x", 1<<20)))
	if got, err := collect(huge, 4096); !errors.Is(err, errRecordTooLong) || len(got) != 1 {
		t.Errorf("over-long: %v, %v; want errRecordTooLong after the one good record", got, err)
	}

	if _, err := collect(strings.NewReader("a\x00tail"), 64); !errors.Is(err, errUnterminatedRecord) {
		t.Errorf("unterminated: err = %v, want errUnterminatedRecord", err)
	}

	broken := io.MultiReader(strings.NewReader("a\x00"), iotest.ErrReader(errors.New("pipe broke")))
	if _, err := collect(broken, 64); err == nil || !strings.Contains(err.Error(), "pipe broke") {
		t.Errorf("read error: err = %v, want it reported", err)
	}

	stop := errors.New("stop")
	calls := 0
	err = scanNULRecords(strings.NewReader("a\x00b\x00c\x00"), 64, func(string) error { calls++; return stop })
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("fn refusal: err = %v after %d calls, want stop after 1", err, calls)
	}
}
