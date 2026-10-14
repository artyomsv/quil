package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestSettings_KeysAreUniqueAndFound(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range config.Settings() {
		if seen[s.Key] {
			t.Fatalf("duplicate key %s", s.Key)
		}
		seen[s.Key] = true
		if s.Get == nil || s.Set == nil || s.Label == "" {
			t.Fatalf("%s: incomplete row", s.Key)
		}
		if got, ok := config.LookupSetting(s.Key); !ok || got.Key != s.Key {
			t.Fatalf("LookupSetting(%s) = %v, %v", s.Key, got.Key, ok)
		}
	}
	if _, ok := config.LookupSetting("listener.tcp"); ok {
		t.Fatal("listener.tcp must not be settable from the table")
	}
}

// The cases are the TUI setters' own rules (internal/tui/dialog.go:226-540):
// a refused value changes nothing at all.
func TestSettings_AcceptAndRefuse(t *testing.T) {
	cases := []struct {
		key, in string
		ok      bool
		want    string
	}{
		{"daemon.snapshot_interval", "45s", true, "45s"},
		{"daemon.snapshot_interval", "0s", false, ""},
		{"daemon.snapshot_interval", "soon", false, ""},
		{"ghost_buffer.dimmed", "false", true, "false"},
		{"ghost_buffer.max_lines", "500", true, "500"},
		{"ghost_buffer.max_lines", "0", false, ""},
		{"ui.scrollback_lines", "0", false, ""},
		{"ui.scrollback_lines", "2000000", true, "1000000"},
		{"ui.mouse_scroll_lines", "0", false, ""},
		{"ui.page_scroll_lines", "0", true, "0"},
		{"ui.page_scroll_lines", "-1", false, ""},
		{"ui.sidebar_width", "11", false, ""},
		{"ui.sidebar_width", "12", true, "12"},
		{"ui.unfocused_dim", "0.35", true, "0.35"},
		{"ui.unfocused_dim", "0", false, ""},
		{"ui.unfocused_dim", "0.004", false, ""},
		{"ui.unfocused_dim", "NaN", false, ""},
		{"ui.unfocused_dim", "0.95", false, ""},
		{"overlay.idle_timeout_minutes", "0", true, "0"},
		{"overlay.max_live", "-1", false, ""},
		{"logging.level", "warn", true, "warn"},
		{"logging.level", "trace", false, ""},
		{"ui.show_disclaimer", "false", true, "false"},
		{"update.auto", "true", true, "true"},
		{"update.auto", "yes", false, ""},
		{"notification.desktop.blocked", "false", true, "false"},
		{"notification.events.idle", "true", true, "true"},
		{"sandbox.default_image", "  img:1  ", true, "img:1"},
		{"sandbox.default_image", "", true, ""},
		{"sandbox.default_image", strings.Repeat("é", 256), false, ""},
		{"sandbox.sign_in", "shared", true, "shared"},
		{"sandbox.sign_in", "magic", false, ""},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.in, func(t *testing.T) {
			s, ok := config.LookupSetting(c.key)
			if !ok {
				t.Fatalf("no row %s", c.key)
			}
			cfg := config.Default()
			before := cfg
			err := s.Set(&cfg, c.in)
			if c.ok {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if got := s.Get(cfg); got != c.want {
					t.Fatalf("Get = %q, want %q", got, c.want)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted a value the TUI refuses")
			}
			if !reflect.DeepEqual(cfg, before) {
				t.Fatal("a refused value changed the config")
			}
		})
	}
}

// The dim switch reports STATE (toggleUnfocusedDim's rule): a legacy level 0
// reads off, and switching on supplies the default level.
func TestSettings_DimSwitchActsOnState(t *testing.T) {
	s, _ := config.LookupSetting("ui.unfocused_dim_enabled")
	cfg := config.Default()
	cfg.UI.UnfocusedDimEnabled, cfg.UI.UnfocusedDim = true, 0
	if s.Get(cfg) != "false" {
		t.Fatal("legacy level 0 must read off")
	}
	if err := s.Set(&cfg, "true"); err != nil {
		t.Fatal(err)
	}
	if !cfg.UI.UnfocusedDimEnabled || cfg.UI.UnfocusedDim != config.DefaultUnfocusedDim {
		t.Fatalf("on: %+v", cfg.UI)
	}
	cfg.UI.UnfocusedDim = 0.35
	if err := s.Set(&cfg, "false"); err != nil {
		t.Fatal(err)
	}
	if cfg.UI.UnfocusedDimEnabled || cfg.UI.UnfocusedDim != 0.35 {
		t.Fatalf("off must keep the level: %+v", cfg.UI)
	}
}

// One sign-in choice writes both keys, so token + shared is unreachable.
func TestSettings_SignInWritesBothKeys(t *testing.T) {
	s, _ := config.LookupSetting("sandbox.sign_in")
	cfg := config.Default()
	cfg.Sandbox.SharedClaudeConfig = true
	if err := s.Set(&cfg, "token"); err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Auth != "token" || cfg.Sandbox.SharedClaudeConfig {
		t.Fatalf("token: %+v", cfg.Sandbox)
	}
	if err := s.Set(&cfg, "shared"); err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Auth != "browser" || !cfg.Sandbox.SharedClaudeConfig {
		t.Fatalf("shared: %+v", cfg.Sandbox)
	}
}

func TestSetKeyPresetAndPrefix(t *testing.T) {
	b := config.Bindings{Preset: "tmux", Prefix: "ctrl+a"}
	if err := config.SetKeyPreset(&b, "nope"); err == nil || b.Preset != "tmux" {
		t.Fatalf("unknown preset: %v %+v", err, b)
	}
	if err := config.SetKeyPreset(&b, "default"); err != nil {
		t.Fatal(err)
	}
	if b.Prefix != "" {
		t.Fatal("a preset without ${prefix} must drop the prefix")
	}
	if err := config.SetKeyPrefix(&b, "ctrl+a"); err == nil {
		t.Fatal("prefix accepted for a preset that writes none")
	}
	if err := config.SetKeyPreset(&b, "tmux"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetKeyPrefix(&b, "ctrl+a ctrl+b"); err == nil {
		t.Fatal("a two-chord prefix was accepted")
	}
	if err := config.SetKeyPrefix(&b, " ctrl+a "); err != nil || b.Prefix != "ctrl+a" {
		t.Fatalf("prefix: %v %q", err, b.Prefix)
	}
	if err := config.SetKeyPrefix(&b, ""); err != nil || b.Prefix != "" {
		t.Fatalf("empty prefix: %v %q", err, b.Prefix)
	}
}
