package plugin

import "regexp"

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
	ErrorHandlers []ErrorHandler
	Available     bool // set at startup by running detect cmd
}

// CommandConfig describes how to launch the plugin's process.
type CommandConfig struct {
	Cmd              string
	Args             []string
	Env              []string
	DetectCmd        string
	ShellIntegration bool
	ArgTemplate      []string    // template args with {field} placeholders, e.g., ["-p", "{port}", "{user}@{host}"]
	FormFields       []FormField // fields for instance creation form (if empty, no instance management)
}

// FormField defines a user-fillable field for creating plugin instances.
type FormField struct {
	Name     string // field key (used in ArgTemplate placeholders)
	Label    string // display label in form
	Required bool   // must be filled before submit
	Default  string // pre-filled value (empty = blank)
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
