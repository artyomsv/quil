package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Daemon       DaemonConfig       `toml:"daemon"`
	GhostBuffer  GhostBufferConfig  `toml:"ghost_buffer"`
	Logging      LoggingConfig      `toml:"logging"`
	Security     SecurityConfig     `toml:"security"`
	UI           UIConfig           `toml:"ui"`
	Keybindings  KeybindingsConfig  `toml:"keybindings"`
	MCP          MCPConfig          `toml:"mcp"`
	Notification NotificationConfig `toml:"notification"`
}

type NotificationConfig struct {
	SidebarWidth int                     `toml:"sidebar_width"` // default 30
	MaxEvents    int                     `toml:"max_events"`    // default 200
	Hooks        HookNotificationsConfig `toml:"hooks"`
}

// HookNotificationsConfig controls which hook-driven events get spool-emitted
// per source. Tier values are "default" / "verbose" / "off". Daemon passes
// the resolved value to the hook scripts via the QUIL_HOOK_MODE env var at
// pane spawn so the script can branch on it (default → forward the v1 tier;
// verbose → also forward tool-use + pre/post events; off → no spool writes
// at all). Unset = "default" downstream.
type HookNotificationsConfig struct {
	Claude   string `toml:"claude"`
	OpenCode string `toml:"opencode"`
}

type MCPConfig struct {
	HighlightDuration string `toml:"highlight_duration"` // e.g., "10s"
	LogDir            string `toml:"log_dir"`            // empty = ~/.quil/mcp-logs/
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
	Level string `toml:"level"`

	// MaxSizeMB and MaxFiles drive log rotation via logger.RotatingWriter.
	// When the active quild.log / quil.log would exceed MaxSizeMB it is
	// rotated to a timestamped archive (stem-YYYYMMDD-HHMMSS.log) and a
	// fresh base file is opened. The newest MaxFiles archives are kept;
	// older ones are pruned by modification time. Implemented natively in
	// internal/logger/rotate.go — no external dependency.
	MaxSizeMB int `toml:"max_size_mb"`
	MaxFiles  int `toml:"max_files"`
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
	// LogViewerPageLines controls the cursor jump distance for Alt+Up /
	// Alt+Down inside the F1 → log viewer. 0 falls back to the default 40.
	LogViewerPageLines int  `toml:"log_viewer_page_lines"`
	ShowDisclaimer     bool `toml:"show_disclaimer"`
}

