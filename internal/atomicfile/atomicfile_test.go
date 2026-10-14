package atomicfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"
)

// Two processes writing one file each use their own temp file: every write
// leaves a whole file and nothing beside it.
func TestWrite_ConcurrentWritersLeaveAWholeFileAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				// A Windows rename can still lose to a reader twice in a row;
				// the property under test is the file, not the error.
				_ = Write(p, bytes.Repeat([]byte{byte('a' + i)}, 4096))
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = os.ReadFile(p)
			}
		}()
	}
	wg.Wait()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != "config.toml" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4096 || !bytes.Equal(got, bytes.Repeat(got[:1], 4096)) {
		t.Fatal("the file mixes two writes")
	}
}

// A failed first rename (a Windows reader holding the file) is retried once,
// and a write whose renames all fail leaves no temp file.
func TestWrite_RetriesTheRenameOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "instances.json")
	calls := 0
	orig := renameFn
	renameFn = func(a, b string) error {
		calls++
		if calls == 1 {
			return errors.New("sharing violation")
		}
		return orig(a, b)
	}
	t.Cleanup(func() { renameFn = orig })
	if err := Write(p, []byte("{}")); err != nil {
		t.Fatalf("Write after one failed rename: %v", err)
	}
	if calls != 2 {
		t.Fatalf("rename calls = %d, want 2", calls)
	}
	renameFn = func(string, string) error { return errors.New("always") }
	if err := Write(p, []byte("{}")); err == nil {
		t.Fatal("Write reported success with every rename failing")
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("a failed write left %d entries, want only instances.json", len(ents))
	}
}

// The temp keeps the target's stem: ipc.IsQuilHomeEntry accepts it, so a
// crash leftover never makes ProtectDir refuse QUIL_HOME.
func TestWrite_TempNameKeepsTheTargetStem(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	var src string
	orig := renameFn
	renameFn = func(a, b string) error { src = a; return orig(a, b) }
	t.Cleanup(func() { renameFn = orig })
	if err := Write(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^config\.toml\.\d+\.tmp$`).MatchString(filepath.Base(src)) {
		t.Fatalf("temp name %q", filepath.Base(src))
	}
}

// A link AT THE TARGET is replaced by a regular file, never written through:
// the rename replaces the directory entry, and the temp is never a link.
func TestWrite_ReplacesALinkAtTheTarget(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "bindings.toml")
	if err := os.Symlink(victim, p); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Write(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("the link was followed: victim = %q", b)
	}
	li, err := os.Lstat(p)
	if err != nil || !li.Mode().IsRegular() {
		t.Fatalf("target is not a regular file: %v %v", li, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("target = %q", b)
	}
}

func TestWrite_Mode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	p := filepath.Join(t.TempDir(), "templates.toml")
	if err := Write(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}
