package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
	Overlay      OverlayConfig      `toml:"overlay"`
	Update       UpdateConfig       `toml:"update"`
	Remote       RemoteConfig       `toml:"remote"`
	// Destinations are the ADDITIONAL daemons this client attaches to beside
	// the local one, each contributing its projects to the same sidebar. A
	// slice rather than a map because order is meaningful — it is the order the
	// projects appear in — and TOML spells a list of tables as [[destinations]].
	//
	// `quil --remote <host>` ignores this list entirely: that mode is "drive
	// THAT machine", and quietly attaching the configured extras to it would
	// make one flag mean two different things.
	Destinations []Destination `toml:"destinations"`
}

// Destination names one remote daemon to attach at launch.
type Destination struct {
	// Name labels the host in launch diagnostics. Optional; Dest is used when
	// it is empty. It exists because Dest is an ssh destination — often
	// `user@10.0.0.4` or an ssh_config alias — and the message a user reads
	// when a host is unreachable at launch should be able to say "gpu box".
	Name string `toml:"name"`
	// Dest is passed to ssh VERBATIM, exactly like --remote: an ssh_config Host
	// alias keeps its HostName/Port/User/ProxyJump, which is the whole reason
	// the transport does not parse it. It is also the routing key — the key a
	// project's Dest, its reconnect state and its link banner all carry.
	Dest string `toml:"dest"`
}

// Label returns the name to show for a destination, falling back to the ssh
// destination itself.
func (d Destination) Label() string {
	if d.Name != "" {
		return d.Name
	}
	return d.Dest
}

// RemoteConfig holds per-destination settings for `quil --remote`, keyed by the
// destination string exactly as the user types it — an ssh_config Host alias,
// a hostname, or user@host.
type RemoteConfig struct {
	Hosts map[string]RemoteHost `toml:"hosts"`
}

// RemoteHost pins how to reach quil on one remote destination.
type RemoteHost struct {
	// Binary is the absolute path to quil on that host, as resolved by
	// `quil remote setup`. It is used verbatim as the ssh remote command,
	// which is what makes attaching work when the non-interactive PATH cannot
	// see the install directory — the normal case for ~/.local/bin on Debian
	// and Ubuntu, where ~/.bashrc returns before reaching any PATH line.
	Binary string `toml:"binary"`
}

// SetRemoteBinary records where quil lives on dest, creating the map on first
// use so callers need not care whether the config predates this section.
func (c *Config) SetRemoteBinary(dest, binary string) {
	if c.Remote.Hosts == nil {
		c.Remote.Hosts = make(map[string]RemoteHost)
	}
	c.Remote.Hosts[dest] = RemoteHost{Binary: binary}
}

// RemoteBinary returns the recorded quil path for dest, or "" when none has
// been recorded — in which case the caller falls back to a bare `quil`, which
// works only if the remote's non-interactive PATH can see it.
func (c *Config) RemoteBinary(dest string) string {
	return c.Remote.Hosts[dest].Binary
}

// ClearRemoteBinary forgets the recorded quil path for dest.
//
// Called only when the host probe has ANSWERED and reported no quil at all: the
// record is then known-false, and keeping it means the next launch runs the
// same missing path and fails identically. A probe that errored is not
// evidence — see healRemoteRecord in cmd/quil.
//
// Deleting from a nil map is a no-op in Go, so a config predating the [remote]
// section needs no special case. That matters here rather than being a
// curiosity: this runs on the failure path, where a panic would replace a
// diagnosable error with a crash.
func (c *Config) ClearRemoteBinary(dest string) {
	delete(c.Remote.Hosts, dest)
}

type NotificationConfig struct {
	SidebarWidth int                     `toml:"sidebar_width"` // default 30
	MaxEvents    int                     `toml:"max_events"`    // default 200
	Hooks        HookNotificationsConfig `toml:"hooks"`
	Desktop      DesktopConfig           `toml:"desktop"`
}

