package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

func TestEmbeddedTemplates_NoCarriageReturns(t *testing.T) {
	if strings.ContainsRune(defaultTemplates, '\r') {
		t.Fatal("embedded templates contain CR; check .gitattributes eol=lf")
	}
}

func TestLoadTemplates_MissingFile_UsesEmbeddedDefaults(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	got, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	for _, name := range []string{"agent-team", "pair", "review"} {
		if _, ok := got.ByName(name); !ok {
			t.Fatalf("shipped template %q missing from %+v", name, got)
		}
	}
	if _, ok := got.ByName("no-such-template"); ok {
		t.Fatal("ByName invented a template")
	}
}

func TestWriteTemplates_RoundTripsThroughDisk(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	want := DefaultTemplates()
	want.Templates[0].Description = "edited"
	if err := WriteTemplates(want); err != nil {
		t.Fatalf("WriteTemplates: %v", err)
	}
	got, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the templates:\n got %+v\nwant %+v", got, want)
	}
}

func TestWriteTemplates_InvalidInput_RefusesAndWritesNothing(t *testing.T) {
	// One case per Validate rule. Each mutates a fresh copy of the shipped
	// default, so a rule that stops being reachable shows up as a pass here
	// and a failure in the round-trip test above.
	cases := []struct {
		name string
		bad  func(*Templates)
		want string
	}{
		{"no templates", func(t *Templates) { t.Templates = nil }, "no templates"},
		{"name shape", func(t *Templates) { t.Templates[0].Name = "Agent Team" }, "template name"},
		{"duplicate name", func(t *Templates) { t.Templates[1].Name = t.Templates[0].Name }, "defined twice"},
		{"unknown layout", func(t *Templates) { t.Templates[0].Layout = "diagonal" }, "layout"},
		{"no panes", func(t *Templates) { t.Templates[0].Panes = nil }, "no panes"},
		{"too many panes", func(t *Templates) {
			for len(t.Templates[0].Panes) <= 8 {
				t.Templates[0].Panes = append(t.Templates[0].Panes, t.Templates[0].Panes[0])
			}
		}, "the limit is 8"},
		{"two mains", func(t *Templates) {
			for i := range t.Templates[0].Panes {
				t.Templates[0].Panes[i].Main = true
			}
		}, "marked main"},
		{"empty type", func(t *Templates) { t.Templates[0].Panes[0].Type = " " }, "no type"},
		{"control in prompt", func(t *Templates) { t.Templates[0].Panes[0].Prompt = "hi\x1b[31m" }, "control characters"},
		{"control in name", func(t *Templates) { t.Templates[0].Panes[0].Name = "an\rlyst" }, "control characters"},
		{"control in description", func(t *Templates) { t.Templates[0].Description = "a\x9bb" }, "control characters"},
		{"bad model", func(t *Templates) { t.Templates[0].Panes[0].Model = "-m sneaky" }, "model"},
		{"empty toggle", func(t *Templates) { t.Templates[0].Panes[0].Toggles = []string{""} }, "toggle name"},
		{"absolute cwd", func(t *Templates) { t.Templates[0].Panes[0].CWD = "/etc" }, "relative"},
		{"drive-letter cwd", func(t *Templates) { t.Templates[0].Panes[0].CWD = `C:\Windows` }, "relative"},
		{"escaping cwd", func(t *Templates) { t.Templates[0].Panes[0].CWD = "sub/../../elsewhere" }, "may not leave"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir())
			tpl := DefaultTemplates()
			tc.bad(&tpl)
			err := WriteTemplates(tpl)
			if err == nil {
				t.Fatal("accepted an invalid template")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if _, statErr := os.Stat(TemplatesPath()); !os.IsNotExist(statErr) {
				t.Fatal("a refused template still wrote the file")
			}
		})
	}
}

