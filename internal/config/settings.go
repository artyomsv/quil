package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/keymap"
)

// The kinds of value a setting holds. The browser picks its input by kind.
const (
	SettingBool     = "bool"
	SettingInt      = "int"
	SettingFloat    = "float"
	SettingDuration = "duration"
	SettingEnum     = "enum"
	SettingString   = "string"
)

// When a change takes effect.
const (
	AppliesNow       = "now"
	AppliesNextStart = "next start"
)

// Limits shared by the TUI setters and the browser's Settings page.
const (
	// MaxScrollbackLines caps ui.scrollback_lines: the setting whose purpose
	// is reducing memory must not exhaust it through a typo.
	MaxScrollbackLines = 1000000
	// MinSidebarWidth is the narrowest the project sidebar may be set.
	MinSidebarWidth = 12
	// SandboxImageMax bounds the default sandbox image name, in runes.
	SandboxImageMax = 255
	// DimLevelDecimals is the display resolution of a dim level.
	DimLevelDecimals = 2
)

// MinUnfocusedDim is the smallest level the Settings row can display
// distinctly (internal/tui/dim.go explains why a smaller one is refused).
var MinUnfocusedDim = math.Pow(10, -DimLevelDecimals)

// SandboxSignInChoices are the sign-in defaults, in display order.
var SandboxSignInChoices = []string{"browser", "shared", "token"}

// LogLevels are the accepted logging.level values.
var LogLevels = []string{"debug", "info", "warn", "error"}

// Setting is one row of the Settings screens: the TUI dialogs and the
// browser's page both call Set, so their checks cannot drift. Set checks
// first and stores only an accepted value; a refused one leaves the config
// unchanged. It holds no live side effect (a TUI closure applies those).
type Setting struct {
	Key     string // the TOML path, e.g. "ui.scrollback_lines"
	Group   string // "general", "notifications" or "sandbox"
	Label   string
	Kind    string
	Choices []string // SettingEnum only
	Applies string
	Get     func(Config) string
	Set     func(*Config, string) error
}

// FormatDimLevel renders a dim level at the row's resolution.
func FormatDimLevel(level float64) string {
	return strconv.FormatFloat(level, 'f', DimLevelDecimals, 64)
}

// ParseDimLevel accepts a typed dim level, ok only for a value the renderer
// would honour verbatim (internal/tui/dim.go has the reasons).
func ParseDimLevel(s string) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(n) || n <= 0 || n < MinUnfocusedDim || n > MaxUnfocusedDim {
		return 0, false
	}
	return n, true
}

// SandboxSignInFields maps a sign-in choice onto (Auth, ClaudeConfig). Token
// is "own": a token pane never enters the shared directory.
func SandboxSignInFields(choice string) (auth, claudeConfig string) {
	switch choice {
	case "shared":
		return string(SandboxAuthBrowser), SandboxClaudeConfigShared
	case "token":
		return string(SandboxAuthToken), SandboxClaudeConfigOwn
	}
	return string(SandboxAuthBrowser), SandboxClaudeConfigOwn
}

func parseBool(v string) (bool, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, errors.New("must be true or false")
	}
	return b, nil
}

func boolRow(key, group, label, applies string, p func(*Config) *bool) Setting {
	return Setting{
		Key: key, Group: group, Label: label, Kind: SettingBool, Applies: applies,
		Get: func(c Config) string { return strconv.FormatBool(*p(&c)) },
		Set: func(c *Config, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			*p(c) = b
			return nil
		},
	}
}

// intRow accepts a whole number >= lo; why is the refusal text.
func intRow(key, group, label, applies string, lo int, why string, p func(*Config) *int) Setting {
	return Setting{
		Key: key, Group: group, Label: label, Kind: SettingInt, Applies: applies,
		Get: func(c Config) string { return strconv.Itoa(*p(&c)) },
		Set: func(c *Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < lo {
				return errors.New(why)
			}
			*p(c) = n
			return nil
		},
	}
}

func enumRow(key, group, label, applies string, choices []string, get func(Config) string, set func(*Config, string)) Setting {
	return Setting{
		Key: key, Group: group, Label: label, Kind: SettingEnum, Choices: choices, Applies: applies,
		Get: get,
		Set: func(c *Config, v string) error {
			for _, ch := range choices {
				if ch == v {
					set(c, v)
					return nil
				}
			}
			return fmt.Errorf("must be one of %s", strings.Join(choices, ", "))
		},
	}
}