// DesktopConfig controls operating-system toasts raised from the project
// sidebar's attention model.
//
// Enabled defaults TRUE, and that does not make registration implicit: Windows
// toasts need a Start Menu shortcut and an HKCU class key, both outside
// QUIL_HOME, and nothing writes them as a side effect of a config flag. The
// flag says "I want these"; `quil notify setup` is still the gate.
//
// The consequence is that enabled-but-unregistered is the DEFAULT state on a
// fresh Windows install rather than an edge case, which is why the Settings
// row reports registration state instead of this bool.
type DesktopConfig struct {
	Enabled bool `toml:"enabled"`
	Blocked bool `toml:"blocked"` // a pane parked waiting on the user
	Done    bool `toml:"done"`    // a turn finished while the user was away

	// Cooldown is per PANE and shared by both kinds — a floor against a
	// pathological loop, NOT a batching window.
	//
	// It was 30 s and that was wrong. Three mechanisms already prevent
	// duplicates: a toast only fires on a state CHANGE, a pane that already has
	// a toast showing cannot get a second one, and the pane you are looking at
	// never toasts at all. So the only thing a long cooldown suppresses is a
	// genuinely new event after you have already dealt with the previous one.
	//
	// Measured in real use: four completed turns 12-23 s apart produced one
	// toast, because every later turn landed inside the window. That reads as
	// the feature being unreliable, which is worse than the duplication it was
	// guarding against.
	Cooldown string `toml:"cooldown"`

	// There is deliberately NO require_blur key.
	//
	// An earlier version had one, gating toasts on the whole terminal being
	// unfocused. It was removed rather than re-defaulted because it was a
	// silent footgun: config.Save writes the entire struct, so anyone who ran
	// that build has `require_blur = true` on disk, and Load would keep
	// honouring it — disabling the feature completely with no error, no log
	// and no clue. A key whose wrong value is invisible is worse than no key.
	//
	// Its original purpose is also gone. It existed as a fallback for
	// terminals with no focus reporting (DEC 1004), where termFocused would
	// stay true forever and suppress everything. Under the per-pane rule that
	// case is already fine: only the pane you are actually looking at is
	// suppressed, so such a terminal still toasts for every other pane.
	//
	// BurntSushi/toml ignores unknown keys, so a config carrying the old line
	// loads cleanly and the value simply stops meaning anything.
}

// DefaultDesktopCooldown is deliberately SHORT: it exists only to stop a
// runaway agent from storming, not to batch notifications. See Cooldown.
const DefaultDesktopCooldown = 5 * time.Second

// CooldownDuration parses Cooldown, falling back to the default for an empty,
// malformed or non-positive value. A garbage value must not disable the rate
// limit — that would turn a typo into a toast storm.
func (d DesktopConfig) CooldownDuration() time.Duration {
	v, err := time.ParseDuration(d.Cooldown)
	if err != nil || v <= 0 {
		return DefaultDesktopCooldown
	}
	return v
}

// OverlayConfig bounds how long an overlay pane (the Alt+G lazygit pane) may
// stay alive while hidden, and how many may live at once.
//
// An overlay is only HIDDEN when its tab is closed out of, never destroyed, so
// before these bounds existed one lazygit process survived per tab that had
// ever opened one — measured at 116 MB each, and they outlived every TUI
// restart because the daemon keeps them.
type OverlayConfig struct {
	// IdleTimeoutMinutes destroys an overlay hidden for at least this long.
	// 0 disables idle eviction.
	IdleTimeoutMinutes int `toml:"idle_timeout_minutes"`
	// MaxLive caps live overlays across ALL tabs; opening one past the cap
	// evicts the least recently shown. 0 disables the cap.
	MaxLive int `toml:"max_live"`
}

// UpdateConfig controls the auto-update pipeline. Check gates the daily
// GitHub release check (one unauthenticated GET to api.github.com); Auto
// gates background download + staging of a newer release. auto = false
// degrades to notify-only. Dev builds (version.IsRelease() == false) skip
// the pipeline regardless of these settings.
type UpdateConfig struct {
	Check bool `toml:"check"`
	Auto  bool `toml:"auto"`
}

