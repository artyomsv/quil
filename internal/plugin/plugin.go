package plugin

import "regexp"

// StalePlugin holds data for a plugin whose on-disk schema_version is lower
// than the embedded default. The TUI uses this to show a migration dialog.
type StalePlugin struct {
	Name        string // plugin name (e.g., "claude-code")
	FilePath    string // absolute path to the user's TOML file
	UserData    []byte // current content of the user's file
	DefaultData []byte // embedded default content (newer schema)
}

// PanePlugin defines a pane type with its command, persistence strategy,
// and optional error handlers.
type PanePlugin struct {
	Name                 string
	DisplayName          string
	Category             string
	Description          string
	Homepage             string // optional project/tool URL, shown when the binary is missing
	Command              CommandConfig
	Persistence          PersistenceConfig
	Display              DisplayConfig
	Instances            []InstanceConfig
	ErrorHandlers        []ErrorHandler
	NotificationHandlers []NotificationHandler
	IdleHandlers         []IdleHandler
	Available            bool // set at startup by running detect cmd
}

// CommandConfig describes how to launch the plugin's process.
type CommandConfig struct {
	Cmd              string
	Path             string // optional: full path to binary (overrides PATH lookup)
	Args             []string
	Env              []string
	DetectCmd        string
	ShellIntegration bool
	ArgTemplate      []string    // template args with {field} placeholders, e.g., ["-p", "{port}", "{user}@{host}"]
	FormFields       []FormField // fields for instance creation form (if empty, no instance management)
	PromptsCWD       bool        // if true, create-pane setup dialog prompts for the working directory
	Toggles          []Toggle    // runtime on/off switches rendered as checkboxes in the setup dialog
	// RawKeys lists key strings (in Bubble Tea form, e.g. "shift+tab") that
	// should bypass Quil's global shortcut layer for panes of this plugin and
	// be forwarded directly to the PTY. This lets TUI apps like Claude Code
	// receive shift+tab (mode toggle) which Quil otherwise binds to PrevPane.
	RawKeys []string
	// Discover selects a discovery mode for the pane setup dialog.
	// "" = none (plain directory browser). "git" = the CWD step lists git
	// repo candidates (enclosing repo + 1-level sub-repos of the active
	// pane's CWD) with a "Browse…" escape hatch; only meaningful when
	// PromptsCWD is true. "kube" = a context pick-list lists kube contexts
	// from the kubeconfig and injects --context <name> (CWD-independent).
	Discover string
	// RecordHistory enables per-pane user-input history capture for this
	// plugin. When true, the daemon sets QUIL_RECORD_HISTORY=1 in the pane's
	// PTY env, and the plugin's hook producer appends submitted prompts to
	// <quilDir>/history/<paneID>.jsonl. Meaningful only for plugins with a
	// hook producer (claude-code; opencode is a follow-up).
	RecordHistory bool
	// Sessions enables the pane setup dialog's "resume an existing session"
	// field for this plugin. "" = off (a new pane always starts a fresh
	// session). "claude" = list the Claude Code transcripts recorded for the
	// selected working directory and, when one is chosen, spawn
	// `--resume <id>` instead of the preassign_id strategy's start args.
	//
	// Only meaningful together with PromptsCWD: the list is scoped to the
	// directory chosen in the dialog.
	Sessions string
}

// FormField defines a user-fillable field for creating plugin instances.
type FormField struct {
	Name     string // field key (used in ArgTemplate placeholders)
	Label    string // display label in form
	Required bool   // must be filled before submit
	Default  string // pre-filled value (empty = blank)
}

// Toggle is a boolean runtime flag the user can enable when creating a pane.
// When enabled, ArgsWhenOn is appended to the spawn args (and persisted via
// the pane's InstanceArgs so it survives daemon restarts).
//
// Group gives mutual-exclusion semantics: toggles that share a non-empty
// Group value are rendered as radio buttons and only one member may be ON
// at a time. Enabling one automatically disables the others in the group.
// A toggle with an empty Group behaves as an independent checkbox.
type Toggle struct {
	Name       string   // identifier (stable across renames for future addressability)
	Label      string   // text shown next to the checkbox in the setup dialog
	ArgsWhenOn []string // args appended to the command when this toggle is checked
	Default    bool     // initial checked state
	Group      string   // optional mutual-exclusion group; empty = independent checkbox
}

// PersistenceConfig describes how to restore the pane after daemon restart.
type PersistenceConfig struct {
	Strategy    string   // "none", "cwd_only", "rerun", "session_scrape", "preassign_id"
	StartArgs   []string // template args for fresh start (e.g., ["--session-id", "{session_id}"])
	ResumeArgs  []string
	Scrapers    []ScrapePattern
	GhostBuffer bool // save PTY output to disk for replay on reconnect (default true)

	// RedrawKey is written to the pane's stdin when a client attaches and the
	// pane had no ghost replay to send.
	//
	// Only meaningful with GhostBuffer = false, which is what leaves a
	// reconnecting client with a blank rectangle in front of a live process.
	//
	// Empty (the default) does NOT mean "do nothing": such a pane is given a
	// resize instead, so its program repaints via SIGWINCH. Declaring a key
	// therefore means "my program IGNORES SIGWINCH, send this instead", and
	// suppresses the resize. Declare one only if that is true — adding a key to
	// a program that does repaint on SIGWINCH replaces a working mechanism with
	// a broken one, which is exactly how opencode came to be blank on every
	// reattach.
	//
	// It is opt-IN because a key is INPUT, not a signal: the plugin author is
	// asserting that their program treats the byte as "repaint" and that nothing
	// else is reading its stdin. A pane running `cat > file` or a password
	// prompt would receive it as data. SIGWINCH carries no such risk, which is
	// why it needs no opt-in and is the default.
	//
	// Neither trigger is universal, and the measurements are counter-intuitive
	// in both directions: vim repaints with ~5 KB on a resize; claude-code emits
	// 0 bytes there (it re-lays-out but only paints on its own render tick,
	// which input drives) and ~3.8 KB on Ctrl+L; opencode is the exact inverse
	// of claude-code, ~8 KB on a resize and nothing on Ctrl+L. Measure before
	// declaring.
	RedrawKey string
}