type KeybindingsConfig struct {
	Quit            string `toml:"quit"`
	NewTab          string `toml:"new_tab"`
	ClosePane       string `toml:"close_pane"`
	CloseTab        string `toml:"close_tab"`
	SplitHorizontal string `toml:"split_horizontal"`
	SplitVertical   string `toml:"split_vertical"`
	// Linear pane cycling. Empty string = unbound (the default) — users
	// now navigate spatially via PaneLeft/Right/Up/Down. Keeping the fields
	// for backward compat so existing configs that set e.g. next_pane = "tab"
	// continue to work (though that would re-intercept Tab from the PTY).
	NextPane string `toml:"next_pane"`
	PrevPane string `toml:"prev_pane"`
	// Spatial pane navigation — focus the neighbor in a given direction.
	// Defaults are Alt+Arrow. Tab and Shift+Tab are deliberately NOT used
	// so shell completion and Claude Code mode cycling reach the PTY
	// unmolested. Plain Alt+H / Alt+V are also free for the PTY (claude-code
	// uses Alt+V to paste an image); splits live on Alt+Shift+H / Alt+Shift+V
	// instead. Vim users can rebind to "alt+h"/"alt+l"/"alt+k"/"alt+j" in
	// config.toml if they want the classic hjkl motion.
	PaneLeft  string `toml:"pane_left"`
	PaneRight string `toml:"pane_right"`
	PaneUp    string `toml:"pane_up"`
	PaneDown  string `toml:"pane_down"`

	RenameTab          string `toml:"rename_tab"`
	RenamePane         string `toml:"rename_pane"`
	CycleTabColor      string `toml:"cycle_tab_color"`
	ScrollPageUp       string `toml:"scroll_page_up"`
	ScrollPageDown     string `toml:"scroll_page_down"`
	Paste              string `toml:"paste"`
	JSONTransform      string `toml:"json_transform"`
	QuickActions       string `toml:"quick_actions"`
	FocusPane          string `toml:"focus_pane"`
	NotificationToggle string `toml:"notification_toggle"`
	NotificationFocus  string `toml:"notification_focus"`
	// MutePane toggles notification mute on the active pane (idle/bell/exit
	// events stop firing). Useful for `npm test --watch` and other chatty
	// processes that would otherwise flood the sidebar.
	MutePane    string `toml:"mute_pane"`
	GoBack      string `toml:"go_back"`
	NotesToggle string `toml:"notes_toggle"`
	// Redraw forces a full screen repaint (tea.ClearScreen). Recovery key
	// for rendering artifacts left behind by cell-diff drift — width
	// disagreements between Quil and the host terminal (most common on
	// Windows) scramble characters until something repaints everything.
	Redraw string `toml:"redraw"`
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
			MaxSizeMB: 5,
			MaxFiles:  10,
		},
		Security: SecurityConfig{
			EncryptTokens: true,
			RedactSecrets: true,
		},
		UI: UIConfig{
			TabDock:            "top",
			Theme:              "default",
			MouseScrollLines:   3,
			PageScrollLines:    0,  // 0 = half-page (dynamic) — used by terminal pane scrollback
			LogViewerPageLines: 40, // Alt+Up / Alt+Down jump in F1 → log viewer
			ShowDisclaimer:     true,
		},
		MCP: MCPConfig{
			HighlightDuration: "10s",
		},
		Notification: NotificationConfig{
			SidebarWidth: 30,
			MaxEvents:    200,
			Hooks: HookNotificationsConfig{
				Claude:   "default",
				OpenCode: "default",
			},
		},
		Keybindings: KeybindingsConfig{
			Quit:      "ctrl+q",
			NewTab:    "ctrl+t",
			ClosePane: "ctrl+w",
			CloseTab:  "alt+w",
			// alt+shift+h / alt+shift+v — mnemonic preserved ("h for horizontal,
			// v for vertical"), extra Shift dodges claude-code's Alt-letter
			// bindings (Alt+V pastes an image in claude-code).
			SplitHorizontal: "alt+shift+h",
			SplitVertical:   "alt+shift+v",
			NextPane:        "", // unbound — use directional PaneLeft/Right/Up/Down
			PrevPane:        "", // unbound — use directional PaneLeft/Right/Up/Down
			PaneLeft:        "alt+left",
			PaneRight:       "alt+right",
			PaneUp:          "alt+up",
			PaneDown:        "alt+down",
			RenameTab:       "f2",
			// macOS often eats F2 and may not forward Option as Meta; the
			// second binding is the reliable fallback.
			RenamePane:         "alt+f2,alt+shift+r",
			CycleTabColor:      "alt+c",
			ScrollPageUp:       "alt+pgup",
			ScrollPageDown:     "alt+pgdown",
			Paste:              "ctrl+v",
			JSONTransform:      "ctrl+j",
			QuickActions:       "ctrl+a",
			FocusPane:          "ctrl+e",
			NotificationToggle: "alt+n",
			NotificationFocus:  "f3",
			MutePane:           "alt+m",
			GoBack:             "alt+backspace",
			NotesToggle:        "alt+e",
			// Mnemonic: Ctrl+L clears/redraws a shell; the Alt+Shift layer
			// keeps plain Ctrl+L flowing to the PTY.
			Redraw: "alt+shift+l",
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

func QuilDir() string {
	if dir := os.Getenv("QUIL_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".quil")
}

func ConfigPath() string {
	return filepath.Join(QuilDir(), "config.toml")
}

// PasteDir returns the directory where Quil writes clipboard images that
// are pasted into a pane. Used by the image-paste proxy that works around
// Claude Code's broken Windows clipboard reader (see
// anthropics/claude-code#32791) — Quil reads the image, saves a PNG here,
// and pastes the absolute path into the PTY.
func PasteDir() string {
	return filepath.Join(QuilDir(), "paste")
}

func SocketPath() string {
	return filepath.Join(QuilDir(), "quild.sock")
}

func PidPath() string {
	return filepath.Join(QuilDir(), "quild.pid")
}

func WorkspacePath() string {
	return filepath.Join(QuilDir(), "workspace.json")
}

func BufferDir() string {
	return filepath.Join(QuilDir(), "buffers")
}

func PluginsDir() string {
	return filepath.Join(QuilDir(), "plugins")
}

func WindowStatePath() string {
	return filepath.Join(QuilDir(), "window.json")
}

func InstancesPath() string {
	return filepath.Join(QuilDir(), "instances.json")
}

func MCPLogDir(cfg MCPConfig) string {
	if cfg.LogDir != "" {
		return cfg.LogDir
	}
	return filepath.Join(QuilDir(), "mcp-logs")
}

// NotesDir returns the directory where per-pane notes are stored.
func NotesDir() string {
	return filepath.Join(QuilDir(), "notes")
}

// ClaudeHookDir returns the directory where Quil writes the Claude Code
// SessionStart hook scripts it passes via --settings. Lives under Quil's
// own home so we never touch the user's ~/.claude/ config.
func ClaudeHookDir() string {
	return filepath.Join(QuilDir(), "claudehook")
}

// EventsDir returns the directory where Claude / opencode hooks append
// per-pane JSONL event spool files (<paneID>.jsonl). The daemon's
// hookEventsWatcher polls these files on a 200 ms ticker, parses new
// lines, and feeds them through hookevents.Ingester → eventQueue → IPC
// fan-out. Truncated at daemon start (no replay of stale events); files
// for destroyed panes are unlinked.
func EventsDir() string {
	return filepath.Join(QuilDir(), "events")
}

// SessionsDir returns the directory where the Claude Code SessionStart hook
// writes per-pane session id files (<paneID>.id). Read on daemon restore
// by resumeTemplateFor so panes reattach to the latest session id after
// /clear, compaction, or /resume rotations.
func SessionsDir() string {
	return filepath.Join(QuilDir(), "sessions")
}