// HookNotificationsConfig controls which hook-driven events get spool-emitted
// per source. Tier values are "default" / "verbose" / "off". Daemon passes
// the resolved value to the hook scripts via the QUIL_HOOK_MODE env var at
// pane spawn so the script can branch on it (default → forward the v1 tier
// plus the throttled PreToolUse heartbeat the work indicator needs for turns
// no user prompt began; verbose → reserved for the full per-tool-call
// PreToolUse/PostToolUse stream; off → no spool writes at all). Unset =
// "default" downstream.
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
	// SidebarOpen/SidebarWidth control the project sidebar (a reserved left
	// column listing projects and the active project's panes). These are
	// screen properties, not session ones — client config, never
	// workspace.json — so a workspace saved with the sidebar open doesn't
	// fight a narrower terminal on restore.
	SidebarOpen  bool `toml:"sidebar_open"`
	SidebarWidth int  `toml:"sidebar_width"`
	// ScrollbackLines is the per-pane VT scrollback depth. 0 (unset) selects the
	// ADAPTIVE default, which spends a workspace-wide line budget across the
	// panes that exist: ten or fewer are unchanged at the historical depth, more
	// get proportionally less, and a floor keeps every pane usable. A non-zero
	// value here always wins and is never adapted.
	//
	// This multiplies by pane count — every pane holds its own emulator whether
	// visible or not — so it is the knob for a workspace with dozens of panes on
	// a memory-tight machine. Measured: 37 panes at the old flat 10 000 lines put
	// the TUI at 1.13 GB resident. internal/tui owns the policy
	// (adaptiveScrollbackLines) and the constants; config cannot import tui.
	ScrollbackLines int `toml:"scrollback_lines"`
	// UnfocusedDimEnabled is the dim's off switch, kept SEPARATE from the level
	// below rather than folded into it as `0 = off`. That was the original
	// shape, and it cost the user their setting: with one key, switching the
	// dim off has to write 0 over the level, so switching it back on can only
	// restore the DEFAULT — a customised 0.35 is destroyed by an off/on round
	// trip through the Settings dialog or the command palette. With two keys
	// the level is preserved across the switch, which is the whole point of
	// exposing an off switch in the UI at all.
	//
	// Absent from a config.toml written before this key existed, so it MUST
	// default to true: Load starts from Default() and lets the decoder
	// overwrite only the keys the file names, which is what stops the upgrade
	// from silently switching the dim off for every install that has ever
	// saved a config. A legacy `unfocused_dim = 0` still reads as off, through
	// the level arm of UnfocusedDimAmount. Pinned by
	// TestLoad_AbsentEnabledKeyKeepsTheDimOn and
	// TestLoad_LegacyZeroStillDisablesTheDim.
	UnfocusedDimEnabled bool `toml:"unfocused_dim_enabled"`
	// UnfocusedDim is how far the frame fades toward the terminal background
	// while the terminal window does not have OS focus, so typing into a window
	// that only looks focused is visibly wrong before the first keystroke
	// lands. See UnfocusedDimLevel for the accepted range.
	//
	// It needs no terminal-capability key because the mechanism is
	// self-gating: the dim only ever applies after a DEC 1004 blur, which a
	// terminal without focus reporting never sends.
	UnfocusedDim float64 `toml:"unfocused_dim"`
}

// DefaultUnfocusedDim is the share of the way toward the terminal background
// an unfocused frame travels. Chosen to read as unmistakably muted at a glance
// while leaving a parked agent's output legible — the point is to notice the
// window is not focused, not to stop being able to read it.
const DefaultUnfocusedDim = 0.6

// MaxUnfocusedDim is short of 1.0 deliberately: a full blend renders the frame
// as an empty rectangle, which is indistinguishable from a crashed TUI.
const MaxUnfocusedDim = 0.9

