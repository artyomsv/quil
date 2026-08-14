package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultPlugins_DetectsStalePlugins(t *testing.T) {
	dir := t.TempDir()

	// First run: creates fresh files — expect 0 stale.
	stale, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("first run: expected 0 stale plugins, got %d", len(stale))
	}

	// Downgrade claude-code.toml by writing content without schema_version.
	ccPath := filepath.Join(dir, "claude-code.toml")
	downgraded := []byte("[plugin]\nname = \"claude-code\"\ndisplay_name = \"Claude Code\"\ncategory = \"ai\"\n\n[command]\ncmd = \"claude\"\n")
	if err := os.WriteFile(ccPath, downgraded, 0600); err != nil {
		t.Fatalf("write downgraded: %v", err)
	}

	// Second run: should detect 1 stale plugin.
	stale, err = EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("second run: expected 1 stale plugin, got %d", len(stale))
	}

	sp := stale[0]
	if sp.Name != "claude-code" {
		t.Errorf("expected stale plugin name 'claude-code', got %q", sp.Name)
	}
	if sp.FilePath != ccPath {
		t.Errorf("expected FilePath %q, got %q", ccPath, sp.FilePath)
	}
	if !bytes.Equal(sp.UserData, downgraded) {
		t.Error("UserData does not match downgraded content")
	}
	if ParseSchemaVersion(sp.DefaultData) == 0 {
		t.Error("DefaultData should have a non-zero schema_version")
	}

	// Verify the file on disk was NOT overwritten.
	ondisk, err := os.ReadFile(ccPath)
	if err != nil {
		t.Fatalf("read after second run: %v", err)
	}
	if !bytes.Equal(ondisk, downgraded) {
		t.Error("stale file was overwritten — expected it to remain unchanged")
	}
}

func TestEnsureDefaultPlugins_CurrentVersionNotStale(t *testing.T) {
	dir := t.TempDir()

	// First run: creates fresh files at current schema version.
	_, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run: files already at current version — expect 0 stale.
	stale, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("second run: expected 0 stale plugins, got %d", len(stale))
	}
}

func TestEnsureDefaultPlugins_ClaudeCodeRecordsHistory(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "claude-code.toml"))
	if err != nil {
		t.Fatalf("load claude-code.toml: %v", err)
	}
	if !p.Command.RecordHistory {
		t.Fatal("expected claude-code Command.RecordHistory = true")
	}
}

// TestEnsureDefaultPlugins_ClaudeCodeOffersSessions guards the setup dialog's
// resume picker: without this opt-in the field never renders, and the feature
// silently disappears from a shipped build.
func TestEnsureDefaultPlugins_ClaudeCodeOffersSessions(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "claude-code.toml"))
	if err != nil {
		t.Fatalf("load claude-code.toml: %v", err)
	}
	if p.Command.Sessions != "claude" {
		t.Errorf("Command.Sessions = %q, want \"claude\"", p.Command.Sessions)
	}
	// The picker lists sessions for the directory the dialog selects, so the
	// CWD prompt is a prerequisite.
	if !p.Command.PromptsCWD {
		t.Error("sessions picker requires PromptsCWD, which is now false")
	}
}

