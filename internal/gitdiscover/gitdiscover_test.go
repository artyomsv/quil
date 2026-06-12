package gitdiscover

import (
	"os"
	"path/filepath"
	"testing"
)

// mkRepo creates dir with a .git subdirectory (normal repo shape).
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatalf("mkRepo %s: %v", dir, err)
	}
}

// mkWorktree creates dir with a .git FILE (worktree/submodule shape).
func mkWorktree(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkWorktree mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatalf("mkWorktree write %s: %v", dir, err)
	}
}

func TestEnclosingRepo_RepoRoot(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkRepo(t, root)
	got, ok := EnclosingRepo(root)
	if !ok {
		t.Fatal("expected ok=true at repo root")
	}
	if got != root {
		t.Errorf("got %q, want %q", got, root)
	}
}

func TestEnclosingRepo_NestedDir(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkRepo(t, root)
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	got, ok := EnclosingRepo(nested)
	if !ok || got != root {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, root)
	}
}

func TestEnclosingRepo_GitFile_Worktree(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(base, "wt")
	mkWorktree(t, wt)
	got, ok := EnclosingRepo(wt)
	if !ok || got != wt {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, wt)
	}
}

func TestEnclosingRepo_NoRepo(t *testing.T) {
	t.Parallel()
	if got, ok := EnclosingRepo(t.TempDir()); ok {
		t.Errorf("expected not found, got %q", got)
	}
}

func TestEnclosingRepo_EmptyAndMissing(t *testing.T) {
	t.Parallel()
	if _, ok := EnclosingRepo(""); ok {
		t.Error("empty dir must not resolve")
	}
	if _, ok := EnclosingRepo(filepath.Join(t.TempDir(), "does-not-exist")); ok {
		t.Error("missing dir with no enclosing repo must not resolve")
	}
}

func TestSubRepos_OneLevel(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkRepo(t, filepath.Join(base, "proj-a"))
	mkRepo(t, filepath.Join(base, "proj-b"))
	mkWorktree(t, filepath.Join(base, "proj-wt"))
	if err := os.MkdirAll(filepath.Join(base, "plain", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	mkRepo(t, filepath.Join(base, "plain", "deep"))

	got := SubRepos(base)
	want := map[string]bool{
		filepath.Join(base, "proj-a"):  true,
		filepath.Join(base, "proj-b"):  true,
		filepath.Join(base, "proj-wt"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d repos %v, want %d", len(got), got, len(want))
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected sub-repo %q", r)
		}
	}
}

func TestSubRepos_UnreadableDir_ReturnsNil(t *testing.T) {
	t.Parallel()
	if got := SubRepos(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("expected nil for unreadable dir, got %v", got)
	}
}

func TestCandidates_EnclosingFirstThenSubsDeduped(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkRepo(t, base)
	sub := filepath.Join(base, "vendor-fork")
	mkRepo(t, sub)

	got := Candidates(base)
	if len(got) != 2 {
		t.Fatalf("got %v, want [base sub]", got)
	}
	if got[0] != base || got[1] != sub {
		t.Errorf("got %v, want [%q %q]", got, base, sub)
	}
}

func TestCandidates_EmptyDir(t *testing.T) {
	t.Parallel()
	if got := Candidates(""); got != nil {
		t.Errorf("expected nil for empty dir, got %v", got)
	}
}
