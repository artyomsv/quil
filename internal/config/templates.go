package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// A workspace template describes ONE TAB: a list of panes, how they are laid
// out, and an optional starting prompt for each. Quil recreates that setup on
// demand and then gets out of the way — nothing here survives creation, and
// nothing here supervises what the panes go on to do.
//
// This package owns the file and its validation only. Anything needing the
// plugin registry (does this plugin exist, do these toggle names resolve) is
// the daemon's, because the registry describes a MACHINE and this file can be
// edited for a machine it is not on.

//go:embed templates.toml
var defaultTemplates string

// DefaultTemplatesText supplies the editable starting file, comments included.
func DefaultTemplatesText() string { return defaultTemplates }

// Layout keywords. Empty means LayoutRows, which is what an untemplated tab
// has always done.
const (
	LayoutRows     = "rows"      // stacked top to bottom
	LayoutColumns  = "columns"   // side by side
	LayoutMainLeft = "main-left" // main pane full height left, rest stacked right
	LayoutMainTop  = "main-top"  // main pane full width top, rest side by side below
	LayoutGrid     = "grid"      // two columns, filled top to bottom
)

var templateLayouts = []string{LayoutRows, LayoutColumns, LayoutMainLeft, LayoutMainTop, LayoutGrid}

var templateModelShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,79}$`)
var templatePlaceholder = regexp.MustCompile(`\{\{[^{}]+\}\}`)

// templateName bounds a template name to what a palette row and a TOML key can
// both carry without quoting.
var templateName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// TemplatePane is one pane of a template. Every field maps to something the
// create-pane path already accepts; none of them is a new concept.
type TemplatePane struct {
	// Type is a plugin name — any plugin, not only the AI ones.
	Type string `toml:"type" json:"type"`
	// Name labels the pane. Empty keeps the plugin's own label.
	Name string `toml:"name,omitempty" json:"name,omitempty"`
	// Model is passed to the agent's own model flag at creation and frozen
	// into the pane's arguments, so a later edit of this file cannot reach a
	// pane that already exists. Ignored for a non-AI plugin.
	Model string `toml:"model,omitempty" json:"model,omitempty"`
	// Toggles are plugin toggle NAMES, resolved by the daemon. Never raw args.
	Toggles []string `toml:"toggles,omitempty" json:"toggles,omitempty"`
	// CWD is relative to the directory chosen at creation and may not leave it.
	CWD string `toml:"cwd,omitempty" json:"cwd,omitempty"`
	// Prompt is typed into the pane after EVERY pane of the tab exists, so
	// {{panes}} can name all of them.
	Prompt string `toml:"prompt,omitempty" json:"prompt,omitempty"`
	// Muted sets the existing per-pane mute, so this pane's turns raise no
	// notification. A pane being driven by another one would otherwise toast
	// the user on every delegated turn.
	Muted bool `toml:"muted,omitempty" json:"muted,omitempty"`
	// Main marks the layout anchor for main-left and main-top. At most one
	// pane per template; the first pane is the default. It exists so layout
	// does not dictate ORDER, which is what decides who is prompted first.
	Main bool `toml:"main,omitempty" json:"main,omitempty"`
	// QuilMCP registers the Quil MCP server for this pane at spawn, for a
	// template with a pane that drives the others.
	QuilMCP bool `toml:"quil_mcp,omitempty" json:"quil_mcp,omitempty"`
}

type Template struct {
	Name        string         `toml:"name" json:"name"`
	Description string         `toml:"description,omitempty" json:"description,omitempty"`
	Layout      string         `toml:"layout,omitempty" json:"layout,omitempty"`
	Panes       []TemplatePane `toml:"panes" json:"panes"`
}

type Templates struct {
	Templates []Template `toml:"templates" json:"templates"`
}

func TemplatesPath() string { return filepath.Join(QuilDir(), "templates.toml") }

// DefaultTemplates returns the embedded templates. It PANICS on an invalid
// embed rather than returning a zero value, because the failure would
// otherwise surface much later as "unknown template" with nothing naming the
// cause. The usual cause is a CRLF checkout of templates.toml on Windows,
// which .gitattributes pins against.
func DefaultTemplates() Templates {
	var t Templates
	if err := toml.Unmarshal([]byte(defaultTemplates), &t); err != nil {
		panic("invalid embedded templates: " + err.Error())
	}
	if err := t.Validate(); err != nil {
		panic("invalid embedded templates: " + err.Error())
	}
	return t
}

func LoadTemplates() (Templates, error) {
	data, err := os.ReadFile(TemplatesPath())
	if os.IsNotExist(err) {
		return DefaultTemplates(), nil
	}
	if err != nil {
		return Templates{}, fmt.Errorf("read templates: %w", err)
	}
	var t Templates
	if err := toml.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("parse templates: %w", err)
	}
	return t, t.Validate()
}

func (t Templates) ByName(name string) (Template, bool) {
	for _, tpl := range t.Templates {
		if tpl.Name == name {
			return tpl, true
		}
	}
	return Template{}, false
}

// MainPane reports the index of the layout anchor: the pane marked main, or
// the first one.
func (t Template) MainPane() int {
	for i, p := range t.Panes {
		if p.Main {
			return i
		}
	}
	return 0
}

// LayoutKeyword resolves the empty default.
func (t Template) LayoutKeyword() string {
	if t.Layout == "" {
		return LayoutRows
	}
	return t.Layout
}

func (t Templates) Validate() error {
	if len(t.Templates) == 0 {
		return fmt.Errorf("no templates defined")
	}
	seen := make(map[string]bool, len(t.Templates))
	for _, tpl := range t.Templates {
		if !templateName.MatchString(tpl.Name) {
			return fmt.Errorf("template name %q must be lower-case letters, digits and hyphens, starting with a letter", tpl.Name)
		}
		if seen[tpl.Name] {
			return fmt.Errorf("template %q is defined twice", tpl.Name)
		}
		seen[tpl.Name] = true
		if err := tpl.validate(); err != nil {
			return fmt.Errorf("template %q: %w", tpl.Name, err)
		}
	}
	return nil
}

func (t Template) validate() error {
	if t.Layout != "" {
		ok := false
		for _, k := range templateLayouts {
			ok = ok || k == t.Layout
		}
		if !ok {
			return fmt.Errorf("layout %q is not one of %s", t.Layout, strings.Join(templateLayouts, ", "))
		}
	}
	if len(t.Panes) == 0 {
		return fmt.Errorf("has no panes")
	}
	if len(t.Panes) > 8 {
		return fmt.Errorf("has %d panes; the limit is 8", len(t.Panes))
	}
	if UnsafeTemplateText(t.Description) {
		return fmt.Errorf("description contains terminal control characters")
	}
	mains := 0
	for i, p := range t.Panes {
		if p.Main {
			mains++
		}
		if err := p.validate(); err != nil {
			return fmt.Errorf("pane %d: %w", i+1, err)
		}
	}
	if mains > 1 {
		return fmt.Errorf("has %d panes marked main; at most one may be", mains)
	}
	return nil
}

func (p TemplatePane) validate() error {
	if strings.TrimSpace(p.Type) == "" {
		return fmt.Errorf("has no type")
	}
	// The plugin name itself is checked by the daemon, which holds the
	// registry. Here we only refuse what no plugin name could be.
	if UnsafeTemplateText(p.Type) || UnsafeTemplateText(p.Name) || UnsafeTemplateText(p.Prompt) || UnsafeTemplateText(p.CWD) {
		return fmt.Errorf("contains terminal control characters")
	}
	if p.Model != "" && !templateModelShape.MatchString(p.Model) {
		return fmt.Errorf("model %q is not a valid model id", p.Model)
	}
	for _, name := range p.Toggles {
		if strings.TrimSpace(name) == "" || UnsafeTemplateText(name) {
			return fmt.Errorf("has an empty or unusable toggle name")
		}
	}
	return safeRelativePath(p.CWD)
}

// safeRelativePath refuses a cwd that could leave the directory the user
// chose. The drive-letter and backslash cases are checked explicitly rather
// than through filepath, because CI runs on Linux where `C:\x` is an ordinary
// relative file name and the check would be statically dead.
func safeRelativePath(p string) error {
	if p == "" {
		return nil
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return fmt.Errorf("cwd %q must be relative to the chosen directory", p)
	}
	if len(p) > 1 && p[1] == ':' {
		return fmt.Errorf("cwd %q must be relative to the chosen directory", p)
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return fmt.Errorf("cwd %q may not leave the chosen directory", p)
		}
	}
	return nil
}

// UnsafeTemplateText reports text that must never be typed into a pane: ESC
// and the C1 CSI introducer in both spellings, and CR, which would submit a
// prompt early and leave the rest as loose keystrokes. Newlines and tabs are
// text and are kept.
func UnsafeTemplateText(s string) bool {
	return strings.ContainsAny(s, "\x1b\u009b\r") || strings.Contains(s, string([]byte{0x9b}))
}

func WriteTemplates(t Templates) error {
	if err := t.Validate(); err != nil {
		return err
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(t); err != nil {
		return err
	}
	return WriteTemplatesSource(b.String())
}

// WriteTemplatesSource validates the complete file before an atomic write,
// preserving comments and prompt formatting from the TOML editor.
func WriteTemplatesSource(source string) error {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	var templates Templates
	if err := toml.Unmarshal([]byte(source), &templates); err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	if err := templates.Validate(); err != nil {
		return err
	}
	path := TemplatesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".templates-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(source); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// TemplatePlaceholders are the only substitutions a prompt may use.
var TemplatePlaceholders = []string{"{{task}}", "{{dir}}", "{{branch}}", "{{panes}}"}

// RenderPrompt substitutes the placeholders in ONE pass, so a placeholder
// written inside the user's own task text stays literal instead of expanding
// against a second value.
func RenderPrompt(prompt string, vars map[string]string) string {
	pairs := make([]string, 0, len(TemplatePlaceholders)*2)
	for _, key := range TemplatePlaceholders {
		pairs = append(pairs, key, vars[strings.Trim(key, "{}")])
	}
	return strings.NewReplacer(pairs...).Replace(prompt)
}

// UnknownTemplatePlaceholders names the {{…}} groups a prompt uses that
// nothing will substitute, so the editor can say so before the file is saved.
func UnknownTemplatePlaceholders(prompt string) []string {
	known := make(map[string]bool, len(TemplatePlaceholders))
	for _, key := range TemplatePlaceholders {
		known[key] = true
	}
	var out []string
	for _, key := range templatePlaceholder.FindAllString(prompt, -1) {
		if !known[key] {
			out = append(out, key)
		}
	}
	return out
}
