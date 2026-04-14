package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultPlugins_DetectsStalePlugins(t *testing.T) {
	dir := t.TempDir()

	// First run: creates fresh files — expect 0 stale.
	stale, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("first run: expected 0 stale plugins, got %d", len(stale))
	}

	// Downgrade claude-code.toml by writing content without schema_version.
	ccPath := filepath.Join(dir, "claude-code.toml")
	downgraded := []byte("[plugin]\nname = \"claude-code\"\ndisplay_name = \"Claude Code\"\ncategory = \"ai\"\n\n[command]\ncmd = \"claude\"\n")
	if err := os.WriteFile(ccPath, downgraded, 0600); err != nil {
		t.Fatalf("write downgraded: %v", err)
	}

	// Second run: should detect 1 stale plugin.
	stale, err = EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("second run: expected 1 stale plugin, got %d", len(stale))
	}

	sp := stale[0]
	if sp.Name != "claude-code" {
		t.Errorf("expected stale plugin name 'claude-code', got %q", sp.Name)
	}
	if sp.FilePath != ccPath {
		t.Errorf("expected FilePath %q, got %q", ccPath, sp.FilePath)
	}
	if !bytes.Equal(sp.UserData, downgraded) {
		t.Error("UserData does not match downgraded content")
	}
	if ParseSchemaVersion(sp.DefaultData) == 0 {
		t.Error("DefaultData should have a non-zero schema_version")
	}

	// Verify the file on disk was NOT overwritten.
	ondisk, err := os.ReadFile(ccPath)
	if err != nil {
		t.Fatalf("read after second run: %v", err)
	}
	if !bytes.Equal(ondisk, downgraded) {
		t.Error("stale file was overwritten — expected it to remain unchanged")
	}
}

func TestEnsureDefaultPlugins_CurrentVersionNotStale(t *testing.T) {
	dir := t.TempDir()

	// First run: creates fresh files at current schema version.
	_, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run: files already at current version — expect 0 stale.
	stale, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("second run: expected 0 stale plugins, got %d", len(stale))
	}
}
