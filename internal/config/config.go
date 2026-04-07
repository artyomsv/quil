package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Daemon      DaemonConfig      `toml:"daemon"`
	GhostBuffer GhostBufferConfig `toml:"ghost_buffer"`
	Logging     LoggingConfig     `toml:"logging"`
	Security    SecurityConfig    `toml:"security"`
	UI          UIConfig          `toml:"ui"`
	Keybindings  KeybindingsConfig  `toml:"keybindings"`
	MCP          MCPConfig          `toml:"mcp"`
	Notification NotificationConfig `toml:"notification"`
}

type NotificationConfig struct {
	SidebarWidth int `toml:"sidebar_width"` // default 30
	MaxEvents    int `toml:"max_events"`    // default 50
}

type MCPConfig struct {
	HighlightDuration string `toml:"highlight_duration"` // e.g., "10s"
	LogDir            string `toml:"log_dir"`            // empty = ~/.aethel/mcp-logs/
}

type DaemonConfig struct {
	SnapshotInterval string `toml:"snapshot_interval"`
	AutoStart        bool   `toml:"auto_start"`
}

type GhostBufferConfig struct {
	MaxLines int  `toml:"max_lines"`
	Dimmed   bool `toml:"dimmed"`
}

type LoggingConfig struct {
	Level     string `toml:"level"`
	MaxSizeMB int    `toml:"max_size_mb"`
	MaxFiles  int    `toml:"max_files"`
}

type SecurityConfig struct {
	EncryptTokens bool `toml:"encrypt_tokens"`
	RedactSecrets bool `toml:"redact_secrets"`
}

type UIConfig struct {
	TabDock          string `toml:"tab_dock"`
	Theme            string `toml:"theme"`
	MouseScrollLines int    `toml:"mouse_scroll_lines"`
	PageScrollLines  int    `toml:"page_scroll_lines"`
	ShowDisclaimer   bool   `toml:"show_disclaimer"`
}

type KeybindingsConfig struct {
	Quit            string `toml:"quit"`
	NewTab          string `toml:"new_tab"`
	ClosePane       string `toml:"close_pane"`
	CloseTab        string `toml:"close_tab"`
	SplitHorizontal string `toml:"split_horizontal"`
	SplitVertical   string `toml:"split_vertical"`
	NextPane        string `toml:"next_pane"`
	PrevPane        string `toml:"prev_pane"`
	RenameTab       string `toml:"rename_tab"`
	RenamePane      string `toml:"rename_pane"`
	CycleTabColor   string `toml:"cycle_tab_color"`
	ScrollPageUp    string `toml:"scroll_page_up"`
	ScrollPageDown  string `toml:"scroll_page_down"`
	Paste           string `toml:"paste"`
	JSONTransform   string `toml:"json_transform"`
	QuickActions    string `toml:"quick_actions"`
	FocusPane          string `toml:"focus_pane"`
	NotificationToggle string `toml:"notification_toggle"`
	NotificationFocus  string `toml:"notification_focus"`
	GoBack             string `toml:"go_back"`
	NotesToggle        string `toml:"notes_toggle"`
}

func Default() Config {
	return Config{
		Daemon: DaemonConfig{
			SnapshotInterval: "30s",
			AutoStart:        true,
		},
		GhostBuffer: GhostBufferConfig{
			MaxLines: 500,
			Dimmed:   true,
		},
		Logging: LoggingConfig{
			Level:     "info",
			MaxSizeMB: 10,
			MaxFiles:  3,
		},
		Security: SecurityConfig{
			EncryptTokens: true,
			RedactSecrets: true,
		},
		UI: UIConfig{
			TabDock:          "top",
			Theme:            "default",
			MouseScrollLines: 3,
			PageScrollLines:  0, // 0 = half-page (dynamic)
			ShowDisclaimer:   true,
		},
		MCP: MCPConfig{
			HighlightDuration: "10s",
		},
		Notification: NotificationConfig{
			SidebarWidth: 30,
			MaxEvents:    50,
		},
		Keybindings: KeybindingsConfig{
			Quit:            "ctrl+q",
			NewTab:          "ctrl+t",
			ClosePane:       "ctrl+w",
			CloseTab:        "alt+w",
			SplitHorizontal: "alt+h",
			SplitVertical:   "alt+v",
			NextPane:        "tab",
			PrevPane:        "shift+tab",
			RenameTab:       "f2",
			RenamePane:      "alt+f2",
			CycleTabColor:   "alt+c",
			ScrollPageUp:    "alt+pgup",
			ScrollPageDown:  "alt+pgdown",
			Paste:           "ctrl+v",
			JSONTransform:   "ctrl+j",
			QuickActions:    "ctrl+a",
			FocusPane:          "ctrl+e",
			NotificationToggle: "alt+n",
			NotificationFocus:  "f3",
			GoBack:             "alt+backspace",
			NotesToggle:        "alt+e",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Save writes the config to disk atomically (write .tmp then rename).
func Save(path string, cfg Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func AethelDir() string {
	if dir := os.Getenv("AETHEL_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aethel")
}

func ConfigPath() string {
	return filepath.Join(AethelDir(), "config.toml")
}

func SocketPath() string {
	return filepath.Join(AethelDir(), "aetheld.sock")
}

func PidPath() string {
	return filepath.Join(AethelDir(), "aetheld.pid")
}

func WorkspacePath() string {
	return filepath.Join(AethelDir(), "workspace.json")
}

func BufferDir() string {
	return filepath.Join(AethelDir(), "buffers")
}

func PluginsDir() string {
	return filepath.Join(AethelDir(), "plugins")
}

func WindowStatePath() string {
	return filepath.Join(AethelDir(), "window.json")
}

func InstancesPath() string {
	return filepath.Join(AethelDir(), "instances.json")
}

func MCPLogDir(cfg MCPConfig) string {
	if cfg.LogDir != "" {
		return cfg.LogDir
	}
	return filepath.Join(AethelDir(), "mcp-logs")
}

// NotesDir returns the directory where per-pane notes are stored.
func NotesDir() string {
	return filepath.Join(AethelDir(), "notes")
}
