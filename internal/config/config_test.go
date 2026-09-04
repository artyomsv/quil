package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

// TestDefault_ToggleHunkBinding pins alt+d rather than the more obvious alt+h.
//
// Alt+H is a documented deliberate PTY passthrough (see the PaneLeft comment in
// config.go and .claude/rules/tui-rendering.md): it is left unbound at the
// global level so it reaches the child process, alongside Alt+V, which
// claude-code uses for image paste. Binding it here would take that away
// silently from every pane.
func TestDefault_ToggleHunkBinding(t *testing.T) {
	cfg := config.Default()
	if cfg.Keybindings.ToggleHunk != "alt+d" {
		t.Errorf("ToggleHunk = %q, want alt+d", cfg.Keybindings.ToggleHunk)
	}
	if cfg.Keybindings.ToggleHunk == "alt+h" {
		t.Error("alt+h is reserved for the PTY passthrough — see tui-rendering.md")
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

func TestRecentCWDsPath_LocalIsUnchanged(t *testing.T) {
	if got := filepath.Base(config.RecentCWDsPath("")); got != "recent-cwds.json" {
		t.Errorf("basename = %q, want recent-cwds.json — local users must see no migration", got)
	}
}

func TestRecentCWDsPath_PerRemoteTarget(t *testing.T) {
	if config.RecentCWDsPath("") == config.RecentCWDsPath("gpu01") {
		t.Fatal("remote and local share one recent-cwds file")
	}
	if config.RecentCWDsPath("gpu01") == config.RecentCWDsPath("gpu02") {
		t.Fatal("two destinations share one recent-cwds file")
	}
}

// Destinations are user input and reach a filename.
func TestRecentCWDsPath_SanitizesDestination(t *testing.T) {
	for _, dest := range []string{"user@host", "host:22", "../../etc/passwd", `a/b\c`, "", "  "} {
		got := filepath.Base(config.RecentCWDsPath(dest))
		if strings.ContainsAny(got, `/\:`) || strings.Contains(got, "..") {
			t.Errorf("RecentCWDsPath(%q) basename = %q, unsafe", dest, got)
		}
	}
}

// Collapsing unsafe characters must not collapse distinct destinations onto one
// another: "a/b" and "a-b" both sanitize to "a-b" without the hash.
func TestRecentCWDsPath_CollapsedDestinationsStayDistinct(t *testing.T) {
	if config.RecentCWDsPath("a/b") == config.RecentCWDsPath("a-b") {
		t.Error("two destinations that sanitize alike share one file")
	}
}

// The cache is per destination for the same reason recent-cwds is: one file
// shared by two hosts would show one host's projects under the other's name.
func TestRemoteProjectsPath_DistinctPerDest(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	a := config.RemoteProjectsPath("user@a")
	b := config.RemoteProjectsPath("user@b")
	if a == b {
		t.Fatalf("both destinations resolve to %q", a)
	}
	if config.RemoteProjectsPath("") == a {
		t.Error("the empty dest must not share a file with a real one")
	}
}

// "a/b" and "a-b" collapse to the same readable component; the digest is what
// keeps them apart.
func TestRemoteProjectsPath_CollapsedDestinationsStayDistinct(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if config.RemoteProjectsPath("a/b") == config.RemoteProjectsPath("a-b") {
		t.Error("collapsed destinations share a cache file")
	}
}

func TestDefault_OverlayPolicy(t *testing.T) {
	c := config.Default()
	if c.Overlay.IdleTimeoutMinutes != 5 {
		t.Errorf("IdleTimeoutMinutes = %d, want 5", c.Overlay.IdleTimeoutMinutes)
	}
	if c.Overlay.MaxLive != 5 {
		t.Errorf("MaxLive = %d, want 5", c.Overlay.MaxLive)
	}
}

// An absent [overlay] section must load the DEFAULTS, not zeros — zero means
// "disabled" for both knobs, so a config written before this feature existed
// would silently opt out of it.
func TestLoad_AbsentOverlaySection_KeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Overlay.IdleTimeoutMinutes != 5 || c.Overlay.MaxLive != 5 {
		t.Errorf("Overlay = %+v, want the defaults (5, 5)", c.Overlay)
	}
}

func TestOverlayConfig_TOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	in := config.Default()
	in.Overlay.IdleTimeoutMinutes = 11
	in.Overlay.MaxLive = 2
	if err := config.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Overlay != in.Overlay {
		t.Errorf("round trip = %+v, want %+v", out.Overlay, in.Overlay)
	}
}

func TestDesktopNotificationDefaults(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	d := cfg.Notification.Desktop

	if !d.Enabled {
		t.Error("desktop notifications must default to ON — the flag expresses intent; registration is the gate")
	}
	if !d.Blocked || !d.Done {
		t.Errorf("both kinds default on: blocked=%v done=%v", d.Blocked, d.Done)
	}
	if got := d.CooldownDuration(); got != 5*time.Second {
		t.Errorf("CooldownDuration() = %v, want 5s", got)
	}
}

// A garbage value must not disable the rate limit — that would turn a typo
// into a toast storm.
func TestDesktopCooldownDuration_FallsBackOnGarbage(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"2m", 2 * time.Minute},
		{"", 5 * time.Second},
		{"nonsense", 5 * time.Second},
		{"-5s", 5 * time.Second},
		{"0s", 5 * time.Second},
	}
	for _, tt := range tests {
		d := config.DesktopConfig{Cooldown: tt.in}
		if got := d.CooldownDuration(); got != tt.want {
			t.Errorf("Cooldown %q → %v, want %v", tt.in, got, tt.want)
		}
	}
}

// The desktop block must survive a save/load round trip, or a Settings toggle
// silently reverts on the next launch.
func TestDesktopConfig_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := config.Default()
	in.Notification.Desktop.Enabled = false
	in.Notification.Desktop.Done = false
	in.Notification.Desktop.Cooldown = "90s"
	if err := config.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Notification.Desktop != in.Notification.Desktop {
		t.Errorf("round trip = %+v, want %+v", out.Notification.Desktop, in.Notification.Desktop)
	}
}

// TestLoad_HooksCodexKnob: the third hook source has its own tier, defaulted
// like the other two and overridable from config.toml.
func TestLoad_HooksCodexKnob(t *testing.T) {
	if got := config.Default().Notification.Hooks.Codex; got != "default" {
		t.Errorf("default codex hooks tier = %q, want \"default\"", got)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[notification.hooks]\ncodex = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Notification.Hooks.Codex != "off" {
		t.Errorf("loaded codex tier = %q, want off", loaded.Notification.Hooks.Codex)
	}
	if loaded.Notification.Hooks.Claude != "default" {
		t.Errorf("claude tier must keep its default beside a codex override, got %q", loaded.Notification.Hooks.Claude)
	}
}
