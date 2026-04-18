package plugin

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Registry holds all known plugins (built-in + user TOML).
type Registry struct {
	plugins map[string]*PanePlugin
	mu      sync.RWMutex
}

// NewRegistry creates a registry pre-loaded with built-in plugins.
func NewRegistry() *Registry {
	r := &Registry{
		plugins: make(map[string]*PanePlugin),
	}
	for _, p := range builtinPlugins() {
		compilePatterns(p)
		r.plugins[p.Name] = p
	}
	return r
}

// compilePatterns pre-compiles all regex patterns in a plugin so they
// are safe for concurrent use from PTY output goroutines.
func compilePatterns(p *PanePlugin) {
	for i := range p.Persistence.Scrapers {
		if err := p.Persistence.Scrapers[i].Compile(); err != nil {
			log.Printf("plugin %q: invalid scrape pattern %q: %v", p.Name, p.Persistence.Scrapers[i].Pattern, err)
		}
	}
	for i := range p.ErrorHandlers {
		if err := p.ErrorHandlers[i].Compile(); err != nil {
			log.Printf("plugin %q: invalid error pattern %q: %v", p.Name, p.ErrorHandlers[i].Pattern, err)
		}
	}
	for i := range p.NotificationHandlers {
		if err := p.NotificationHandlers[i].Compile(); err != nil {
			log.Printf("plugin %q: invalid notification pattern %q: %v", p.Name, p.NotificationHandlers[i].Pattern, err)
		}
	}
	for i := range p.IdleHandlers {
		if err := p.IdleHandlers[i].Compile(); err != nil {
			log.Printf("plugin %q: invalid idle pattern %q: %v", p.Name, p.IdleHandlers[i].Pattern, err)
		}
	}
}

// Get returns a plugin by name, or nil if not found.
func (r *Registry) Get(name string) *PanePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.plugins[name]
}

// All returns all registered plugins.
func (r *Registry) All() []*PanePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*PanePlugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		result = append(result, p)
	}
	return result
}

// ByCategory returns plugins grouped by category.
func (r *Registry) ByCategory() map[string][]*PanePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cats := make(map[string][]*PanePlugin)
	for _, p := range r.plugins {
		cats[p.Category] = append(cats[p.Category], p)
	}
	return cats
}

// AvailableByCategory returns only available plugins grouped by category.
func (r *Registry) AvailableByCategory() map[string][]*PanePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cats := make(map[string][]*PanePlugin)
	for _, p := range r.plugins {
		if p.Available {
			cats[p.Category] = append(cats[p.Category], p)
		}
	}
	return cats
}

// CategoryOrder returns the display order for categories.
func CategoryOrder() []struct{ Key, Label string } {
	return []struct{ Key, Label string }{
		{"terminal", "Terminal"},
		{"ai", "AI Assistant"},
		{"tools", "Tools"},
		{"remote", "Remote"},
	}
}

// LoadFromDir loads all *.toml plugin files from dir.
// TOML plugins override built-ins with the same name.
//
// LoadFromDir is also the reload entry point: any plugin currently in the
// registry that no longer has a corresponding TOML file on disk is dropped,
// EXCEPT the Go-built-in "terminal" plugin which always survives. This makes
// "delete a TOML, hit reload" behave the way users expect.
func (r *Registry) LoadFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No plugins dir is fine
		}
		return fmt.Errorf("read plugins dir: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Track which plugin names came from disk this pass so we can prune
	// stragglers below.
	loaded := make(map[string]struct{})

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, err := loadPluginTOML(path)
		if err != nil {
			log.Printf("skip plugin %q: %v", e.Name(), err)
			continue
		}
		compilePatterns(p)
		r.plugins[p.Name] = p
		loaded[p.Name] = struct{}{}
		log.Printf("loaded plugin %q from %q", p.Name, e.Name())
	}

	// Prune in-memory entries whose backing TOML file vanished. The Go
	// built-in "terminal" plugin is always preserved — it has no file on
	// disk and is the fallback for every untyped pane.
	for name := range r.plugins {
		if name == "terminal" {
			continue
		}
		if _, ok := loaded[name]; ok {
			continue
		}
		delete(r.plugins, name)
		log.Printf("plugin %q removed from registry (no backing TOML)", name)
	}

	return nil
}

