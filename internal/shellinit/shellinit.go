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
//
// intercept names the agent binaries the init script should shadow with a
// function, and token authenticates the marker that function emits. Both are
// passed through the environment and both must be non-empty for the scripts to
// arm at all — an empty pair is how a caller says "shell integration, but no
// interception", which is what every path except a terminal pane wants.
//
// Fish is absent from the switch and therefore gets no interception, the same
// way it gets no OSC 133. Fish emits OSC 7 on its own, which is the whole of
// what "native fish integration" means; nothing here reaches it.
func Configure(shell, quilDir string, intercept []string, token string) *ShellConfig {
	base := filepath.Join(quilDir, "shellinit")
	name := shellName(shell)

	switch name {
	case "bash":
		return &ShellConfig{
			Cmd:  shell,
			Args: []string{"--rcfile", filepath.Join(base, "bash-init.sh")},
			Env:  interceptEnv(intercept, token),
		}

	case "zsh":
		zshDir := filepath.Join(base, "zsh")
		origZdotdir := os.Getenv("ZDOTDIR")
		return &ShellConfig{
			Cmd: shell,
			Env: append([]string{
				"QUIL_ORIG_ZDOTDIR=" + origZdotdir,
				"ZDOTDIR=" + zshDir,
			}, interceptEnv(intercept, token)...),
		}

	case "pwsh", "powershell":
		return &ShellConfig{
			Cmd:  shell,
			Args: []string{"-NoProfile", "-NoLogo", "-NoExit", "-File", filepath.Join(base, "pwsh-init.ps1")},
			Env:  interceptEnv(intercept, token),
		}

	default:
		return nil
	}
}

// interceptEnv is the pair the init scripts gate on. It answers nil unless both
// halves are present: a name list with no token would arm functions whose
// marker the daemon must reject, which is a shell that pauses for a second
// before every agent launch and converts nothing.
//
// A name carrying a comma would split into two bogus names, and one carrying a
// character outside the scripts' own validation would be skipped there anyway;
// both are dropped here so the two ends cannot disagree about the list.
func interceptEnv(intercept []string, token string) []string {
	if token == "" || len(intercept) == 0 {
		return nil
	}
	names := make([]string, 0, len(intercept))
	for _, n := range intercept {
		if n != "" && interceptNameOK(n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []string{
		"QUIL_INTERCEPT=" + strings.Join(names, ","),
		"QUIL_INTERCEPT_TOKEN=" + token,
	}
}

// interceptNameOK mirrors the scripts' own ^[A-Za-z0-9._-]+$ check. The scripts
// validate because the name reaches `eval`; this validates so that a name the
// scripts would silently skip never appears in the list the daemon believes is
// armed.
func interceptNameOK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// shellName extracts the base shell name from a path, normalized to lowercase
// without extension. E.g., "/usr/bin/bash" -> "bash", "C:\...\pwsh.exe" -> "pwsh".
func shellName(shell string) string {
	name := filepath.Base(shell)
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSuffix(name, ".EXE")
	return strings.ToLower(name)
}
