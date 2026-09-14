package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func mcpSupported(agent string) bool {
	return agent == "claude-code" || agent == "codex" || agent == "opencode"
}

// mcpModelArgs translates a pane's configured model into the agent's own
// flag. Empty means the agent's default and yields nothing. For codex the
// flag is inserted before a "--" if one is present, because everything after
// it is the positional prompt. Claude Code and OpenCode take --model anywhere.
func mcpModelArgs(agent, model string, args []string) []string {
	if model == "" {
		return args
	}
	var flag []string
	switch agent {
	case "claude-code", "opencode":
		flag = []string{"--model", model}
	case "codex":
		flag = []string{"-m", model}
	default:
		return args
	}
	end := len(args)
	for i, arg := range args {
		if arg == "--" {
			end = i
			break
		}
	}
	out := append([]string(nil), args[:end]...)
	out = append(out, flag...)
	return append(out, args[end:]...)
}

// Use the matched sibling bridge, including the dev/debug suffix. A PATH
// lookup could select a production bridge talking to another workspace.
var mcpExeFn = func() (string, error) {
	daemon, err := quildExeFn()
	if err != nil {
		return "", err
	}
	name := filepath.Base(daemon)
	if !strings.HasPrefix(name, "quild") {
		return "", fmt.Errorf("cannot locate Quil MCP bridge beside %s", daemon)
	}
	path := filepath.Join(filepath.Dir(daemon), "quil"+strings.TrimPrefix(name, "quild"))
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", fmt.Errorf("Quil MCP bridge is missing: %s", path)
	}
	return path, nil
}

// These are argv elements, not shell snippets. JSON/TOML encoders preserve
// Windows paths and spaces without shell interpolation. The pane inherits
// QUIL_HOME and QUIL_PANE_ID through the existing spawn environment.
func mcpSpawn(agent string, args, env, codexServers []string) ([]string, []string, error) {
	bridgeArgs := []string{"mcp"}
	exe, err := mcpExeFn()
	if err != nil {
		return nil, nil, err
	}
	switch agent {
	case "claude-code":
		b, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"quil": map[string]any{
			"type": "stdio", "command": exe, "args": bridgeArgs,
		}}})
		if err != nil {
			return nil, nil, err
		}
		args = append([]string{"--strict-mcp-config", "--mcp-config", string(b)}, args...)
	case "codex":
		// Codex filters the environment inherited by stdio servers. Explicitly
		// forward the pane identity and daemon directory or the bridge cannot
		// resolve its caller (and a custom QUIL_HOME would reach another daemon).
		// Keep original transport definitions (including CLI-only servers),
		// then apply the disabling fields last. -c is global, including resume.
		end := len(args)
		for i, arg := range args {
			if arg == "--" {
				end = i
				break
			}
		}
		out := append([]string(nil), args[:end]...)
		overrides, err := mcpCodexConfig(exe, codexServers)
		if err != nil {
			return nil, nil, err
		}
		for _, override := range overrides {
			out = append(out, "-c", override)
		}
		args = append(out, args[end:]...)
	case "opencode":
		cfg := make(map[string]any)
		out := make([]string, 0, len(env))
		for _, item := range env {
			if data, ok := strings.CutPrefix(item, "OPENCODE_CONFIG_CONTENT="); ok {
				if err := json.Unmarshal([]byte(data), &cfg); err != nil {
					return nil, nil, fmt.Errorf("OpenCode config: %w", err)
				}
			} else {
				out = append(out, item)
			}
		}
		servers, _ := cfg["mcp"].(map[string]any)
		if servers == nil {
			servers = make(map[string]any)
		}
		servers["quil"] = map[string]any{"type": "local", "command": append([]string{exe}, bridgeArgs...), "enabled": true}
		cfg["mcp"] = servers
		b, err := json.Marshal(cfg)
		if err != nil {
			return nil, nil, err
		}
		env = append(out, "OPENCODE_CONFIG_CONTENT="+string(b))
	default:
		return nil, nil, fmt.Errorf("agent %q has no per-spawn MCP support", agent)
	}
	return args, env, nil
}

// Codex merges even a whole-table -c override with user/project configuration.
// List the effective server names without starting servers, then explicitly
// disable each one. A new name avoids merging a URL or credentials into our
// stdio server. Never log the probe output: it may contain server credentials.
var mcpCodexServersFn = func(command, cwd string, args, env []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := mcpCodexConfigArgs(args)
	probe = append(probe, "mcp", "list", "--json")
	cmd := exec.CommandContext(ctx, command, probe...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = time.Second
	hideGitWindow(cmd)
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect Codex MCP configuration (codex mcp list --json): %w", err)
	}
	var servers []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("Codex MCP configuration probe returned invalid JSON")
	}
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		names = append(names, server.Name)
	}
	return names, nil
}

// Preserve configuration selectors for the read-only probe, without passing
// interactive options or a resume/session/prompt positional argument to list.
func mcpCodexConfigArgs(args []string) (probe []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		flag, value, inline := strings.Cut(arg, "=")
		if strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
			flag, value, inline = "-c", arg[2:], true
		}
		switch flag {
		case "-c", "--config", "-p", "--profile", "-C", "--cd", "--enable", "--disable":
			part := []string{arg}
			if !inline && i+1 < len(args) {
				i++
				value = args[i]
				part = append(part, value)
			}
			probe = append(probe, part...)
		case "--strict-config":
			probe = append(probe, arg)
		}
	}
	return probe
}

var mcpCodexServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func mcpCodexConfig(exe string, servers []string) ([]string, error) {
	names := make(map[string]bool, len(servers))
	for _, name := range servers {
		// Codex splits -c keys on literal dots and keeps quote characters.
		// Refuse names it cannot address instead of leaving a server enabled.
		if !mcpCodexServerName.MatchString(name) {
			return nil, fmt.Errorf("cannot isolate Codex MCP server %q: rename it using letters, digits, underscores or hyphens", name)
		}
		names[name] = true
	}
	bridge := "quil_pane"
	for i := 2; names[bridge]; i++ {
		bridge = "quil_pane_" + strconv.Itoa(i)
	}
	entries := make([]string, 0, len(names)+1)
	for name := range names {
		entries = append(entries, "mcp_servers."+name+".enabled=false")
	}
	sort.Strings(entries)
	quotedArgs := []string{"mcp"}
	for i, arg := range quotedArgs {
		quotedArgs[i] = strconv.Quote(arg)
	}
	entries = append(entries, "mcp_servers."+bridge+"={enabled=true,command="+strconv.Quote(exe)+`,args=[`+strings.Join(quotedArgs, ",")+`],env_vars=["QUIL_HOME","QUIL_PANE_ID"]}`)
	return entries, nil
}
