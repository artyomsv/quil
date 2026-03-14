package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/aethel/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg := config.Default()

	if cfg.Daemon.SnapshotInterval != "30s" {
		t.Errorf("expected snapshot_interval=30s, got %s", cfg.Daemon.SnapshotInterval)
	}
	if !cfg.Daemon.AutoStart {
		t.Error("expected auto_start=true")
	}
	if cfg.UI.TabDock != "top" {
		t.Errorf("expected tab_dock=top, got %s", cfg.UI.TabDock)
	}
	if cfg.Keybindings.NewTab != "ctrl+t" {
		t.Errorf("expected new_tab=ctrl+t, got %s", cfg.Keybindings.NewTab)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte(`
[daemon]
snapshot_interval = "10s"
auto_start = false

[ui]
tab_dock = "bottom"
`)
	if err := os.WriteFile(cfgPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Daemon.SnapshotInterval != "10s" {
		t.Errorf("expected snapshot_interval=10s, got %s", cfg.Daemon.SnapshotInterval)
	}
	if cfg.Daemon.AutoStart {
		t.Error("expected auto_start=false")
	}
	if cfg.UI.TabDock != "bottom" {
		t.Errorf("expected tab_dock=bottom, got %s", cfg.UI.TabDock)
	}
	// Unset fields keep defaults
	if cfg.Keybindings.NewTab != "ctrl+t" {
		t.Errorf("expected default new_tab=ctrl+t, got %s", cfg.Keybindings.NewTab)
	}
}

func TestAethelDir(t *testing.T) {
	dir := config.AethelDir()
	if dir == "" {
		t.Error("expected non-empty aethel dir")
	}
}

func TestAethelDir_EnvOverride(t *testing.T) {
	t.Setenv("AETHEL_HOME", "/tmp/custom-aethel")
	if got := config.AethelDir(); got != "/tmp/custom-aethel" {
		t.Errorf("expected /tmp/custom-aethel, got %s", got)
	}
}

func TestPathHelpers(t *testing.T) {
	dir := config.AethelDir()
	if dir == "" {
		t.Skip("cannot determine home directory")
	}

	tests := []struct {
		name     string
		fn       func() string
		expected string
	}{
		{"SocketPath", config.SocketPath, filepath.Join(dir, "aetheld.sock")},
		{"ConfigPath", config.ConfigPath, filepath.Join(dir, "config.toml")},
		{"PidPath", config.PidPath, filepath.Join(dir, "aetheld.pid")},
		{"WorkspacePath", config.WorkspacePath, filepath.Join(dir, "workspace.json")},
		{"BufferDir", config.BufferDir, filepath.Join(dir, "buffers")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn()
			if got != tt.expected {
				t.Errorf("got %s, want %s", got, tt.expected)
			}
		})
	}
}