// DetectAvailability checks whether each plugin's tool is installed.
// Search order: 1) explicit Path field, 2) PATH lookup, 3) common install locations.
func (r *Registry) DetectAvailability() {
	r.mu.Lock()
	defer r.mu.Unlock()

	home, _ := os.UserHomeDir()

	for _, p := range r.plugins {
		if p.Name == "terminal" {
			p.Available = true
			continue
		}

		// 1. Explicit path override (set by user in TOML: path = "C:\\...")
		if p.Command.Path != "" {
			if _, err := os.Stat(p.Command.Path); err == nil {
				p.Available = true
				p.Command.Cmd = p.Command.Path
				log.Printf("plugin %q: using explicit path %q", p.Name, p.Command.Path)
				continue
			}
		}

		// Determine which binary to look for
		bin := p.Command.Cmd
		if p.Command.DetectCmd != "" {
			if parts := strings.Fields(p.Command.DetectCmd); len(parts) > 0 {
				bin = parts[0]
			}
		}

		// 2. Standard PATH lookup
		if _, err := exec.LookPath(bin); err == nil {
			p.Available = true
			log.Printf("plugin %q: detected %q on PATH", p.Name, bin)
			continue
		}

		// 3. Search additional locations (Windows: Explorer-launched apps may have incomplete PATH)
		if found := searchBinary(p, bin, home); found {
			continue
		}
		log.Printf("plugin %q: %q not found on PATH or common locations", p.Name, bin)
	}
}

// searchBinary finds a binary when exec.LookPath fails. The motivating case
// is Windows: when Quil is launched from Explorer (rather than a Terminal
// session) the inherited PATH can be incomplete and PATHEXT lookups can
// miss legitimate .exe entries. We rescan the user-PATH directories with a
// direct os.Stat which is more reliable.
//
// On Unix, exec.LookPath has already exhausted $PATH using the same
// mechanism, so the additional walk is purely wasted syscalls. We still
// honor ~/.local/bin everywhere because some installers drop binaries
// there without updating PATH for the current login session.
func searchBinary(p *PanePlugin, bin, home string) bool {
	suffixes := []string{""}
	if runtime.GOOS == "windows" {
		suffixes = append(suffixes, ".exe")
	}

	// Common locations — checked on every OS.
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}

	// Windows-only: also walk every entry in the user PATH. On Unix
	// exec.LookPath already covered this and re-walking would just burn
	// syscalls.
	if runtime.GOOS == "windows" {
		if userPath := os.Getenv("Path"); userPath != "" {
			for _, dir := range strings.Split(userPath, string(os.PathListSeparator)) {
				dir = strings.TrimSpace(dir)
				if dir != "" {
					dirs = append(dirs, dir)
				}
			}
		}
	}

	for _, dir := range dirs {
		for _, suffix := range suffixes {
			candidate := filepath.Join(dir, bin+suffix)
			if _, err := os.Stat(candidate); err == nil {
				p.Available = true
				p.Command.Cmd = candidate
				log.Printf("plugin %q: found at %q (fallback search)", p.Name, candidate)
				return true
			}
		}
	}
	return false
}

