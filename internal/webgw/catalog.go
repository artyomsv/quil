package webgw

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/artyomsv/quil/internal/plugin"
)

// PluginDef is what the page learns about a plugin: enough to draw the
// create-pane dialog. Never the command, its arguments, its environment or
// its argument template — the gateway expands saved instances itself.
type PluginDef struct {
	Name           string      `json:"name"`
	DisplayName    string      `json:"display_name"`
	Category       string      `json:"category"`
	Description    string      `json:"description,omitempty"`
	Homepage       string      `json:"homepage,omitempty"`
	PromptsCWD     bool        `json:"prompts_cwd,omitempty"`
	Discover       string      `json:"discover,omitempty"`
	Sessions       string      `json:"sessions,omitempty"`
	FormFields     []FieldDef  `json:"form_fields,omitempty"`
	Toggles        []ToggleDef `json:"toggles,omitempty"`
	RawKeys        []string    `json:"raw_keys,omitempty"`
	UsesClaudeAuth bool        `json:"uses_claude_auth,omitempty"`
}

// FieldDef is one field of a plugin's saved-instance form.
type FieldDef struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
}

// ToggleDef is one setup toggle; toggles sharing a Group are exclusive.
type ToggleDef struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
	Group   string `json:"group,omitempty"`
}

// CategoryDef is one dialog category, in the TUI's order.
type CategoryDef struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// catalog holds the plugin definitions of the machine running quil web (spec
// 5b E7): loaded at start, and reloaded when the plugin directory's *.toml
// files change (name, size, modification time), checked on each read. The
// page has no plugin editor that could report a reload.
//
// It is also the ONE source of plugin definitions in the gateway: the
// saved-instance expansion reads its arg templates here, so what /api/client
// lists and what a submit expands can never disagree.
type catalog struct {
	dir string

	mu  sync.Mutex
	fp  string
	reg *plugin.Registry
}

func newCatalog(dir string) *catalog {
	c := &catalog{dir: dir}
	c.mu.Lock()
	c.refreshLocked()
	c.mu.Unlock()
	return c
}

// fingerprint names every *.toml with its size and modification time.
func (c *catalog) fingerprint() string {
	if c.dir == "" {
		return ""
	}
	ents, err := os.ReadDir(c.dir)
	if err != nil {
		return "unreadable"
	}
	var b strings.Builder
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", e.Name(), fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String()
}

// refreshLocked reloads the registry when the fingerprint moved. Caller holds
// c.mu. The registry is replaced, never reloaded in place: a caller still
// reading the old one keeps a consistent set.
func (c *catalog) refreshLocked() {
	fp := c.fingerprint()
	if c.reg != nil && fp == c.fp {
		return
	}
	reg := plugin.NewRegistry()
	if c.dir != "" {
		// Quiet: a reload may follow any request. A file that fails to load
		// is still logged, and the built-ins stay.
		_ = reg.LoadFromDirQuiet(c.dir)
	}
	c.reg, c.fp = reg, fp
}

func (c *catalog) registry() *plugin.Registry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked()
	return c.reg
}

// plugins lists every plugin, by name.
func (c *catalog) plugins() []PluginDef {
	all := c.registry().All()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	out := make([]PluginDef, 0, len(all))
	for _, p := range all {
		d := PluginDef{
			Name: p.Name, DisplayName: p.DisplayName, Category: p.Category,
			Description: p.Description, Homepage: p.Homepage,
			PromptsCWD: p.Command.PromptsCWD, Discover: p.Command.Discover, Sessions: p.Command.Sessions,
			RawKeys: append([]string(nil), p.Command.RawKeys...), UsesClaudeAuth: p.UsesClaudeAuth(),
		}
		for _, f := range p.Command.FormFields {
			d.FormFields = append(d.FormFields, FieldDef{Name: f.Name, Label: f.Label, Required: f.Required, Default: f.Default})
		}
		for _, t := range p.Command.Toggles {
			d.Toggles = append(d.Toggles, ToggleDef{Name: t.Name, Label: t.Label, Default: t.Default, Group: t.Group})
		}
		out = append(out, d)
	}
	return out
}

func categories() []CategoryDef {
	var out []CategoryDef
	for _, c := range plugin.CategoryOrder() {
		out = append(out, CategoryDef{Key: c.Key, Label: c.Label})
	}
	return out
}

// argTemplate is the plugin's instance argument template, and whether the
// plugin manages instances at all (has form fields).
func (c *catalog) argTemplate(name string) ([]string, bool) {
	p := c.registry().Get(name)
	if p == nil || len(p.Command.FormFields) == 0 {
		return nil, false
	}
	return append([]string(nil), p.Command.ArgTemplate...), true
}

// formFieldNames is the set of field keys an instance of name may carry; nil
// for an unknown plugin or one that manages no instances.
func (c *catalog) formFieldNames(name string) map[string]bool {
	p := c.registry().Get(name)
	if p == nil || len(p.Command.FormFields) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, f := range p.Command.FormFields {
		out[f.Name] = true
	}
	return out
}
