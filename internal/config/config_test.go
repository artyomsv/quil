package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
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
	if cfg.Keybindings.FocusPane != "ctrl+e" {
		t.Errorf("expected focus_pane=ctrl+e, got %s", cfg.Keybindings.FocusPane)
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

func TestQuilDir(t *testing.T) {
	dir := config.QuilDir()
	if dir == "" {
		t.Error("expected non-empty quil dir")
	}
}

func TestQuilDir_EnvOverride(t *testing.T) {
	t.Setenv("QUIL_HOME", "/tmp/custom-quil")
	if got := config.QuilDir(); got != "/tmp/custom-quil" {
		t.Errorf("expected /tmp/custom-quil, got %s", got)
	}
}

func TestShowDisclaimerDefault(t *testing.T) {
	cfg := config.Default()
	if !cfg.UI.ShowDisclaimer {
		t.Error("expected ShowDisclaimer=true by default")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	cfg := config.Default()
	cfg.UI.ShowDisclaimer = false
	cfg.UI.TabDock = "bottom"

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.UI.ShowDisclaimer {
		t.Error("expected ShowDisclaimer=false after roundtrip")
	}
	if loaded.UI.TabDock != "bottom" {
		t.Errorf("expected tab_dock=bottom, got %s", loaded.UI.TabDock)
	}
	// Defaults should survive
	if loaded.Keybindings.NewTab != "ctrl+t" {
		t.Errorf("expected default new_tab=ctrl+t, got %s", loaded.Keybindings.NewTab)
	}
}

func TestPathHelpers(t *testing.T) {
	dir := config.QuilDir()
	if dir == "" {
		t.Skip("cannot determine home directory")
	}

	tests := []struct {
		name     string
		fn       func() string
		expected string
	}{
		{"SocketPath", config.SocketPath, filepath.Join(dir, "quild.sock")},
		{"ConfigPath", config.ConfigPath, filepath.Join(dir, "config.toml")},
		{"PidPath", config.PidPath, filepath.Join(dir, "quild.pid")},
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