// ScrapePattern extracts named values from PTY output via regex.
type ScrapePattern struct {
	Name     string
	Pattern  string
	compiled *regexp.Regexp
}

// Compile pre-compiles the regex pattern. Must be called before concurrent use.
// Returns an error if the pattern is invalid (instead of panicking).
func (sp *ScrapePattern) Compile() error {
	re, err := regexp.Compile(sp.Pattern)
	if err != nil {
		return err
	}
	sp.compiled = re
	return nil
}

// Compiled returns the compiled regex, or nil if compilation failed.
func (sp *ScrapePattern) Compiled() *regexp.Regexp {
	return sp.compiled
}

// InstanceConfig is a pre-configured variant of a plugin (e.g., a specific SSH host).
type InstanceConfig struct {
	Name        string
	DisplayName string
	Args        []string
	Env         []string
}

// DisplayConfig controls visual appearance of the pane.
type DisplayConfig struct {
	BorderColor string
	DialogWidth int // width for plugin dialogs (0 = default 50)
	// WideCanvas keeps the pane's PTY/emulator sized to the full window
	// regardless of its layout rect; small rects render a soft-wrapped
	// preview TUI-side. For AI tools (claude-code, opencode) whose
	// transcript is immutable hard-wrapped text: rendering wide once and
	// wrapping down beats resizing, which garbles history. Default false.
	WideCanvas bool
	// MinNativeCols is the inner-width threshold (columns) at or above which
	// a wide_canvas pane renders natively (real pane-width PTY) instead of
	// the window canvas + preview. 0 means "use the built-in default" (80).
	MinNativeCols int
}

// ErrorHandler matches PTY output patterns and triggers help dialogs.
type ErrorHandler struct {
	Pattern  string
	Title    string
	Message  string
	Action   string // "dialog" | "log"
	compiled *regexp.Regexp
}

// Compile pre-compiles the regex pattern. Must be called before concurrent use.
// Returns an error if the pattern is invalid (instead of panicking).
func (eh *ErrorHandler) Compile() error {
	re, err := regexp.Compile(eh.Pattern)
	if err != nil {
		return err
	}
	eh.compiled = re
	return nil
}

// Compiled returns the compiled regex, or nil if compilation failed.
func (eh *ErrorHandler) Compiled() *regexp.Regexp {
	return eh.compiled
}

// NotificationHandler matches PTY output patterns and triggers notification events.
type NotificationHandler struct {
	Pattern  string
	Title    string
	Severity string // "info", "warning", "error"
	compiled *regexp.Regexp
}

// Compile pre-compiles the regex pattern.
func (nh *NotificationHandler) Compile() error {
	re, err := regexp.Compile(nh.Pattern)
	if err != nil {
		return err
	}
	nh.compiled = re
	return nil
}

// Compiled returns the compiled regex, or nil if compilation failed.
func (nh *NotificationHandler) Compiled() *regexp.Regexp {
	return nh.compiled
}

// IdleHandler matches patterns against pane content when the pane goes idle.
// Unlike NotificationHandler (checked on every output chunk), IdleHandler runs
// only at idle time against the last few lines — much less noisy.
type IdleHandler struct {
	Pattern  string
	Title    string
	Severity string // "info", "warning", "error"
	compiled *regexp.Regexp
}

// Compile pre-compiles the regex pattern.
func (ih *IdleHandler) Compile() error {
	re, err := regexp.Compile(ih.Pattern)
	if err != nil {
		return err
	}
	ih.compiled = re
	return nil
}

// Compiled returns the compiled regex, or nil if compilation failed.
func (ih *IdleHandler) Compiled() *regexp.Regexp {
	return ih.compiled
}

// ClaudeSessionSource is the Command.Sessions value naming Claude Code's
// transcript store. It implies the whole Claude protocol: the preassigned
// session id, the SessionStart hook that tracks /clear, /resume and compaction
// rotation, and transcripts under the Claude config dir.
const ClaudeSessionSource = "claude"

// UsesClaudeSessions reports whether this plugin's sessions are Claude Code
// sessions. Every site that once compared Name against "claude-code" asks this
// instead, so a renamed plugin -- or any future Claude-compatible tool -- works
// without a code change.
//
// Nil-receiver safe: callers resolve through Registry.Get, which returns nil for
// an unknown or failed-to-load plugin. False is the right answer there, because
// a plugin that failed to load has already been degraded to a plain terminal at
// the spawn site.
func (p *PanePlugin) UsesClaudeSessions() bool {
	return p != nil && p.Command.Sessions == ClaudeSessionSource
}

// RestoresOwnHistory reports whether this plugin's resume strategy hands the
// respawned child a session id, so the child paints its own transcript back
// instead of depending on Quil's ghost replay.
//
// The resume-strategy question, not a plugin-name list: these two strategies are
// exactly the ones resolveSpawnArgs expands into `--resume <id>` /
// `--session <id>`. `rerun` re-runs a command that starts from nothing and
// `cwd_only` respawns a shell that will not reprint a word of its scrollback --
// both of those still need the replay.
func (p *PanePlugin) RestoresOwnHistory() bool {
	if p == nil {
		return false
	}
	switch p.Persistence.Strategy {
	case "preassign_id", "session_scrape":
		return true
	}
	return false
}
