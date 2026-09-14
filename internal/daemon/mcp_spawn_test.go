package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestMCPSpawn_PreservesHooksAndQuotesPaths(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	old := mcpExeFn
	mcpExeFn = func() (string, error) { return `C:\Program Files\Quil\quil-dev.exe`, nil }
	t.Cleanup(func() { mcpExeFn = old })
	for _, agent := range []string{"claude-code", "codex", "opencode"} {
		args, env, err := mcpSpawn(agent, []string{"existing"}, []string{`OPENCODE_CONFIG_CONTENT={"plugin":["hook.js"],"mcp":{"other":{"type":"remote","url":"https://example.com/mcp"}}}`, "KEEP=yes"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(args, " "), "existing") {
			t.Fatal("lost argv")
		}
		if agent == "claude-code" && args[0] != "--strict-mcp-config" {
			t.Fatal("Claude inherited user MCP servers", args)
		}
		if agent == "codex" && !strings.Contains(strings.Join(args, " "), `env_vars=["QUIL_HOME","QUIL_PANE_ID"]`) {
			t.Fatal("Codex MCP must inherit the pane identity and daemon home")
		}
		if agent == "opencode" {
			found := false
			for _, v := range env {
				if data, ok := strings.CutPrefix(v, "OPENCODE_CONFIG_CONTENT="); ok {
					var cfg map[string]any
					if err := json.Unmarshal([]byte(data), &cfg); err != nil {
						t.Fatal(err)
					}
					if cfg["plugin"] == nil || cfg["mcp"] == nil {
						t.Fatal(cfg)
					}
					servers := cfg["mcp"].(map[string]any)
					if servers["other"] == nil || servers["quil"] == nil {
						t.Fatal("lost existing MCP server", cfg)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("no OpenCode config")
			}
		} else if !strings.Contains(strings.Join(args, " "), "quil") {
			t.Fatal(args)
		}
	}
}

func stubMCPCodexProbe(t *testing.T) {
	t.Helper()
	old := mcpCodexServersFn
	mcpCodexServersFn = func(_, _ string, _, _ []string) ([]string, error) { return []string{"inherited"}, nil }
	t.Cleanup(func() { mcpCodexServersFn = old })
}

func TestMCPCodexConfig_DisablesInheritedServersWithoutMergingBridge(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	const exe = `C:\Program Files\Quil\quil-dev.exe`
	servers := []string{"quil", "quil_pane", "quil_pane_2", "with-hyphen"}
	var cfg struct {
		Servers map[string]struct {
			Enabled bool
			Command string
			Args    []string
			EnvVars []string `toml:"env_vars"`
		} `toml:"mcp_servers"`
	}
	overrides, err := mcpCodexConfig(exe, servers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(strings.Join(overrides, "\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range servers {
		server, ok := cfg.Servers[name]
		if !ok || server.Enabled || server.Command != "" {
			t.Fatal("inherited server not disabled", name, server)
		}
	}
	bridge := cfg.Servers["quil_pane_3"]
	if len(cfg.Servers) != len(servers)+1 || !bridge.Enabled || bridge.Command != exe || !reflect.DeepEqual(bridge.Args, []string{"mcp"}) || !reflect.DeepEqual(bridge.EnvVars, []string{"QUIL_HOME", "QUIL_PANE_ID"}) {
		t.Fatal(cfg)
	}
	for _, name := range []string{"with.dot", `with"quote`, ""} {
		if _, err := mcpCodexConfig(exe, []string{name}); err == nil {
			t.Fatal("unaddressable server accepted", name)
		}
	}
}

func TestMCPCodexConfigArgs_PreservesSelectors(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	for _, override := range [][]string{{"-c", "mcp_servers.quil.enabled=true"}, {"--config=mcp_servers={}"}, {`-c"mcp_servers".quil.enabled=true`}} {
		args := append([]string{"-c", "hooks={}", "--profile", "work", "-C", "/project"}, override...)
		probe := mcpCodexConfigArgs(append(args, "resume", "session"))
		if !reflect.DeepEqual(probe, args) {
			t.Fatal(probe)
		}
	}
}

func TestSpawnPane_MCPCodexProbeFailure_RefusesLaunch(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := newTestDaemon(t)
	registerShippedPlugins(t, d)
	d.registry.Get("codex").Available = true
	stubMCPCodexProbe(t)
	mcpCodexServersFn = func(_, _ string, _, _ []string) ([]string, error) { return nil, errors.New("probe failed") }
	fake := &fakeSession{}
	err := d.spawnPane(&Pane{ID: "pane-f10af10a", Type: "codex", QuilMCP: true, CWD: t.TempDir()}, fake, false)
	if err == nil || !strings.Contains(err.Error(), "probe failed") || fake.started {
		t.Fatal("probe failure did not prevent agent launch", err)
	}
}

// Opt in with a native Codex binary; list only, never launch an MCP server or
// agent. The real CLI test catches configuration merge semantics a TOML parser
// cannot model. CODEX_HOME is always an empty temporary fixture.
func TestMCPCodexProbe_InstalledCLI(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	binary := os.Getenv("QUIL_TEST_CODEX")
	if binary == "" {
		t.Skip("set QUIL_TEST_CODEX to a native Codex binary")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.quil_pane]\nurl = \"https://example.invalid/mcp\"\n[mcp_servers.inherited]\ncommand = \"must-not-run\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	original := []string{"-c", `mcp_servers.from_args={command="must-not-run"}`}
	servers, err := mcpCodexServersFn(binary, home, original, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := mcpExeFn
	mcpExeFn = func() (string, error) { return "fixture-quil", nil }
	t.Cleanup(func() { mcpExeFn = old })
	args, _, err := mcpSpawn("codex", original, nil, servers)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, append(args, "mcp", "list", "--json")...)
	cmd.Dir = home
	hideGitWindow(cmd)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Name    string
		Enabled bool
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal("invalid JSON")
	}
	if len(got) != 4 {
		t.Fatalf("want 3 disabled and 1 pane server, got %d", len(got))
	}
	for _, server := range got {
		if server.Enabled != (server.Name == "quil_pane_2") {
			t.Fatal(server)
		}
	}
}

func TestMCPModelArgs_PerAgentFlagBeforePositional(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cases := []struct {
		agent string
		in    []string
		want  []string
	}{
		{"claude-code", []string{"--dangerously-skip-permissions"}, []string{"--dangerously-skip-permissions", "--model", "m1"}},
		{"opencode", nil, []string{"--model", "m1"}},
		{"codex", []string{"--full-auto", "--", "resume"}, []string{"--full-auto", "-m", "m1", "--", "resume"}},
		{"terminal", []string{"-l"}, []string{"-l"}},
	}
	for _, c := range cases {
		if got := mcpModelArgs(c.agent, "m1", c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: got %v want %v", c.agent, got, c.want)
		}
		if got := mcpModelArgs(c.agent, "", c.in); !reflect.DeepEqual(got, c.in) {
			t.Fatalf("%s: empty model changed args: %v", c.agent, got)
		}
	}
}
