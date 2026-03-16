package plugin

import (
	"os"
	"os/exec"
	"runtime"
)

// builtinTerminal returns the terminal plugin definition.
func builtinTerminal() *PanePlugin {
	instances := detectShells()
	return &PanePlugin{
		Name:        "terminal",
		DisplayName: "Terminal",
		Category:    "terminal",
		Description: "System shell",
		Command: CommandConfig{
			Cmd:              defaultShell(),
			ShellIntegration: true,
		},
		Persistence: PersistenceConfig{
			Strategy: "cwd_only",
		},
		Instances: instances,
		Available: true, // always available
	}
}

// builtinClaudeCode returns the Claude Code plugin definition.
func builtinClaudeCode() *PanePlugin {
	return &PanePlugin{
		Name:        "claude-code",
		DisplayName: "Claude Code",
		Category:    "ai",
		Description: "AI coding assistant",
		Command: CommandConfig{
			Cmd:       "claude",
			DetectCmd: "claude --version",
		},
		Persistence: PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--resume", "{session_id}"},
		},
		ErrorHandlers: []ErrorHandler{
			{
				Pattern: `(?i)error.*API key not found|ANTHROPIC_API_KEY.*not set`,
				Title:   "API Key Missing",
				Message: "Set ANTHROPIC_API_KEY in your environment or run 'claude auth'.",
				Action:  "dialog",
			},
		},
	}
}

// builtinStripe returns the Stripe CLI plugin definition.
func builtinStripe() *PanePlugin {
	return &PanePlugin{
		Name:        "stripe",
		DisplayName: "Stripe",
		Category:    "tools",
		Description: "Stripe webhook listener",
		Command: CommandConfig{
			Cmd:       "stripe",
			Args:      []string{"listen"},
			DetectCmd: "stripe --version",
		},
		Persistence: PersistenceConfig{
			Strategy: "rerun",
		},
		ErrorHandlers: []ErrorHandler{
			{
				Pattern: `not logged in|login required`,
				Title:   "Stripe Authentication Required",
				Message: "Run 'stripe login' in a terminal pane first.",
				Action:  "dialog",
			},
		},
	}
}

// builtinSSH returns the SSH plugin definition.
func builtinSSH() *PanePlugin {
	return &PanePlugin{
		Name:        "ssh",
		DisplayName: "SSH",
		Category:    "remote",
		Description: "Remote SSH connection",
		Command: CommandConfig{
			Cmd:       "ssh",
			DetectCmd: "ssh -V",
		},
		Persistence: PersistenceConfig{
			Strategy: "rerun",
		},
		ErrorHandlers: []ErrorHandler{
			{
				Pattern: `Permission denied \(publickey`,
				Title:   "SSH Authentication Failed",
				Message: "SSH key not configured for this host.\n\n1. Generate:  ssh-keygen -t ed25519\n2. Copy key:  ssh-copy-id user@host\n3. Retry",
				Action:  "dialog",
			},
			{
				Pattern: `Host key verification failed`,
				Title:   "Unknown Host",
				Message: "Run: ssh-keyscan <host> >> ~/.ssh/known_hosts",
				Action:  "dialog",
			},
			{
				Pattern: `Connection refused|No route to host`,
				Title:   "Connection Failed",
				Message: "Cannot reach host. Check that the server is running and the address is correct.",
				Action:  "dialog",
			},
		},
	}
}

// builtinPlugins returns all built-in plugin definitions.
func builtinPlugins() []*PanePlugin {
	return []*PanePlugin{
		builtinTerminal(),
		builtinClaudeCode(),
		builtinStripe(),
		builtinSSH(),
	}
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("pwsh.exe"); err == nil {
			return ps
		}
		return "cmd.exe"
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// detectShells returns InstanceConfig entries for shells found on the system.
func detectShells() []InstanceConfig {
	var instances []InstanceConfig

	type shellDef struct {
		name, display, cmd string
	}

	candidates := []shellDef{
		{"bash", "Bash", "bash"},
		{"zsh", "Zsh", "zsh"},
		{"fish", "Fish", "fish"},
		{"pwsh", "PowerShell", "pwsh"},
		{"cmd", "Command Prompt", "cmd.exe"},
	}
	if runtime.GOOS == "windows" {
		candidates = []shellDef{
			{"pwsh", "PowerShell", "pwsh.exe"},
			{"powershell", "Windows PowerShell", "powershell.exe"},
			{"cmd", "Command Prompt", "cmd.exe"},
			{"bash", "Git Bash", "bash"},
		}
	}

	for _, c := range candidates {
		if _, err := exec.LookPath(c.cmd); err == nil {
			instances = append(instances, InstanceConfig{
				Name:        c.name,
				DisplayName: c.display,
				Args:        []string{}, // use default shell args
			})
		}
	}

	return instances
}