// UnfocusedDimLevel clamps UnfocusedDim into the usable range, IGNORING the
// on/off switch. A negative value (which would brighten toward the foreground)
// and anything past MaxUnfocusedDim both read as "off by one keystroke" typos
// rather than intent, so they are clamped rather than honoured or rejected.
//
// NaN is named explicitly because it defeats an ordinary clamp: TOML accepts
// the literal `nan`, and NaN compares false against BOTH bounds, so it would
// fall through the default arm and reach the blend, where uint8(NaN) is
// undefined. The frame happens to survive that today only because the caller
// also gates on `amount > 0` — but a value this function exists to make safe
// must not depend on a downstream check a refactor could drop.
//
// Ignoring the switch is what the Settings dialog and the command palette need:
// both show the level the dim WOULD use while reporting it as off, and the
// dialog's own rule is that it never displays a number the renderer would not
// use. Consulting the switch here would make a switched-off dim report a level
// of 0 and destroy the setting on the next edit. Use UnfocusedDimAmount to
// decide whether to blend at all.
func (u UIConfig) UnfocusedDimLevel() float64 {
	switch {
	case math.IsNaN(u.UnfocusedDim), u.UnfocusedDim <= 0:
		return 0
	case u.UnfocusedDim > MaxUnfocusedDim:
		return MaxUnfocusedDim
	default:
		return u.UnfocusedDim
	}
}

