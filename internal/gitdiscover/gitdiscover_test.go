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

func TestSubRepos_NoSubRepos_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := SubRepos(base); len(got) != 0 {
		t.Errorf("expected no sub-repos, got %v", got)
	}
}

func TestCandidates_SymlinkDir_CanonicalAndDeduped(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	mkRepo(t, real)
	sub := filepath.Join(real, "vendor-fork")
	mkRepo(t, sub)
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink creation not permitted: %v", err)
	}

	got := Candidates(link)
	if len(got) != 2 {
		t.Fatalf("got %v, want exactly 2 entries [%q %q]", got, real, sub)
	}
	if got[0] != real || got[1] != sub {
		t.Errorf("got %v, want resolved paths [%q %q]", got, real, sub)
	}
}

func TestCandidates_EmptyDir(t *testing.T) {
	t.Parallel()
	if got := Candidates(""); got != nil {
		t.Errorf("expected nil for empty dir, got %v", got)
	}
}

// TestIsUNCOrDevicePath covers the helper directly.
func TestIsUNCOrDevicePath(t *testing.T) {
	t.Parallel()
	yes := []string{
		`\\host\share`,
		`\\host\share\repo`,
		`\\?\C:\x`,
		`\\.\PIPE\x`,
		`//host/share`,
		`//host/share/repo`,
	}
	no := []string{
		`C:\Users\foo`,
		`/home/foo`,
		`relative/path`,
		``,
	}
	for _, p := range yes {
		if !isUNCOrDevicePath(p) {
			t.Errorf("isUNCOrDevicePath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if isUNCOrDevicePath(p) {
			t.Errorf("isUNCOrDevicePath(%q) = true, want false", p)
		}
	}
}

// TestCanonical_RejectsUNC asserts that EnclosingRepo and Candidates both
// return empty/false for UNC and device paths. This prevents an untrusted
// OSC7-reported CWD from triggering an outbound SMB connection via os.Stat.
func TestCanonical_RejectsUNC(t *testing.T) {
	t.Parallel()
	uncPaths := []string{
		`\\host\share\repo`,
		`//host/share`,
		`\\?\C:\x`,
		`\\.\PIPE\x`,
	}
	for _, p := range uncPaths {
		p := p
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			if got, ok := EnclosingRepo(p); ok || got != "" {
				t.Errorf("EnclosingRepo(%q) = (%q, %v), want (\"\", false)", p, got, ok)
			}
			if got := Candidates(p); got != nil {
				t.Errorf("Candidates(%q) = %v, want nil", p, got)
			}
		})
	}
}

// TestCanonical_NormalPathStillWorks is a regression guard — a normal absolute
// path must still be resolved by canonical (the UNC check must not be a no-op
// that also blocks valid paths).
func TestCanonical_NormalPathStillWorks(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkRepo(t, base)
	got, ok := EnclosingRepo(base)
	if !ok || got != base {
		t.Errorf("EnclosingRepo(%q) = (%q, %v), want (%q, true)", base, got, ok, base)
	}
}
