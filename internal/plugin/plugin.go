package plugin

import (
	"fmt"
	"regexp"
)

// StalePlugin holds data for a plugin whose on-disk schema_version is lower
// than the embedded default. The TUI uses this to show a migration dialog.
type StalePlugin struct {
	Name        string // plugin name (e.g., "claude-code")
	FilePath    string // absolute path to the user's TOML file
	UserData    []byte // current content of the user's file
	DefaultData []byte // embedded default content (newer schema)
	NeedVersion int    // the default's schema_version, the lowest a saved file may carry
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
	Update               *UpdateSpec // nil when the TOML has no [update] table
	Available            bool        // set at startup by running detect cmd
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
	Pattern string
	Title   string
	Message string
	Action  string // "dialog" | "log"
	Fixes   []Fix  // buttons on the dialog; each runs in the pane
	// OfferUpdate makes the handler's dialog carry the [update] table's
	// Update button with the tool's LATEST release, looked up on GitHub when
	// the handler fires — for a tool too old to print its own update notice
	// (a Stripe CLI that fails on a flag newer CLIs require exits before it
	// says a newer version exists). Load-time refused without [update].
	OfferUpdate bool
	compiled    *regexp.Regexp
}

// Fix is one [[error_handlers.fixes]] entry: a button on the handler's dialog
// that runs Run in the pane in place of the pane's normal command, once.
//
// Only Name and Label ever leave the daemon. A client asks for a fix by NAME
// and the daemon looks Run up in its OWN plugin definition, the way toggles
// are resolved — a free-form argv on the wire would let any IPC client run any
// program under the plugin's name. Names are therefore unique per PLUGIN, not
// per handler, and FixNameUpdate is reserved for the [update] table's button.
type Fix struct {
	Name  string
	Label string
	Run   []string // argv; Run[0] is resolved like [command] cmd
	Then  string   // FixThenRestart (default) or FixThenStay: what a zero exit leads to
}

// FixNameUpdate is the reserved fix name for the [update] table's own button.
// A handler fix carrying it fails plugin load, so the two can never collide
// in the dialog or in a plugin_fix_req.
const FixNameUpdate = "update"

// Values of Fix.Then.
const (
	FixThenRestart = "restart" // start the pane's normal command again
	FixThenStay    = "stay"    // leave the pane on the fix's output
)

// UpdateManager is one [[update.managers]] entry: a package manager that owns
// the binary when Match (a regex) matches its symlink-resolved path. Run is
// the manager's own upgrade argv, run in the pane as an ordinary fix.
type UpdateManager struct {
	Name     string
	Match    string
	Run      []string
	compiled *regexp.Regexp
}

// Compile pre-compiles the match pattern. Must be called before concurrent use.
func (um *UpdateManager) Compile() error {
	re, err := regexp.Compile(um.Match)
	if err != nil {
		return err
	}
	um.compiled = re
	return nil
}

// Compiled returns the compiled regex, or nil if compilation failed.
func (um *UpdateManager) Compiled() *regexp.Regexp {
	return um.compiled
}

// UpdateSpec is the [update] table: how Quil learns the tool is outdated and
// how it replaces the binary.
//
// Notice is matched against complete, ANSI-stripped output lines, apart from
// the error handlers; capture group 1 is the offered version. Managers are
// tried first on the resolved binary path; with no match the manual path
// downloads the release asset named by Assets for this platform from the
// GitHub repo, checks it against the Checksums file and swaps Binary in place.
type UpdateSpec struct {
	Notice    string
	GitHub    string            // "owner/repo"
	Binary    string            // file name inside the asset (".exe" appended on Windows)
	Assets    map[string]string // "goos_goarch" → asset name, "{version}" expanded
	Checksums map[string]string // goos → checksums file name in the release
	Managers  []UpdateManager
	notice    *regexp.Regexp
}

// Compile pre-compiles the notice and every manager match. Must be called
// before concurrent use.
//
// Unlike the handler Compile methods this one also REFUSES a notice with no
// capture group: group 1 is the offered version, and a notice without one
// would match forever while the dialog could not say which version it
// installs.
func (u *UpdateSpec) Compile() error {
	re, err := regexp.Compile(u.Notice)
	if err != nil {
		return fmt.Errorf("invalid update notice %q: %w", u.Notice, err)
	}
	if re.NumSubexp() < 1 {
		return fmt.Errorf("update notice %q has no capture group for the version", u.Notice)
	}
	u.notice = re
	for i := range u.Managers {
		if err := u.Managers[i].Compile(); err != nil {
			return fmt.Errorf("update manager %q: invalid match %q: %w", u.Managers[i].Name, u.Managers[i].Match, err)
		}
	}
	return nil
}