// UnfocusedDimAmount is what the renderer blends with: the clamped level, or 0
// when the dim is switched off. Zero means "do not dim", however it arose —
// the switch being off, a legacy `unfocused_dim = 0`, or a value the clamp
// rejected — so callers need only the one `> 0` test they already make.
func (u UIConfig) UnfocusedDimAmount() float64 {
	if !u.UnfocusedDimEnabled {
		return 0
	}
	return u.UnfocusedDimLevel()
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
	MutePane string `toml:"mute_pane"`
	// RestartPane kills and respawns the active pane's process in place
	// (same pane, same plugin resume strategy — AI panes resume their
	// session via the recorded session id). Recovery for a child that
	// stopped reading stdin ("Pane not accepting input").
	RestartPane string `toml:"restart_pane"`
	GoBack      string `toml:"go_back"`
	NotesToggle string `toml:"notes_toggle"`
	// Redraw forces a full screen repaint (tea.ClearScreen). Recovery key
	// for rendering artifacts left behind by cell-diff drift — width
	// disagreements between Quil and the host terminal (most common on
	// Windows) scramble characters until something repaints everything.
	Redraw string `toml:"redraw"`
	// ToggleEager flips the active pane's eager-restore flag. Eager panes
	// respawn immediately on daemon restart (vs the default lazy deferral) and
	// show a ● marker on their tab.
	ToggleEager string `toml:"toggle_eager"`
	// CommandHistory opens the per-pane input-history modal (list of submitted
	// prompts; Enter opens one full-text read-only). Only meaningful for panes
	// whose plugin sets record_history (claude-code).
	CommandHistory string `toml:"command_history"`
	// ToggleLazygit opens/hides the per-tab lazygit overlay for the git
	// repo resolved from the active pane's CWD.
	ToggleLazygit string `toml:"toggle_lazygit"`
	// ToggleHunk opens/hides the per-tab hunk overlay for the git repo
	// resolved from the active pane's CWD. A tab has ONE overlay slot, so
	// this and ToggleLazygit replace each other rather than stacking.
	//
	// Defaults to alt+d ("diff"), NOT the more obvious alt+h: Alt+H is left
	// unbound at the global level on purpose so it reaches the PTY (see the
	// PaneLeft comment above), and it is also what vim-style setups rebind to
	// pane-left. Users who want it anyway can set toggle_hunk = "alt+h".
	ToggleHunk string `toml:"toggle_hunk"`
	// ToggleWrap switches the active wide-canvas pane's preview between
	// left-edge crop (default) and soft-wrap. Only meaningful for panes
	// whose plugin sets [display] wide_canvas; no-op elsewhere.
	ToggleWrap string `toml:"toggle_wrap"`
	// CommandPalette opens the fuzzy command palette — a modal, centered
	// launcher for every action plus jump-to-tab/pane. Default is alt+shift+p:
	// ctrl+shift+p is intercepted by many terminals' own command palette
	// (Windows Terminal, VS Code's terminal) before it reaches Quil, so it is
	// deliberately NOT a default. Add it back in config.toml if your terminal
	// leaves it free (e.g. `command_palette = "ctrl+shift+p,alt+shift+p"`).
	CommandPalette string `toml:"command_palette"`
	// SidebarToggle collapses / expands the PROJECT sidebar (the reserved
	// left column, not the notification overlay on the right — that one is
	// NotificationToggle). Unlike the overlay this reserves real layout
	// width, so toggling it resizes every pane's PTY.
	SidebarToggle string `toml:"sidebar_toggle"`
	// ProjectPicker opens the fuzzy project picker, ProjectToggle bounces
	// between the two most recent projects, AttentionQueue opens the
	// cross-project list of panes blocked on the user, and NewProject opens
	// the create-project dialog.
	//
	// The whole group deliberately avoids alt+w / alt+a / alt+shift+p
	// (CloseTab, QuickActions, CommandPalette). alt+p and alt+o are plain
	// Alt-letter keys because no AI tool binds them; the rest take the
	// Alt+Shift layer for the same reason the split keys do.
	ProjectPicker string `toml:"project_picker"`
	ProjectToggle string `toml:"project_toggle"`
	// ProjectNext/ProjectPrev cycle through the project list in order, where
	// ProjectToggle bounces between the last two. Bound to alt+shift+arrows
	// so they read as the project-level echo of alt+arrows' pane navigation.
	// Deliberately NOT alt+[ / alt+] : those send ESC [ and ESC ], and ESC [
	// is the CSI introducer, so the terminal cannot tell the keypress from
	// the start of an escape sequence. Same reason alt+O (SS3) is avoided,
	// and alt+b/f/d are left to readline's word operations.
	ProjectNext    string `toml:"project_next"`
	ProjectPrev    string `toml:"project_prev"`
	AttentionQueue string `toml:"attention_queue"`
	NewProject     string `toml:"new_project"`
	DestroyProject string `toml:"destroy_project"`
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
			TabDock:             "top",
			Theme:               "default",
			MouseScrollLines:    3,
			PageScrollLines:     0,  // 0 = half-page (dynamic) — used by terminal pane scrollback
			LogViewerPageLines:  40, // Alt+Up / Alt+Down jump in F1 → log viewer
			ShowDisclaimer:      true,
			SidebarOpen:         false, // closed by default — existing installs keep their pane geometry unchanged
			SidebarWidth:        22,    // internal/tui.defaultSidebarWidth — config can't import tui, kept in sync by TestUIDefault_SidebarWidthMatchesTUIDefault
			UnfocusedDimEnabled: true,  // absent from every pre-existing config.toml; see the field comment
			UnfocusedDim:        DefaultUnfocusedDim,
		},
		MCP: MCPConfig{
			HighlightDuration: "10s",
		},
		Notification: NotificationConfig{
			SidebarWidth: 30,
			MaxEvents:    200,
			Desktop: DesktopConfig{
				Enabled:  true,
				Blocked:  true,
				Done:     true,
				Cooldown: "5s",
			},
			Hooks: HookNotificationsConfig{
				Claude:   "default",
				OpenCode: "default",
			},
		},
		Overlay: OverlayConfig{
			IdleTimeoutMinutes: 5,
			MaxLive:            5,
		},
		Update: UpdateConfig{
			Check: true,
			Auto:  true,
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
			RenamePane:     "alt+f2,alt+shift+r",
			CycleTabColor:  "alt+c",
			ScrollPageUp:   "alt+pgup",
			ScrollPageDown: "alt+pgdown",
			Paste:          "ctrl+v",
			JSONTransform:  "ctrl+j",
			// alt+a (NOT the historical ctrl+a placeholder): ctrl+a is
			// readline beginning-of-line in every shell and the common tmux
			// prefix — stealing it from the PTY would be a regression. The
			// Alt layer matches the other pane-level shortcuts.
			QuickActions:       "alt+a",
			FocusPane:          "ctrl+e",
			NotificationToggle: "alt+n",
			NotificationFocus:  "f3",
			MutePane:           "alt+m",
			RestartPane:        "alt+r",
			GoBack:             "alt+backspace",
			NotesToggle:        "alt+e",
			// Mnemonic: Ctrl+L clears/redraws a shell; the Alt+Shift layer
			// keeps plain Ctrl+L flowing to the PTY.
			Redraw:         "alt+shift+l",
			ToggleEager:    "alt+shift+e",
			CommandHistory: "alt+shift+i",
			ToggleLazygit:  "alt+g",
			ToggleHunk:     "alt+d",
			// Mnemonic: W for wrap; the Alt+Shift layer dodges AI-tool
			// Alt-letter bindings (same reasoning as the split keys).
			ToggleWrap: "alt+shift+w",
			// alt+shift+p only: ctrl+shift+p is grabbed by many terminals' own
			// command palette before Quil sees it (Windows Terminal, VS Code).
			CommandPalette: "alt+shift+p",
			SidebarToggle:  "alt+shift+s",
			ProjectPicker:  "alt+p",
			ProjectToggle:  "alt+o",
			ProjectNext:    "alt+shift+right",
			ProjectPrev:    "alt+shift+left",
			AttentionQueue: "alt+shift+a",
			NewProject:     "alt+shift+n",
			DestroyProject: "alt+shift+x",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, err
	}
	// Legacy quick_actions migration: ctrl+a was the M1 placeholder value —
	// it was never wired to any handler, so it was inert on every config
	// that predates this release. But Save serializes the WHOLE
	// KeybindingsConfig struct (not just fields the user touched), so any
	// config.toml that was ever saved by an old build round-trips
	// quick_actions = "ctrl+a" back onto disk. Left alone, Load would
	// resurrect that dead value and the new context-menu binding would
	// hijack ctrl+a — readline beginning-of-line — out from under every
	// shell pane. Safe to force-migrate because no legitimate "ctrl+a
	// opens the menu" customization can exist on disk; the binding did
	// nothing until this release.
	//
	// This patch is in-memory only — Load never writes to disk, so the
	// legacy value persists on disk until an unrelated Save. The migration
	// (and its log line) therefore re-fires on every launch, deliberately:
	// a startup write from every process that loads config (TUI, daemon,
	// MCP bridge) would race the same file.
	if cfg.Keybindings.QuickActions == "ctrl+a" {
		cfg.Keybindings.QuickActions = "alt+a"
		log.Printf("config: migrated quick_actions ctrl+a -> alt+a (legacy placeholder; ctrl+a stays with the shell)")
	}
	return cfg, nil
}

