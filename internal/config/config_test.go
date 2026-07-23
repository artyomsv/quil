package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestIsDefaultQuilDir(t *testing.T) {
	def := config.DefaultQuilDir()
	if def == "" {
		t.Skip("no home dir on this runner")
	}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"exact default", def, true},
		{"trailing separator", def + string(filepath.Separator), true},
		{"different dir", filepath.Join(def, "sub"), false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := config.IsDefaultQuilDir(tt.in); got != tt.want {
				t.Errorf("IsDefaultQuilDir(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
	if runtime.GOOS == "windows" {
		t.Run("case insensitive on windows", func(t *testing.T) {
			if !config.IsDefaultQuilDir(strings.ToUpper(def)) {
				t.Errorf("IsDefaultQuilDir(%q) = false, want true (Windows paths are case-insensitive)", strings.ToUpper(def))
			}
		})
	}
}

func TestDefault_ToggleLazygitBinding(t *testing.T) {
	cfg := config.Default()
	if cfg.Keybindings.ToggleLazygit != "alt+g" {
		t.Errorf("ToggleLazygit = %q, want alt+g", cfg.Keybindings.ToggleLazygit)
	}
}

func TestDefaultKeybindings_CommandHistory(t *testing.T) {
	cfg := config.Default()
	if cfg.Keybindings.CommandHistory != "alt+shift+i" {
		t.Fatalf("want alt+shift+i, got %q", cfg.Keybindings.CommandHistory)
	}
}

func TestDefaultKeybindings_CommandPalette(t *testing.T) {
	cfg := config.Default()
	if got := cfg.Keybindings.CommandPalette; got != "alt+shift+p" {
		t.Errorf("CommandPalette default = %q, want %q", got, "alt+shift+p")
	}
}

func TestDefault_UpdateSection(t *testing.T) {
	cfg := config.Default()
	if !cfg.Update.Check {
		t.Error("Update.Check default = false, want true")
	}
	if !cfg.Update.Auto {
		t.Error("Update.Auto default = false, want true")
	}
}

func TestLoad_MissingUpdateSection_KeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = \"default\"\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Update.Check || !cfg.Update.Auto {
		t.Errorf("missing [update] section: Check=%v Auto=%v, want true/true", cfg.Update.Check, cfg.Update.Auto)
	}
}

func TestLoad_MigratesLegacyQuickActions(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(legacyPath, []byte("[keybindings]\nquick_actions = \"ctrl+a\"\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(legacyPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Keybindings.QuickActions != "alt+a" {
		t.Errorf("QuickActions = %q, want migrated alt+a", cfg.Keybindings.QuickActions)
	}

	// A deliberate, non-legacy customization must survive untouched.
	customPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(customPath, []byte("[keybindings]\nquick_actions = \"ctrl+x\"\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err = config.Load(customPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Keybindings.QuickActions != "ctrl+x" {
		t.Errorf("QuickActions = %q, want untouched ctrl+x", cfg.Keybindings.QuickActions)
	}
}

func TestUpdatePaths_UnderQuilDir(t *testing.T) {
	t.Setenv("QUIL_HOME", filepath.Join(t.TempDir(), "qh"))
	root := config.QuilDir()
	if got, want := config.UpdateDir(), filepath.Join(root, "update"); got != want {
		t.Errorf("UpdateDir = %q, want %q", got, want)
	}
	if got, want := config.UpdateStagingDir("1.2.3"), filepath.Join(root, "update", "staged", "1.2.3"); got != want {
		t.Errorf("UpdateStagingDir = %q, want %q", got, want)
	}
	if got, want := config.UpdateStatePath(), filepath.Join(root, "update", "state.json"); got != want {
		t.Errorf("UpdateStatePath = %q, want %q", got, want)
	}
	if got, want := config.UpdateNotifiedPath(), filepath.Join(root, "update", "notified.json"); got != want {
		t.Errorf("UpdateNotifiedPath = %q, want %q", got, want)
	}
}
