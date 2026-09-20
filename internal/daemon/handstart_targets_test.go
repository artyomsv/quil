package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

func targetRegistry(_ *testing.T, plugins ...*plugin.PanePlugin) []*plugin.PanePlugin {
	return plugins
}

func agentPlugin(name, cmd string, opts ...func(*plugin.PanePlugin)) *plugin.PanePlugin {
	p := &plugin.PanePlugin{Name: name, Available: true}
	p.Command.Cmd = cmd
	for _, o := range opts {
		o(p)
	}
	return p
}

// The set is derived from the spawn switch's three hook arms, not listed. A
// user's own plugin declaring sessions = "claude" must be covered with no code
// change here — that is the argument UsesClaudeSessions' doc records.
func TestHandStartTargets_DerivedFromTheHookArms(t *testing.T) {
	custom := agentPlugin("tclaude", "/opt/bin/tclaude")
	custom.Command.Sessions = "claude"
	shell := agentPlugin("terminal", "/bin/zsh")
	shell.Command.ShellIntegration = true

	reg := targetRegistry(t,
		agentPlugin("claude-code", "/usr/local/bin/claude"),
		agentPlugin(plugin.CodexPluginName, "codex"),
		agentPlugin("opencode", "opencode"),
		custom,
		shell,
		agentPlugin("lazygit", "lazygit"),
	)

	got := handStartTargets(reg)
	for _, want := range []string{"claude", "codex", "opencode", "tclaude"} {
		if _, ok := got[want]; !ok {
			t.Errorf("target set is missing %q", want)
		}
	}
	for _, unwanted := range []string{"lazygit", "zsh"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("target set wrongly includes %q", unwanted)
		}
	}
}

// The loop guard. A plugin whose own pane runs a shell would arm the same
// function in the pane it converted into, and fire again forever. The
// intersection being empty is what makes the loop structurally impossible, so
// it is asserted rather than assumed.
func TestHandStartTargets_NeverIntersectsShellIntegration(t *testing.T) {
	both := agentPlugin("hybrid", "/usr/local/bin/claude")
	both.Command.Sessions = "claude"
	both.Command.ShellIntegration = true

	if _, ok := handStartTargets(targetRegistry(t, both))["claude"]; ok {
		t.Fatal("a shell-integration plugin entered the intercept set; a converted " +
			"pane would arm the same function and convert itself again")
	}
}

// Converting to a pane whose binary the detect command could not find would
// replace a working hand-started agent with a pane that fails to spawn.
func TestHandStartTargets_ExcludesUnavailable(t *testing.T) {
	gone := agentPlugin("claude-code", "claude")
	gone.Available = false
	if len(handStartTargets(targetRegistry(t, gone))) != 0 {
		t.Fatal("an unavailable plugin entered the intercept set")
	}
}

// The user types a basename, and on Windows types it without .exe.
func TestHandStartTargets_KeyedByWhatTheUserTypes(t *testing.T) {
	reg := targetRegistry(t, agentPlugin("claude-code", `C:\Program Files\nodejs\Claude.EXE`))
	if _, ok := handStartTargets(reg)["claude"]; !ok {
		t.Fatalf("keys = %v, want a \"claude\" entry", handStartNames(reg))
	}
}

// QUIL_INTERCEPT lands in every warm shell's environment. An unstable order
// would make otherwise identical shells differ for no reason.
func TestHandStartNames_Sorted(t *testing.T) {
	reg := targetRegistry(t,
		agentPlugin("opencode", "opencode"),
		agentPlugin("claude-code", "claude"),
		agentPlugin(plugin.CodexPluginName, "codex"),
	)
	got := handStartNames(reg)
	want := []string{"claude", "codex", "opencode"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
}

func TestHandStartTargets_NoPlugins(t *testing.T) {
	if handStartTargets(nil) != nil || len(handStartNames(nil)) != 0 {
		t.Fatal("an empty plugin list should yield no targets")
	}
}

// Two shells must never share a token, or a marker from one pane would
// authenticate against another.
func TestNewInterceptToken_Distinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok := newInterceptToken()
		if tok == "" {
			t.Fatal("minted an empty token")
		}
		if seen[tok] {
			t.Fatalf("token %q minted twice", tok)
		}
		seen[tok] = true
	}
}