// Save writes the config to disk atomically (write .tmp then rename).
// Mutate applies fn to the config ON DISK — load, change, save — and is the
// only correct way to write one section of a config another writer also owns.
//
// Save serialises the WHOLE struct, so saving a Config that was loaded at
// launch silently reverts every key written since. That is not hypothetical
// here: a remote install records the absolute path it installed to under
// [remote.hosts.<dest>], and the very next thing the TUI does is record the new
// destination — which, done from the launch-time snapshot, erased the path that
// makes attaching work at all. The symptom was a host that installed
// successfully and then offered to install again on the next launch, forever.
//
// A missing file is not an error: Load returns the defaults for one, which is
// exactly what a first write should be based on.
func Mutate(path string, fn func(*Config)) error {
	cfg, err := Load(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load config: %w", err)
	}
	fn(&cfg)
	return Save(path, cfg)
}

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

// DefaultQuilDir returns the production default data dir (~/.quil),
// ignoring QUIL_HOME. Used by dev builds to detect an inherited
// production-pointing QUIL_HOME.
func DefaultQuilDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".quil")
}

// IsDefaultQuilDir reports whether dir resolves to the production default
// data dir. Case-insensitive on Windows.
func IsDefaultQuilDir(dir string) bool {
	def := DefaultQuilDir()
	if def == "" || dir == "" {
		return false
	}
	a, b := filepath.Clean(dir), filepath.Clean(def)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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

// RecentCWDsPath returns the file storing the last-used working directories
// offered as a quick pick in the pane setup dialog. TUI-owned, single writer.
//
// dest scopes the file to one remote destination. Empty — the local case —
// keeps the historical name exactly, so existing installs need no migration.
// Without the scoping, one flat list mixed laptop and server directories: after
// a remote session the local picker offered paths that exist only on the server,
// and vice versa.
func RecentCWDsPath(dest string) string {
	if dest == "" {
		return filepath.Join(QuilDir(), "recent-cwds.json")
	}
	return filepath.Join(QuilDir(), "recent-cwds-"+destFileKey(dest)+".json")
}

// destFileKey turns an ssh destination into a safe, stable filename component.
//
// The destination is user input that reaches a path, so every character outside
// [A-Za-z0-9_-] is replaced rather than escaped — "../../etc/passwd" and
// "host:22" must both produce an ordinary basename. '.' is deliberately NOT in
// the passthrough set even though it is filename-safe on its own: a destination
// with consecutive dots ("../../etc/passwd") would otherwise survive as
// "..-..-etc-passwd", which still contains ".." and reads as a traversal
// marker even though it can't actually traverse (no separators reach the
// basename). Collapsing alone is not enough to keep distinct destinations
// distinct either: "a/b" and "a-b" would then share a file, so a short digest
// of the ORIGINAL string is appended and is what actually does that job. The
// readable half is kept only so the file can be recognised by eye.
func destFileKey(dest string) string {
	const maxReadable = 32
	var b strings.Builder
	for _, r := range dest {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= maxReadable {
			break
		}
	}
	sum := sha256.Sum256([]byte(dest))
	return b.String() + "-" + hex.EncodeToString(sum[:4])
}

// RemoteProjectsPath is where the client remembers one destination's project
// list, so a host unreachable at launch can still be shown by name.
//
// Keyed like RecentCWDsPath, and for the same two reasons: the destination is
// user input that reaches a filename, and two destinations that collapse to the
// same readable component must not share a file.
func RemoteProjectsPath(dest string) string {
	if dest == "" {
		return filepath.Join(QuilDir(), "remote-projects.json")
	}
	return filepath.Join(QuilDir(), "remote-projects-"+destFileKey(dest)+".json")
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

// UpdateDir returns the root directory of the auto-update pipeline:
// staged binaries, the daemon-owned state.json, and the TUI-owned
// notified.json all live under it.
func UpdateDir() string {
	return filepath.Join(QuilDir(), "update")
}

// UpdateStagingRoot returns the directory that holds one subdirectory per
// staged release version.
func UpdateStagingRoot() string {
	return filepath.Join(UpdateDir(), "staged")
}

// UpdateStagingDir returns the directory a given release version is staged
// into. The stager writes manifest.json into it LAST — its presence is the
// atomic "staging complete" marker.
func UpdateStagingDir(version string) string {
	return filepath.Join(UpdateStagingRoot(), version)
}

// UpdateStatePath is the daemon-owned check/stage status file. The TUI
// never writes it (single-writer-per-file rule).
func UpdateStatePath() string {
	return filepath.Join(UpdateDir(), "state.json")
}

// UpdateNotifiedPath is the TUI-owned once-per-version startup-dialog
// marker. The daemon never writes it.
func UpdateNotifiedPath() string {
	return filepath.Join(UpdateDir(), "notified.json")
}

// LastRunPath is the TUI-owned marker recording the version that last ran on
// this machine, which is what the post-upgrade What's New dialog compares
// against. Distinct from UpdateNotifiedPath, which records a version the user
// was TOLD ABOUT: conflating them would let dismissing an update offer suppress
// the what's-new for a version that was never installed.
func LastRunPath() string {
	return filepath.Join(UpdateDir(), "lastrun.json")
}
