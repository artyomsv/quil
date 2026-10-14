package config_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// AC-9: two writers of config.toml never share a temp file.
func TestSave_ConcurrentSavesLeaveAWholeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := config.Default()
			c.UI.SidebarWidth = 20 + i
			for j := 0; j < 10; j++ {
				_ = config.Save(p, c)
			}
		}(i)
	}
	wg.Wait()
	if _, err := config.Load(p); err != nil {
		t.Fatalf("final file does not load: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("%d entries beside config.toml", len(ents)-1)
	}
}

// A bindings.toml that is a link is replaced by a regular file, never written
// through (spec 5d §3.2a).
func TestWriteBindings_ReplacesALinkAtTheTarget(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, config.BindingsPath()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := config.WriteBindings(config.Bindings{Preset: "default"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("victim = %q", b)
	}
	if li, err := os.Lstat(config.BindingsPath()); err != nil || !li.Mode().IsRegular() {
		t.Fatalf("bindings.toml is not a regular file: %v %v", li, err)
	}
	if _, err := config.LoadBindings(); err != nil {
		t.Fatal(err)
	}
}