// NoticeRe returns the compiled notice regex, or nil if compilation failed.
func (u *UpdateSpec) NoticeRe() *regexp.Regexp {
	return u.notice
}

// FindFix returns the handler fix named name, searching every handler of the
// plugin — a plugin_fix_req carries the name alone, and load-time validation
// keeps it unique across handlers. Nil-receiver safe, like UsesClaudeSessions:
// the daemon resolves through Registry.Get, which returns nil for a plugin
// that failed to load, and "no such fix" is the right answer there.
func (p *PanePlugin) FindFix(name string) (*Fix, bool) {
	if p == nil {
		return nil, false
	}
	for i := range p.ErrorHandlers {
		fixes := p.ErrorHandlers[i].Fixes
		for j := range fixes {
			if fixes[j].Name == name {
				return &fixes[j], true
			}
		}
	}
	return nil, false
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

// ClaudeCodePluginName is the shipped Claude Code plugin. Recognised by name in
// UsesClaudeSessions for backwards compatibility — see there for why.
const ClaudeCodePluginName = "claude-code"

// CodexPluginName is the shipped Codex CLI plugin. Recognised by NAME at the
// spawn, restore and shutdown-refresh sites, the way opencode is: codex speaks
// Claude's hook JSON but not Claude's argv (`resume <id>`, no preassigned id),
// so it must never fall into the UsesClaudeSessions arm — which is also why
// those sites test the codex name BEFORE that capability.
const CodexPluginName = "codex"

// UsesClaudeSessions reports whether this plugin speaks Claude's hook and
// resume protocol: the SessionStart hook that tracks /clear, /resume and
// compaction, the preassigned session id, and session occupancy.
//
// Two ways to qualify, and the name half is NOT vestigial. `Command.Sessions`
// is documented — in this file and in the shipped TOML, which says "Set to \"\"
// to always start a fresh session" — as a *pane setup dialog* affordance:
// whether to offer a resume pick-list. Deriving the protocol from it alone
// would silently turn a documented UI preference into a kill switch for every
// Claude hook, so a user who took that option, or who kept a plugin file from
// before the field existed, would lose notifications, work state, input history
// and per-pane --resume with nothing logged and nothing failing.
//
// So the shipped plugin qualifies by NAME, exactly as the six call sites this
// replaced each did on their own, and any other plugin may opt in by declaring
// the sessions source. The single benefit that survives from consolidating them
// is the one that mattered: the rule lives in one place with its reasoning
// attached, instead of six scattered comparisons where a missed one fails
// silently.
//
// Nil-receiver safe: callers resolve through Registry.Get, which returns nil for
// an unknown or failed-to-load plugin. False is the right answer there, because
// a plugin that failed to load has already been degraded to a plain terminal at
// the spawn site.
func (p *PanePlugin) UsesClaudeSessions() bool {
	if p == nil {
		return false
	}
	return p.Name == ClaudeCodePluginName || p.Command.Sessions == ClaudeSessionSource
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

// UsesClaudeAuth reports whether this plugin authenticates the way Claude Code
// does: a CLAUDE_CODE_OAUTH_TOKEN minted by `claude setup-token`, or a browser
// sign-in inside the container.
//
// By NAME, and narrower than UsesClaudeSessions on purpose. Codex qualifies
// there — it speaks Claude's hook JSON — but its credentials are its own
// (`~/.codex/auth.json`), and opencode's are its own again. Conflating the two
// would run `claude setup-token` and open a browser for a codex pane, forward
// a Claude token to a container that has no use for it, and offer the user a
// sign-in choice that does not describe the agent they picked.
//
// A user's own TOML naming itself "claude-code" is the same override
// UsesClaudeSessions already accepts, and means the same thing: this pane runs
// Claude Code.
func (p *PanePlugin) UsesClaudeAuth() bool {
	return p != nil && UsesClaudeAuthName(p.Name)
}

// UsesClaudeAuthName is the same question for a call site that has only the
// name — the daemon's sandbox prep is handed one rather than the plugin. ONE
// definition, so the spawn gate and the dialog's row cannot disagree about
// which panes the Claude sign-in applies to.
func UsesClaudeAuthName(name string) bool { return name == ClaudeCodePluginName }