// tomlPlugin is the on-disk representation of a plugin TOML file.
type tomlPlugin struct {
	Plugin struct {
		Name          string `toml:"name"`
		DisplayName   string `toml:"display_name"`
		Category      string `toml:"category"`
		Description   string `toml:"description"`
		SchemaVersion int    `toml:"schema_version"`
	} `toml:"plugin"`
	Command struct {
		Cmd              string   `toml:"cmd"`
		Path             string   `toml:"path"` // optional: full path to binary
		Args             []string `toml:"args"`
		Env              []string `toml:"env"`
		Detect           string   `toml:"detect"`
		ShellIntegration bool     `toml:"shell_integration"`
		ArgTemplate      []string `toml:"arg_template"`
		FormFields       []struct {
			Name     string `toml:"name"`
			Label    string `toml:"label"`
			Required bool   `toml:"required"`
			Default  string `toml:"default"`
		} `toml:"form_fields"`
		PromptsCWD bool `toml:"prompts_cwd"`
		Toggles    []struct {
			Name       string   `toml:"name"`
			Label      string   `toml:"label"`
			ArgsWhenOn []string `toml:"args_when_on"`
			Default    bool     `toml:"default"`
			Group      string   `toml:"group"`
		} `toml:"toggles"`
		RawKeys []string `toml:"raw_keys"`
	} `toml:"command"`
	Persistence struct {
		Strategy    string `toml:"strategy"`
		StartArgs   []string `toml:"start_args"`
		ResumeArgs  []string `toml:"resume_args"`
		GhostBuffer *bool  `toml:"ghost_buffer"` // pointer to detect unset (default true)
		Scrape     []struct {
			Name    string `toml:"name"`
			Pattern string `toml:"pattern"`
		} `toml:"scrape"`
	} `toml:"persistence"`
	Display struct {
		BorderColor string `toml:"border_color"`
		DialogWidth int    `toml:"dialog_width"`
	} `toml:"display"`
	Instances []struct {
		Name        string   `toml:"name"`
		DisplayName string   `toml:"display_name"`
		Args        []string `toml:"args"`
		Env         []string `toml:"env"`
	} `toml:"instances"`
	ErrorHandlers []struct {
		Pattern string `toml:"pattern"`
		Title   string `toml:"title"`
		Message string `toml:"message"`
		Action  string `toml:"action"`
	} `toml:"error_handlers"`
	NotificationHandlers []struct {
		Pattern  string `toml:"pattern"`
		Title    string `toml:"title"`
		Severity string `toml:"severity"`
	} `toml:"notification_handlers"`
	IdleHandlers []struct {
		Pattern  string `toml:"pattern"`
		Title    string `toml:"title"`
		Severity string `toml:"severity"`
	} `toml:"idle_handlers"`
}

func loadPluginTOML(path string) (*PanePlugin, error) {
	var tp tomlPlugin
	if _, err := toml.DecodeFile(path, &tp); err != nil {
		return nil, fmt.Errorf("decode TOML: %w", err)
	}

	if tp.Plugin.Name == "" {
		return nil, fmt.Errorf("plugin name is required")
	}
	if tp.Command.Cmd == "" {
		return nil, fmt.Errorf("plugin %q: command.cmd is required", tp.Plugin.Name)
	}
	switch tp.Persistence.Strategy {
	case "", "none", "cwd_only", "rerun", "session_scrape", "preassign_id":
		// valid
	default:
		return nil, fmt.Errorf("plugin %q: unknown strategy %q", tp.Plugin.Name, tp.Persistence.Strategy)
	}

	displayName := tp.Plugin.DisplayName
	if displayName == "" {
		displayName = tp.Plugin.Name
	}
	category := tp.Plugin.Category
	if category == "" {
		category = "tools"
	}

	// Cap raw_keys length so a hostile / mistaken TOML cannot turn the
	// per-keystroke linear scan in tryPluginRawKey into a hot path.
	rawKeys := sanitizeRawKeys(tp.Plugin.Name, tp.Command.RawKeys)

	p := &PanePlugin{
		Name:        tp.Plugin.Name,
		DisplayName: displayName,
		Category:    category,
		Description: tp.Plugin.Description,
		Command: CommandConfig{
			Cmd:              tp.Command.Cmd,
			Path:             tp.Command.Path,
			Args:             append([]string{}, tp.Command.Args...),
			Env:              append([]string{}, tp.Command.Env...),
			DetectCmd:        tp.Command.Detect,
			ShellIntegration: tp.Command.ShellIntegration,
			ArgTemplate:      append([]string{}, tp.Command.ArgTemplate...),
			PromptsCWD:       tp.Command.PromptsCWD,
			RawKeys:          rawKeys,
		},
		Persistence: PersistenceConfig{
			Strategy:    tp.Persistence.Strategy,
			StartArgs:   append([]string{}, tp.Persistence.StartArgs...),
			ResumeArgs:  append([]string{}, tp.Persistence.ResumeArgs...),
			GhostBuffer: tp.Persistence.GhostBuffer == nil || *tp.Persistence.GhostBuffer, // default true
		},
		Display: DisplayConfig{
			BorderColor: tp.Display.BorderColor,
			DialogWidth: tp.Display.DialogWidth,
		},
	}

	// Convert form fields
	for _, ff := range tp.Command.FormFields {
		p.Command.FormFields = append(p.Command.FormFields, FormField{
			Name:     ff.Name,
			Label:    ff.Label,
			Required: ff.Required,
			Default:  ff.Default,
		})
	}

	// Convert toggles
	for _, t := range tp.Command.Toggles {
		p.Command.Toggles = append(p.Command.Toggles, Toggle{
			Name:       t.Name,
			Label:      t.Label,
			ArgsWhenOn: append([]string{}, t.ArgsWhenOn...),
			Default:    t.Default,
			Group:      t.Group,
		})
	}

	// Convert scrapers
	for _, s := range tp.Persistence.Scrape {
		p.Persistence.Scrapers = append(p.Persistence.Scrapers, ScrapePattern{
			Name:    s.Name,
			Pattern: s.Pattern,
		})
	}

	// Convert instances
	for _, inst := range tp.Instances {
		p.Instances = append(p.Instances, InstanceConfig{
			Name:        inst.Name,
			DisplayName: inst.DisplayName,
			Args:        inst.Args,
			Env:         inst.Env,
		})
	}

	// Convert error handlers
	for _, eh := range tp.ErrorHandlers {
		action := eh.Action
		switch action {
		case "dialog", "log", "":
			// valid
		default:
			log.Printf("plugin %q: unknown error handler action %q, defaulting to log", tp.Plugin.Name, action)
			action = "log"
		}
		p.ErrorHandlers = append(p.ErrorHandlers, ErrorHandler{
			Pattern: eh.Pattern,
			Title:   eh.Title,
			Message: eh.Message,
			Action:  action,
		})
	}

	// Convert notification handlers
	for _, nh := range tp.NotificationHandlers {
		p.NotificationHandlers = append(p.NotificationHandlers, NotificationHandler{
			Pattern:  nh.Pattern,
			Title:    nh.Title,
			Severity: validateSeverity(tp.Plugin.Name, "notification", nh.Severity),
		})
	}

	// Convert idle handlers
	for _, ih := range tp.IdleHandlers {
		p.IdleHandlers = append(p.IdleHandlers, IdleHandler{
			Pattern:  ih.Pattern,
			Title:    ih.Title,
			Severity: validateSeverity(tp.Plugin.Name, "idle handler", ih.Severity),
		})
	}

	return p, nil
}

