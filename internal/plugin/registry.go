package plugin

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
			log.Printf("plugin %s: invalid scrape pattern %q: %v", p.Name, p.Persistence.Scrapers[i].Pattern, err)
		}
	}
	for i := range p.ErrorHandlers {
		if err := p.ErrorHandlers[i].Compile(); err != nil {
			log.Printf("plugin %s: invalid error pattern %q: %v", p.Name, p.ErrorHandlers[i].Pattern, err)
		}
	}
	for i := range p.NotificationHandlers {
		if err := p.NotificationHandlers[i].Compile(); err != nil {
			log.Printf("plugin %s: invalid notification pattern %q: %v", p.Name, p.NotificationHandlers[i].Pattern, err)
		}
	}
	for i := range p.IdleHandlers {
		if err := p.IdleHandlers[i].Compile(); err != nil {
			log.Printf("plugin %s: invalid idle pattern %q: %v", p.Name, p.IdleHandlers[i].Pattern, err)
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

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, err := loadPluginTOML(path)
		if err != nil {
			log.Printf("skip plugin %s: %v", e.Name(), err)
			continue
		}
		compilePatterns(p)
		r.plugins[p.Name] = p
		log.Printf("loaded plugin: %s from %s", p.Name, e.Name())
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
				log.Printf("plugin %s: using explicit path %q", p.Name, p.Command.Path)
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
			log.Printf("plugin %s: detected %q on PATH", p.Name, bin)
			continue
		}

		// 3. Search additional locations (Windows: Explorer-launched apps may have incomplete PATH)
		if found := searchBinary(p, bin, home); found {
			continue
		}
		log.Printf("plugin %s: %q not found on PATH or common locations", p.Name, bin)
	}
}

// searchBinary finds a binary when exec.LookPath fails (common on Windows when
// launched from Explorer). Re-scans PATH directories with os.Stat which is more
// reliable than LookPath for Explorer-launched processes with PATHEXT issues.
func searchBinary(p *PanePlugin, bin, home string) bool {
	suffixes := []string{"", ".exe"}

	// Common locations
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}

	// On Windows, read the full User PATH from environment to cover
	// directories that may be missing from Explorer-launched processes
	if userPath := os.Getenv("Path"); userPath != "" {
		for _, dir := range strings.Split(userPath, string(os.PathListSeparator)) {
			dir = strings.TrimSpace(dir)
			if dir != "" {
				dirs = append(dirs, dir)
			}
		}
	}

	for _, dir := range dirs {
		for _, suffix := range suffixes {
			candidate := filepath.Join(dir, bin+suffix)
			if _, err := os.Stat(candidate); err == nil {
				p.Available = true
				p.Command.Cmd = candidate
				log.Printf("plugin %s: found at %q (fallback search)", p.Name, candidate)
				return true
			}
		}
	}
	return false
}

// tomlPlugin is the on-disk representation of a plugin TOML file.
type tomlPlugin struct {
	Plugin struct {
		Name        string `toml:"name"`
		DisplayName string `toml:"display_name"`
		Category    string `toml:"category"`
		Description string `toml:"description"`
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

	p := &PanePlugin{
		Name:        tp.Plugin.Name,
		DisplayName: displayName,
		Category:    category,
		Description: tp.Plugin.Description,
		Command: CommandConfig{
			Cmd:              tp.Command.Cmd,
			Path:             tp.Command.Path,
			Args:             tp.Command.Args,
			Env:              tp.Command.Env,
			DetectCmd:        tp.Command.Detect,
			ShellIntegration: tp.Command.ShellIntegration,
			ArgTemplate:      tp.Command.ArgTemplate,
		},
		Persistence: PersistenceConfig{
			Strategy:    tp.Persistence.Strategy,
			StartArgs:   tp.Persistence.StartArgs,
			ResumeArgs:  tp.Persistence.ResumeArgs,
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
		severity := nh.Severity
		switch severity {
		case "info", "warning", "error":
			// valid
		case "":
			severity = "info"
		default:
			log.Printf("plugin %q: unknown notification severity %q, defaulting to info", tp.Plugin.Name, severity)
			severity = "info"
		}
		p.NotificationHandlers = append(p.NotificationHandlers, NotificationHandler{
			Pattern:  nh.Pattern,
			Title:    nh.Title,
			Severity: severity,
		})
	}

	// Convert idle handlers
	for _, ih := range tp.IdleHandlers {
		severity := ih.Severity
		switch severity {
		case "info", "warning", "error":
			// valid
		case "":
			severity = "info"
		default:
			log.Printf("plugin %q: unknown idle handler severity %q, defaulting to info", tp.Plugin.Name, severity)
			severity = "info"
		}
		p.IdleHandlers = append(p.IdleHandlers, IdleHandler{
			Pattern:  ih.Pattern,
			Title:    ih.Title,
			Severity: severity,
		})
	}

	return p, nil
}