func TestRenderPrompt_SubstitutesOncePerPlaceholder(t *testing.T) {
	vars := map[string]string{
		"task":   "fix the thing, then use {{dir}} literally",
		"dir":    "E:/work",
		"branch": "feat/x",
		"panes":  "orchestrator pane-1 claude-code",
	}
	got := RenderPrompt("job: {{task}}\nin {{dir}} on {{branch}}\n{{panes}}", vars)
	for _, want := range []string{"fix the thing", "in E:/work on feat/x", "orchestrator pane-1 claude-code"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	// The {{dir}} the USER wrote inside the task text must survive as text: a
	// second pass would expand it and let one value rewrite another.
	if !strings.Contains(got, "use {{dir}} literally") {
		t.Fatalf("a placeholder inside the task text was expanded: %q", got)
	}
	if strings.Count(got, "E:/work") != 1 {
		t.Fatalf("dir substituted more than once: %q", got)
	}
	// An absent variable renders empty rather than leaving the marker.
	if got := RenderPrompt("[{{branch}}]", nil); got != "[]" {
		t.Fatalf("missing variable left a marker: %q", got)
	}
}

func TestUnknownTemplatePlaceholders_NamesOnlyTheUnsubstituted(t *testing.T) {
	got := UnknownTemplatePlaceholders("{{task}} {{roster}} {{dir}} {{plan}}")
	if !reflect.DeepEqual(got, []string{"{{roster}}", "{{plan}}"}) {
		t.Fatal(got)
	}
	if got := UnknownTemplatePlaceholders("{{task}} {{dir}} {{branch}} {{panes}}"); got != nil {
		t.Fatal(got)
	}
}

func TestTemplate_LayoutAndMainPane_HaveDefaults(t *testing.T) {
	tpl := Template{Panes: []TemplatePane{{Type: "terminal"}, {Type: "terminal", Main: true}}}
	if tpl.LayoutKeyword() != LayoutRows {
		t.Fatal("empty layout is not rows", tpl.LayoutKeyword())
	}
	if tpl.MainPane() != 1 {
		t.Fatal("main pane not found", tpl.MainPane())
	}
	if (Template{Panes: []TemplatePane{{Type: "terminal"}}}).MainPane() != 0 {
		t.Fatal("unmarked template did not default to its first pane")
	}
}

// The shipped templates must only name toggles the shipped plugins define,
// read from the plugin TOMLs rather than retyped here, so a renamed toggle
// fails this test instead of failing at pane creation.
func TestShippedTemplates_UseRealToggleNames(t *testing.T) {
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatal(err)
	}
	for _, tpl := range DefaultTemplates().Templates {
		for _, pane := range tpl.Panes {
			if len(pane.Toggles) == 0 {
				continue
			}
			data, err := os.ReadFile(dir + "/" + pane.Type + ".toml")
			if err != nil {
				t.Fatalf("template %q names plugin %q: %v", tpl.Name, pane.Type, err)
			}
			for _, toggle := range pane.Toggles {
				if !strings.Contains(string(data), `name = "`+toggle+`"`) {
					t.Fatalf("template %q pane %q: plugin %q has no toggle %q", tpl.Name, pane.Name, pane.Type, toggle)
				}
			}
		}
	}
}

// The agent-team prompts are the product, not filler: they carry the two
// lessons that made the manual run work, and losing either silently returns
// the feature to something nobody used.
func TestShippedAgentTeam_PromptsCarryTheirContract(t *testing.T) {
	tpl, ok := DefaultTemplates().ByName("agent-team")
	if !ok {
		t.Fatal("agent-team is not shipped")
	}
	byName := make(map[string]TemplatePane, len(tpl.Panes))
	for _, p := range tpl.Panes {
		byName[p.Name] = p
	}
	orchestrator, worker := byName["orchestrator"], []TemplatePane{byName["analyst"], byName["developer"]}

	if !orchestrator.QuilMCP || !orchestrator.Main {
		t.Fatal("the orchestrator must be the MCP pane and the layout anchor", orchestrator)
	}
	if tpl.Panes[len(tpl.Panes)-1].Name != "orchestrator" {
		t.Fatal("the orchestrator must be prompted last, so its teammates already exist")
	}
	for _, want := range []string{"{{panes}}", "{{task}}", "delegate_task", "You own git", "never commit", "gh"} {
		if !strings.Contains(orchestrator.Prompt, want) {
			t.Fatalf("orchestrator prompt lacks %q", want)
		}
	}
	for _, p := range worker {
		if !p.Muted {
			t.Fatalf("%s must be muted, or every delegated turn notifies the user", p.Name)
		}
		if p.QuilMCP {
			t.Fatalf("%s does not drive other panes and must not get the MCP server", p.Name)
		}
		if !strings.Contains(p.Prompt, "do NOT commit") && !strings.Contains(p.Prompt, "NOT commit") {
			t.Fatalf("%s prompt does not forbid committing", p.Name)
		}
	}
	if !strings.Contains(byName["developer"].Prompt, "file") {
		t.Fatal("the developer prompt must tell it to report in a file; its pane cannot be read")
	}
}
