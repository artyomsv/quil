package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryBuiltins(t *testing.T) {
	r := NewRegistry()

	// All 4 built-ins should be present
	names := []string{"terminal", "claude-code", "stripe", "ssh"}
	for _, name := range names {
		if r.Get(name) == nil {
			t.Errorf("built-in plugin %q not found", name)
		}
	}
}

func TestRegistryTerminalAlwaysAvailable(t *testing.T) {
	r := NewRegistry()
	r.DetectAvailability()

	terminal := r.Get("terminal")
	if terminal == nil {
		t.Fatal("terminal plugin missing")
	}
	if !terminal.Available {
		t.Error("terminal plugin should always be available")
	}
}

func TestRegistryByCategory(t *testing.T) {
	r := NewRegistry()
	cats := r.ByCategory()

	if _, ok := cats["terminal"]; !ok {
		t.Error("missing terminal category")
	}
	if _, ok := cats["ai"]; !ok {
		t.Error("missing ai category")
	}
	if _, ok := cats["tools"]; !ok {
		t.Error("missing tools category")
	}
	if _, ok := cats["remote"]; !ok {
		t.Error("missing remote category")
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()

	p := r.Get("terminal")
	if p == nil {
		t.Fatal("terminal plugin not found")
	}
	if p.DisplayName != "Terminal" {
		t.Errorf("expected display name 'Terminal', got %q", p.DisplayName)
	}
	if p.Persistence.Strategy != "cwd_only" {
		t.Errorf("expected strategy 'cwd_only', got %q", p.Persistence.Strategy)
	}
}

func TestRegistryGetNonExistent(t *testing.T) {
	r := NewRegistry()
	if r.Get("nonexistent") != nil {
		t.Error("expected nil for nonexistent plugin")
	}
}

func TestScrapeOutput(t *testing.T) {
	p := &PanePlugin{
		Persistence: PersistenceConfig{
			Strategy: "session_scrape",
			Scrapers: []ScrapePattern{
				{Name: "session_id", Pattern: `Session: ([a-zA-Z0-9_-]+)`},
			},
		},
	}
	compilePatterns(p)

	data := []byte("Starting session\nSession: abc-123-def\nReady.")
	result := ScrapeOutput(p, data)
	if result == nil {
		t.Fatal("expected scraped result")
	}
	if result["session_id"] != "abc-123-def" {
		t.Errorf("expected session_id 'abc-123-def', got %q", result["session_id"])
	}
}

func TestScrapeOutputNoMatch(t *testing.T) {
	p := &PanePlugin{
		Persistence: PersistenceConfig{
			Strategy: "session_scrape",
			Scrapers: []ScrapePattern{
				{Name: "session_id", Pattern: `Session: ([a-zA-Z0-9_-]+)`},
			},
		},
	}
	compilePatterns(p)

	data := []byte("Just some regular output with no session info")
	result := ScrapeOutput(p, data)
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestScrapeOutputNilPlugin(t *testing.T) {
	result := ScrapeOutput(nil, []byte("data"))
	if result != nil {
		t.Error("expected nil for nil plugin")
	}
}

func TestMatchError(t *testing.T) {
	p := builtinSSH()
	// Pre-compile handlers
	for i := range p.ErrorHandlers {
		p.ErrorHandlers[i].Compile()
	}

	data := []byte("ssh: connect to host: Permission denied (publickey)")
	eh := MatchError(p, data)
	if eh == nil {
		t.Fatal("expected error handler match")
	}
	if eh.Title != "SSH Authentication Failed" {
		t.Errorf("expected title 'SSH Authentication Failed', got %q", eh.Title)
	}
}

func TestClaudeCodePreassignStrategy(t *testing.T) {
	p := builtinClaudeCode()
	if p.Persistence.Strategy != "preassign_id" {
		t.Errorf("expected strategy 'preassign_id', got %q", p.Persistence.Strategy)
	}
	if len(p.Persistence.StartArgs) != 2 || p.Persistence.StartArgs[0] != "--session-id" {
		t.Errorf("expected StartArgs [--session-id {session_id}], got %v", p.Persistence.StartArgs)
	}
	if len(p.Persistence.ResumeArgs) != 2 || p.Persistence.ResumeArgs[0] != "--resume" {
		t.Errorf("expected ResumeArgs [--resume {session_id}], got %v", p.Persistence.ResumeArgs)
	}
	if len(p.Persistence.Scrapers) != 0 {
		t.Errorf("expected no scrapers for preassign_id, got %d", len(p.Persistence.Scrapers))
	}
}

func TestExpandResumeArgsUnresolved(t *testing.T) {
	template := []string{"--resume", "{session_id}"}
	state := map[string]string{"other_key": "value"}
	result := ExpandResumeArgs(template, state)
	if result != nil {
		t.Errorf("expected nil for unresolved placeholder, got %v", result)
	}
}

func TestDetectAvailabilityWithDetectCmd(t *testing.T) {
	r := NewRegistry()
	// Claude Code has detect cmd "claude --version" — DetectAvailability
	// extracts "claude" and checks LookPath. We just verify it doesn't panic.
	r.DetectAvailability()

	// Terminal should always be available
	if !r.Get("terminal").Available {
		t.Error("terminal should always be available")
	}
}

func TestCompileInvalidPattern(t *testing.T) {
	sp := ScrapePattern{Name: "bad", Pattern: `[invalid`}
	err := sp.Compile()
	if err == nil {
		t.Error("expected error for invalid regex")
	}
	if sp.Compiled() != nil {
		t.Error("compiled should be nil after failed compile")
	}

	eh := ErrorHandler{Pattern: `[also-invalid`, Action: "dialog"}
	err = eh.Compile()
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestMatchErrorNoMatch(t *testing.T) {
	p := builtinSSH()
	compilePatterns(p)
	data := []byte("Connected to server successfully")
	eh := MatchError(p, data)
	if eh != nil {
		t.Errorf("expected no match, got %q", eh.Title)
	}
}

func TestExpandResumeArgs(t *testing.T) {
	template := []string{"--resume", "{session_id}"}
	state := map[string]string{"session_id": "abc123"}

	result := ExpandResumeArgs(template, state)
	if len(result) != 2 {
		t.Fatalf("expected 2 args, got %d", len(result))
	}
	if result[0] != "--resume" {
		t.Errorf("expected '--resume', got %q", result[0])
	}
	if result[1] != "abc123" {
		t.Errorf("expected 'abc123', got %q", result[1])
	}
}

func TestExpandResumeArgsEmpty(t *testing.T) {
	result := ExpandResumeArgs(nil, nil)
	if result != nil {
		t.Error("expected nil for nil template")
	}

	result = ExpandResumeArgs([]string{"--arg"}, nil)
	if result != nil {
		t.Error("expected nil when state is nil")
	}
}

func TestExpandMessage(t *testing.T) {
	msg := "Cannot reach {host} on port {port} as {user}"
	args := []string{"-p", "2222", "deploy@staging.example.com"}

	result := ExpandMessage(msg, args)
	if result != "Cannot reach staging.example.com on port 2222 as deploy" {
		t.Errorf("unexpected expansion: %q", result)
	}
}

func TestExpandMessageEmpty(t *testing.T) {
	result := ExpandMessage("hello {host}", nil)
	if result != "hello {host}" {
		t.Errorf("expected no expansion with nil args, got %q", result)
	}
}

func TestExtractConnectionVars(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want map[string]string
	}{
		{
			name: "user@host",
			args: []string{"user@host.com"},
			want: map[string]string{"user": "user", "host": "host.com"},
		},
		{
			name: "user@host with port flag",
			args: []string{"-p", "2222", "deploy@staging.com"},
			want: map[string]string{"user": "deploy", "host": "staging.com", "port": "2222"},
		},
		{
			name: "bare hostname",
			args: []string{"myserver.com"},
			want: map[string]string{"host": "myserver.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractConnectionVars(tt.args)
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("key %q: got %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

func TestLoadFromDir(t *testing.T) {
	dir := t.TempDir()

	// Write a test TOML plugin
	toml := `
[plugin]
name = "test-tool"
display_name = "Test Tool"
category = "tools"
description = "A test plugin"

[command]
cmd = "echo"
args = ["hello"]
detect = "echo ok"

[persistence]
strategy = "rerun"

[[error_handlers]]
pattern = "error occurred"
title = "Test Error"
message = "Something went wrong"
action = "dialog"

[[instances]]
name = "default"
display_name = "Default"
args = ["--default"]
`
	if err := os.WriteFile(filepath.Join(dir, "test-tool.toml"), []byte(toml), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}

	p := r.Get("test-tool")
	if p == nil {
		t.Fatal("test-tool plugin not loaded")
	}
	if p.DisplayName != "Test Tool" {
		t.Errorf("expected display name 'Test Tool', got %q", p.DisplayName)
	}
	if p.Command.Cmd != "echo" {
		t.Errorf("expected cmd 'echo', got %q", p.Command.Cmd)
	}
	if p.Persistence.Strategy != "rerun" {
		t.Errorf("expected strategy 'rerun', got %q", p.Persistence.Strategy)
	}
	if len(p.ErrorHandlers) != 1 {
		t.Fatalf("expected 1 error handler, got %d", len(p.ErrorHandlers))
	}
	if p.ErrorHandlers[0].Title != "Test Error" {
		t.Errorf("expected error title 'Test Error', got %q", p.ErrorHandlers[0].Title)
	}
	if len(p.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(p.Instances))
	}
	if p.Instances[0].Name != "default" {
		t.Errorf("expected instance name 'default', got %q", p.Instances[0].Name)
	}
}

func TestLoadFromDirOverride(t *testing.T) {
	dir := t.TempDir()

	// Write a TOML that overrides the built-in terminal plugin
	toml := `
[plugin]
name = "terminal"
display_name = "Custom Terminal"
category = "terminal"

[command]
cmd = "zsh"
shell_integration = true

[persistence]
strategy = "cwd_only"
`
	if err := os.WriteFile(filepath.Join(dir, "terminal.toml"), []byte(toml), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}

	p := r.Get("terminal")
	if p == nil {
		t.Fatal("terminal plugin missing after override")
	}
	if p.DisplayName != "Custom Terminal" {
		t.Errorf("expected override display name 'Custom Terminal', got %q", p.DisplayName)
	}
	if p.Command.Cmd != "zsh" {
		t.Errorf("expected override cmd 'zsh', got %q", p.Command.Cmd)
	}
}

func TestLoadFromDirNonExistent(t *testing.T) {
	r := NewRegistry()
	err := r.LoadFromDir("/nonexistent/path")
	if err != nil {
		t.Errorf("expected nil error for missing dir, got %v", err)
	}
}

func TestCategoryOrder(t *testing.T) {
	order := CategoryOrder()
	if len(order) != 4 {
		t.Fatalf("expected 4 categories, got %d", len(order))
	}
	if order[0].Key != "terminal" {
		t.Errorf("expected first category 'terminal', got %q", order[0].Key)
	}
}

func TestScrapePatternCompile(t *testing.T) {
	sp := ScrapePattern{
		Name:    "test",
		Pattern: `Session: (\w+)`,
	}
	sp.Compile()
	re := sp.Compiled()
	if re == nil {
		t.Fatal("compiled regex should not be nil")
	}
	// Should return same instance on second call
	re2 := sp.Compiled()
	if re != re2 {
		t.Error("expected same regex instance on second call")
	}
}

func TestErrorHandlerCompile(t *testing.T) {
	eh := ErrorHandler{
		Pattern: `error: (.+)`,
		Title:   "Error",
		Message: "Something happened",
		Action:  "dialog",
	}
	eh.Compile()
	re := eh.Compiled()
	if re == nil {
		t.Fatal("compiled regex should not be nil")
	}
}
