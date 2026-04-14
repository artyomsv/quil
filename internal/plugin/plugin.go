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
	Name          string
	DisplayName   string
	Category      string
	Description   string
	Command       CommandConfig
	Persistence   PersistenceConfig
	Display       DisplayConfig
	Instances     []InstanceConfig
	ErrorHandlers        []ErrorHandler
	NotificationHandlers []NotificationHandler
	IdleHandlers         []IdleHandler
	Available            bool // set at startup by running detect cmd
}

// CommandConfig describes how to launch the plugin's process.
type CommandConfig struct {
	Cmd              string
	Path             string      // optional: full path to binary (overrides PATH lookup)
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
type Toggle struct {
	Name       string   // identifier (stable across renames for future addressability)
	Label      string   // text shown next to the checkbox in the setup dialog
	ArgsWhenOn []string // args appended to the command when this toggle is checked
	Default    bool     // initial checked state
}

// PersistenceConfig describes how to restore the pane after daemon restart.
type PersistenceConfig struct {
	Strategy    string // "none", "cwd_only", "rerun", "session_scrape", "preassign_id"
	StartArgs   []string // template args for fresh start (e.g., ["--session-id", "{session_id}"])
	ResumeArgs  []string
	Scrapers    []ScrapePattern
	GhostBuffer bool // save PTY output to disk for replay on reconnect (default true)
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
