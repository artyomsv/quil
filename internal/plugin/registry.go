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

// DetectAvailability checks whether each plugin's tool is installed by
// looking up the binary on PATH. Terminal is always available.
func (r *Registry) DetectAvailability() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, p := range r.plugins {
		if p.Name == "terminal" {
			p.Available = true
			continue
		}

		// Determine which binary to look for:
		// prefer the detect command's binary (first word), fall back to cmd.
		bin := p.Command.Cmd
		if p.Command.DetectCmd != "" {
			if parts := strings.Fields(p.Command.DetectCmd); len(parts) > 0 {
				bin = parts[0]
			}
		}

		if _, err := exec.LookPath(bin); err == nil {
			p.Available = true
		}
	}
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
		Args             []string `toml:"args"`
		Env              []string `toml:"env"`
		Detect           string   `toml:"detect"`
		ShellIntegration bool     `toml:"shell_integration"`
	} `toml:"command"`
	Persistence struct {
		Strategy   string `toml:"strategy"`
		StartArgs  []string `toml:"start_args"`
		ResumeArgs []string `toml:"resume_args"`
		Scrape     []struct {
			Name    string `toml:"name"`
			Pattern string `toml:"pattern"`
		} `toml:"scrape"`
	} `toml:"persistence"`
	Display struct {
		BorderColor string `toml:"border_color"`
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
			Args:             tp.Command.Args,
			Env:              tp.Command.Env,
			DetectCmd:        tp.Command.Detect,
			ShellIntegration: tp.Command.ShellIntegration,
		},
		Persistence: PersistenceConfig{
			Strategy:   tp.Persistence.Strategy,
			StartArgs:  tp.Persistence.StartArgs,
			ResumeArgs: tp.Persistence.ResumeArgs,
		},
		Display: DisplayConfig{
			BorderColor: tp.Display.BorderColor,
		},
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

	return p, nil
}