// TestLoadPluginTOML_UnknownSessionsSource fails the load rather than silently
// ignoring a typo — same contract as an unknown discover mode.
func TestLoadPluginTOML_UnknownSessionsSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	content := `[plugin]
name = "bad"

[command]
cmd = "bad"
sessions = "opencode"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadPluginTOML(path); err == nil {
		t.Fatal("expected an error for an unknown sessions source, got nil")
	}
}

func TestEnsureDefaultPlugins_WritesLazygit(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "lazygit.toml"))
	if err != nil {
		t.Fatalf("load lazygit.toml: %v", err)
	}
	if p.Name != "lazygit" || p.Command.Cmd != "lazygit" {
		t.Errorf("name/cmd = %q/%q", p.Name, p.Command.Cmd)
	}
	if !p.Command.PromptsCWD || p.Command.Discover != "git" {
		t.Errorf("PromptsCWD=%v Discover=%q, want true/git", p.Command.PromptsCWD, p.Command.Discover)
	}
	if p.Persistence.Strategy != "rerun" || p.Persistence.GhostBuffer {
		t.Errorf("strategy=%q ghost=%v, want rerun/false", p.Persistence.Strategy, p.Persistence.GhostBuffer)
	}
	if len(p.Command.Toggles) != 1 || p.Command.Toggles[0].Name != "screen_mode_full" {
		t.Errorf("toggles = %+v", p.Command.Toggles)
	}
}

func TestEnsureDefaultPlugins_WritesK9s(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "k9s.toml"))
	if err != nil {
		t.Fatalf("load k9s.toml: %v", err)
	}
	if p.Name != "k9s" || p.Command.Cmd != "k9s" {
		t.Errorf("name/cmd = %q/%q", p.Name, p.Command.Cmd)
	}
	if p.Homepage != "https://github.com/derailed/k9s" {
		t.Errorf("Homepage = %q, want the k9s URL", p.Homepage)
	}
	// k9s is cluster-scoped, not directory-scoped: no CWD prompt. Discovery
	// is by kube context, so the setup dialog offers a context pick-list.
	if p.Command.PromptsCWD {
		t.Errorf("PromptsCWD = true, want false")
	}
	if p.Command.Discover != "kube" {
		t.Errorf("Discover = %q, want kube", p.Command.Discover)
	}
	if p.Persistence.Strategy != "rerun" || p.Persistence.GhostBuffer {
		t.Errorf("strategy=%q ghost=%v, want rerun/false", p.Persistence.Strategy, p.Persistence.GhostBuffer)
	}
	if len(p.Command.Toggles) != 2 {
		t.Fatalf("toggles = %+v, want 2 (readonly, start_pods)", p.Command.Toggles)
	}
	if p.Command.Toggles[0].Name != "readonly" || p.Command.Toggles[1].Name != "start_pods" {
		t.Errorf("toggle names = %q,%q", p.Command.Toggles[0].Name, p.Command.Toggles[1].Name)
	}
}

func TestEnsureDefaultPlugins_WritesLazysql(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "lazysql.toml"))
	if err != nil {
		t.Fatalf("load lazysql.toml: %v", err)
	}
	if p.Name != "lazysql" || p.Command.Cmd != "lazysql" {
		t.Errorf("name/cmd = %q/%q", p.Name, p.Command.Cmd)
	}
	if p.Homepage != "https://github.com/jorgerojas26/lazysql" {
		t.Errorf("Homepage = %q, want the lazysql URL", p.Homepage)
	}
	// Connection-scoped, not directory-scoped: no CWD prompt, no Quil-side
	// connection picker (the only launch arg lazysql takes is a credentialed DSN).
	if p.Command.PromptsCWD {
		t.Errorf("PromptsCWD = true, want false")
	}
	if p.Command.Discover != "" {
		t.Errorf("Discover = %q, want \"\"", p.Command.Discover)
	}
	if p.Persistence.Strategy != "rerun" || p.Persistence.GhostBuffer {
		t.Errorf("strategy=%q ghost=%v, want rerun/false", p.Persistence.Strategy, p.Persistence.GhostBuffer)
	}
	if len(p.Command.Toggles) != 1 || p.Command.Toggles[0].Name != "read_only" {
		t.Fatalf("toggles = %+v, want one read_only", p.Command.Toggles)
	}
	tog := p.Command.Toggles[0]
	if tog.Default {
		t.Errorf("read_only toggle default = true, want false")
	}
	if len(tog.ArgsWhenOn) != 1 || tog.ArgsWhenOn[0] != "--read-only" {
		t.Errorf("read_only ArgsWhenOn = %v, want [--read-only]", tog.ArgsWhenOn)
	}
}

// TestDefaultHunk_SpawnsTheDiffSubcommand pins the two properties that make
// the bundled hunk plugin actually run a diff viewer.
//
// `hunk` on its own prints help — the subcommand is what makes it a TUI, and
// it lives in the plugin's BASE args. resolveSpawnArgs REPLACES those base args
// with InstanceArgs whenever a pane has any (daemon.go), and the setup dialog
// builds InstanceArgs out of enabled `[[command.toggles]]`. So a toggle added
// to this plugin would spawn `hunk --whatever`, silently dropping `diff` and
// leaving the pane on a help screen. Both halves are asserted together because
// either one alone is satisfiable while the pane stays broken.
func TestDefaultHunk_SpawnsTheDiffSubcommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	r := NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	p := r.Get("hunk")
	if p == nil {
		t.Fatal("hunk plugin missing from the embedded defaults")
	}
	if p.Command.Cmd != "hunk" {
		t.Errorf("Cmd = %q, want hunk", p.Command.Cmd)
	}
	if len(p.Command.Args) != 1 || p.Command.Args[0] != "diff" {
		t.Errorf("Args = %v, want [diff]", p.Command.Args)
	}
	if len(p.Command.Toggles) != 0 {
		t.Errorf("Toggles = %v; a toggle's args replace the base args, dropping the diff subcommand", p.Command.Toggles)
	}
}

// TestDefaultHunk_ResolvesRepoFromTheWorkingDirectory: hunk has no --path/-C
// flag, so the repository it reviews comes from the spawn CWD alone. That makes
// prompts_cwd + discover="git" load-bearing rather than cosmetic: without them
// the pane opens wherever the daemon happens to be.
func TestDefaultHunk_ResolvesRepoFromTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	r := NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	p := r.Get("hunk")
	if p == nil {
		t.Fatal("hunk plugin missing from the embedded defaults")
	}
	if !p.Command.PromptsCWD {
		t.Error("PromptsCWD = false; the CWD is the only thing that scopes hunk to a repo")
	}
	if p.Command.Discover != "git" {
		t.Errorf("Discover = %q, want git", p.Command.Discover)
	}
}
