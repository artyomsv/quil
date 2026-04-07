package shellinit

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed scripts/*
var scripts embed.FS

// ShellConfig holds modified spawn parameters for a shell with OSC 7 injection.
type ShellConfig struct {
	Cmd  string   // shell command (same as original if unchanged)
	Args []string // arguments to pass to the shell
	Env  []string // additional env vars to merge with os.Environ()
}

// EnsureInitDir writes embedded init scripts to quilDir/shellinit/.
// Overwrites existing scripts to stay current with the binary version.
func EnsureInitDir(quilDir string) error {
	base := filepath.Join(quilDir, "shellinit")
	zshDir := filepath.Join(base, "zsh")

	if err := os.MkdirAll(zshDir, 0700); err != nil {
		return fmt.Errorf("create shellinit dirs: %w", err)
	}

	files := map[string]string{
		filepath.Join(base, "bash-init.sh"):  "scripts/bash-init.sh",
		filepath.Join(base, "pwsh-init.ps1"): "scripts/pwsh-init.ps1",
		filepath.Join(zshDir, ".zshenv"):     "scripts/zsh-env.sh",
		filepath.Join(zshDir, ".zshrc"):      "scripts/zsh-init.sh",
	}

	for dst, src := range files {
		data, err := scripts.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}

	return nil
}

// Configure returns modified spawn parameters for the given shell path.
// Returns nil if the shell doesn't need injection (fish, sh, cmd.exe, unknown).
func Configure(shell, quilDir string) *ShellConfig {
	base := filepath.Join(quilDir, "shellinit")
	name := shellName(shell)

	switch name {
	case "bash":
		return &ShellConfig{
			Cmd:  shell,
			Args: []string{"--rcfile", filepath.Join(base, "bash-init.sh")},
		}

	case "zsh":
		zshDir := filepath.Join(base, "zsh")
		origZdotdir := os.Getenv("ZDOTDIR")
		return &ShellConfig{
			Cmd: shell,
			Env: []string{
				"QUIL_ORIG_ZDOTDIR=" + origZdotdir,
				"ZDOTDIR=" + zshDir,
			},
		}

	case "pwsh", "powershell":
		return &ShellConfig{
			Cmd:  shell,
			Args: []string{"-NoProfile", "-NoLogo", "-NoExit", "-File", filepath.Join(base, "pwsh-init.ps1")},
		}

	default:
		return nil
	}
}

// shellName extracts the base shell name from a path, normalized to lowercase
// without extension. E.g., "/usr/bin/bash" -> "bash", "C:\...\pwsh.exe" -> "pwsh".
func shellName(shell string) string {
	name := filepath.Base(shell)
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSuffix(name, ".EXE")
	return strings.ToLower(name)
}