func groupRow(name, label string, p func(*EventGroupsConfig) *bool) Setting {
	return boolRow("notification.events."+name, "notifications", label, AppliesNow,
		func(c *Config) *bool { return p(&c.Notification.Events) })
}

// Settings returns every row, in the order the pages show them.
func Settings() []Setting {
	const g, n, sb = "general", "notifications", "sandbox"
	return []Setting{
		{
			Key: "daemon.snapshot_interval", Group: g, Label: "Snapshot interval", Kind: SettingDuration, Applies: AppliesNextStart,
			Get: func(c Config) string { return c.Daemon.SnapshotInterval },
			Set: func(c *Config, v string) error {
				v = strings.TrimSpace(v)
				if d, err := time.ParseDuration(v); err != nil || d <= 0 {
					return errors.New("must be a duration above 0, e.g. 30s")
				}
				c.Daemon.SnapshotInterval = v
				return nil
			},
		},
		// Read by the daemon at start: it gates whether gh is ever run.
		boolRow("daemon.pr_status", g, "Show pull request status", AppliesNextStart, func(c *Config) *bool { return &c.Daemon.PRStatus }),
		boolRow("ghost_buffer.dimmed", g, "Ghost dimmed", AppliesNextStart, func(c *Config) *bool { return &c.GhostBuffer.Dimmed }),
		intRow("ghost_buffer.max_lines", g, "Ghost buffer lines", AppliesNextStart, 1, "must be a whole number above 0",
			func(c *Config) *int { return &c.GhostBuffer.MaxLines }),
		{
			// 0 means "adaptive" in the config layer, so storing it would mean
			// the opposite of what was typed; the top end is clamped.
			Key: "ui.scrollback_lines", Group: g, Label: "Scrollback lines", Kind: SettingInt, Applies: AppliesNextStart,
			Get: func(c Config) string { return strconv.Itoa(c.UI.ScrollbackLines) },
			Set: func(c *Config, v string) error {
				k, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil || k <= 0 {
					return errors.New("must be a whole number above 0")
				}
				c.UI.ScrollbackLines = min(k, MaxScrollbackLines)
				return nil
			},
		},
		intRow("ui.mouse_scroll_lines", g, "Mouse scroll lines", AppliesNextStart, 1, "must be a whole number above 0",
			func(c *Config) *int { return &c.UI.MouseScrollLines }),
		intRow("ui.page_scroll_lines", g, "Page scroll lines", AppliesNextStart, 0, "must be a whole number, 0 or more",
			func(c *Config) *int { return &c.UI.PageScrollLines }),
		// The upper bound depends on the terminal width: the TUI checks it.
		intRow("ui.sidebar_width", g, "Sidebar width", AppliesNextStart, MinSidebarWidth,
			fmt.Sprintf("must be a whole number, %d or more", MinSidebarWidth),
			func(c *Config) *int { return &c.UI.SidebarWidth }),
		{
			Key: "ui.unfocused_dim_enabled", Group: g, Label: "Unfocused dim", Kind: SettingBool, Applies: AppliesNextStart,
			Get: func(c Config) string { return strconv.FormatBool(c.UI.UnfocusedDimAmount() > 0) },
			Set: func(c *Config, v string) error {
				on, err := parseBool(v)
				if err != nil {
					return err
				}
				c.UI.UnfocusedDimEnabled = on
				if on && c.UI.UnfocusedDimLevel() <= 0 {
					c.UI.UnfocusedDim = DefaultUnfocusedDim
				}
				return nil
			},
		},
		{
			Key: "ui.unfocused_dim", Group: g, Label: "Unfocused dim level", Kind: SettingFloat, Applies: AppliesNextStart,
			Get: func(c Config) string { return FormatDimLevel(c.UI.UnfocusedDimLevel()) },
			Set: func(c *Config, v string) error {
				f, ok := ParseDimLevel(v)
				if !ok {
					return fmt.Errorf("must be a number from %s to %s", FormatDimLevel(MinUnfocusedDim), FormatDimLevel(MaxUnfocusedDim))
				}
				c.UI.UnfocusedDim = f
				return nil
			},
		},
		// Pushed to a running daemon as overlay_policy (the TUI always, the
		// browser in local mode only), so they apply now.
		intRow("overlay.idle_timeout_minutes", g, "Overlay idle timeout (min)", AppliesNow, 0, "must be a whole number, 0 or more",
			func(c *Config) *int { return &c.Overlay.IdleTimeoutMinutes }),
		intRow("overlay.max_live", g, "Max live overlays", AppliesNow, 0, "must be a whole number, 0 or more",
			func(c *Config) *int { return &c.Overlay.MaxLive }),
		enumRow("logging.level", g, "Log level", AppliesNextStart, LogLevels,
			func(c Config) string { return c.Logging.Level }, func(c *Config, v string) { c.Logging.Level = v }),
		boolRow("ui.show_disclaimer", g, "Show disclaimer", AppliesNextStart, func(c *Config) *bool { return &c.UI.ShowDisclaimer }),
		boolRow("update.check", g, "Update check", AppliesNextStart, func(c *Config) *bool { return &c.Update.Check }),
		boolRow("update.auto", g, "Update auto-download", AppliesNextStart, func(c *Config) *bool { return &c.Update.Auto }),

		boolRow("notification.desktop.enabled", n, "Desktop toasts", AppliesNow, func(c *Config) *bool { return &c.Notification.Desktop.Enabled }),
		boolRow("notification.desktop.blocked", n, "Toast on blocked", AppliesNow, func(c *Config) *bool { return &c.Notification.Desktop.Blocked }),
		boolRow("notification.desktop.done", n, "Toast on done", AppliesNow, func(c *Config) *bool { return &c.Notification.Desktop.Done }),
		groupRow("agent_turn", "Agent turns", func(e *EventGroupsConfig) *bool { return &e.AgentTurn }),
		groupRow("agent_blocked", "Agent blocked", func(e *EventGroupsConfig) *bool { return &e.AgentBlocked }),
		groupRow("agent_subagent", "Agent subagents", func(e *EventGroupsConfig) *bool { return &e.AgentSubagent }),
		groupRow("agent_session", "Agent session", func(e *EventGroupsConfig) *bool { return &e.AgentSession }),
		groupRow("process", "Process", func(e *EventGroupsConfig) *bool { return &e.Process }),
		groupRow("pane", "Pane", func(e *EventGroupsConfig) *bool { return &e.Pane }),
		groupRow("mcp", "MCP", func(e *EventGroupsConfig) *bool { return &e.MCP }),
		groupRow("system", "System", func(e *EventGroupsConfig) *bool { return &e.System }),
		groupRow("commands", "Commands", func(e *EventGroupsConfig) *bool { return &e.Commands }),
		groupRow("idle", "Idle", func(e *EventGroupsConfig) *bool { return &e.Idle }),

		{
			Key: "sandbox.default_image", Group: sb, Label: "Default image", Kind: SettingString, Applies: AppliesNow,
			Get: func(c Config) string { return c.Sandbox.DefaultImage },
			Set: func(c *Config, v string) error {
				v = strings.TrimSpace(v)
				if utf8.RuneCountInString(v) > SandboxImageMax {
					return fmt.Errorf("must be at most %d characters", SandboxImageMax)
				}
				c.Sandbox.DefaultImage = v
				return nil
			},
		},
		enumRow("sandbox.sign_in", sb, "Sign-in", AppliesNow, SandboxSignInChoices,
			func(c Config) string { return c.Sandbox.DefaultSignIn() },
			func(c *Config, v string) {
				auth, cc := SandboxSignInFields(v)
				c.Sandbox.Auth = auth
				c.Sandbox.SharedClaudeConfig = cc == SandboxClaudeConfigShared
			}),
	}
}

// LookupSetting finds the row for key.
func LookupSetting(key string) (Setting, bool) {
	for _, s := range Settings() {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// SetKeyPreset selects a shipped preset. A preset that writes no ${prefix}
// drops the prefix: a stale one would come back with the next switch.
func SetKeyPreset(b *Bindings, name string) error {
	for _, p := range keymap.PresetNames() {
		if p == name {
			b.Preset = name
			if !keymap.PresetUsesPrefix(name) {
				b.Prefix = ""
			}
			return nil
		}
	}
	return errors.New("unknown preset")
}

// SetKeyPrefix sets the prefix chord; "" means the preset's own.
func SetKeyPrefix(b *Bindings, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		b.Prefix = ""
		return nil
	}
	if !keymap.PresetUsesPrefix(b.Preset) {
		return errors.New("this preset uses no prefix")
	}
	canon, err := keymap.ValidatePrefix(v)
	if err != nil {
		return fmt.Errorf("prefix must be one chord: %w", err)
	}
	b.Prefix = canon
	return nil
}