// validateSeverity normalizes a plugin TOML severity field. Empty becomes
// "info", unknown values are warned about and downgraded to "info". The
// `kind` argument is purely for the warning message ("notification" /
// "idle handler" / etc).
func validateSeverity(pluginName, kind, severity string) string {
	switch severity {
	case "info", "warning", "error":
		return severity
	case "":
		return "info"
	default:
		log.Printf("plugin %q: unknown %s severity %q, defaulting to info",
			pluginName, kind, severity)
		return "info"
	}
}

// maxRawKeys is the upper bound on entries we'll honor in a single plugin's
// raw_keys list. The list is scanned linearly on every keystroke, so a runaway
// (or malicious) TOML must not be able to make that scan O(huge). The number
// is generous — real plugins use 1-5 entries.
const maxRawKeys = 64

// sanitizeRawKeys validates a plugin's raw_keys list. It caps the length,
// rejects empty strings, and warns when an entry would shadow a single
// printable character (e.g. raw_keys = ["a"] would eat every "a" keypress
// for that pane type, including all of Quil's global shortcuts that use it).
// Returns a defensive copy.
func sanitizeRawKeys(pluginName string, raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxRawKeys {
		log.Printf("plugin %q: raw_keys has %d entries, truncating to %d", pluginName, len(raw), maxRawKeys)
		raw = raw[:maxRawKeys]
	}
	out := make([]string, 0, len(raw))
	for _, k := range raw {
		if k == "" {
			continue
		}
		// One-rune entries that are printable would shadow ordinary typing
		// for the entire pane. Allow it, but make the consequence visible
		// in the log so a misconfiguration is easy to find.
		if len([]rune(k)) == 1 {
			r := []rune(k)[0]
			if r >= 0x20 && r != 0x7f {
				log.Printf("plugin %q: raw_keys entry %q shadows a printable key — every press will bypass Quil's global shortcuts", pluginName, k)
			}
		}
		out = append(out, k)
	}
	return out
}
