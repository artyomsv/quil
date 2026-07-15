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
			Strategy:    "cwd_only",
			GhostBuffer: true,
		},
		Instances: instances,
		IdleHandlers: []IdleHandler{
			{Pattern: `(?i)\[Y/n\]|\[y/N\]|\(yes/no\)|Continue\?|Proceed\?`, Title: "Waiting for confirmation", Severity: "warning"},
			{Pattern: `(?i)password:|passphrase:`, Title: "Waiting for password", Severity: "warning"},
			{Pattern: `(?i)(error|FAIL|fatal|panic|exception)`, Title: "Error detected", Severity: "error"},
		},
		Available: true, // always available
	}
}

// builtinTerminalWide returns the wide-canvas variant of the terminal
// plugin: identical shell detection and persistence, but the pane keeps
// its PTY/emulator on the window-sized canvas below min_native_cols (like
// AI panes), so squeezing a pane never cuts content — at the cost of
// output being formatted for the canvas width while previewed. Best for
// logs, watch loops, and long-running output; the native "terminal" stays
// the right choice for interactive work (tables and line editing format
// to the real pane width). See docs/features.md and
// techdebt/3-5-terminal-vt-resize-reflow.md for the trade-off.
func builtinTerminalWide() *PanePlugin {
	p := builtinTerminal()
	p.Name = "terminal-wide"
	p.DisplayName = "Terminal (keeps content on squeeze)"
	p.Description = "System shell on a window-sized canvas — resizing never cuts content; narrow panes show a cropped/wrapped preview. Best for logs and long-running output; use Terminal for interactive work."
	p.Display.WideCanvas = true
	return p
}

// builtinPlugins returns built-in plugin definitions that require Go runtime
// logic (e.g., shell detection). Static plugins are shipped as editable TOML
// files in ~/.quil/plugins/ — see defaults.go and defaults/*.toml.
func builtinPlugins() []*PanePlugin {
	return []*PanePlugin{
		builtinTerminal(),
		builtinTerminalWide(),
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
