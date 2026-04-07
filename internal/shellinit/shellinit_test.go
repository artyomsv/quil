package shellinit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureInitDir(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureInitDir(dir); err != nil {
		t.Fatalf("EnsureInitDir: %v", err)
	}

	wantFiles := []string{
		filepath.Join(dir, "shellinit", "bash-init.sh"),
		filepath.Join(dir, "shellinit", "pwsh-init.ps1"),
		filepath.Join(dir, "shellinit", "zsh", ".zshenv"),
		filepath.Join(dir, "shellinit", "zsh", ".zshrc"),
	}

	for _, f := range wantFiles {
		info, err := os.Stat(f)
		if err != nil {
			t.Errorf("missing file %s: %v", f, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("file %s is empty", f)
		}
	}
}

func TestEnsureInitDirIdempotent(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureInitDir(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureInitDir(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestConfigureBash(t *testing.T) {
	dir := t.TempDir()
	cfg := Configure("/usr/bin/bash", dir)
	if cfg == nil {
		t.Fatal("expected config for bash, got nil")
	}
	if cfg.Cmd != "/usr/bin/bash" {
		t.Errorf("Cmd = %q, want /usr/bin/bash", cfg.Cmd)
	}
	if len(cfg.Args) != 2 || cfg.Args[0] != "--rcfile" {
		t.Errorf("Args = %v, want [--rcfile <path>]", cfg.Args)
	}
	wantPath := filepath.Join(dir, "shellinit", "bash-init.sh")
	if cfg.Args[1] != wantPath {
		t.Errorf("rcfile path = %q, want %q", cfg.Args[1], wantPath)
	}
	if len(cfg.Env) != 0 {
		t.Errorf("Env = %v, want empty", cfg.Env)
	}
}

func TestConfigureZsh(t *testing.T) {
	dir := t.TempDir()
	cfg := Configure("/bin/zsh", dir)
	if cfg == nil {
		t.Fatal("expected config for zsh, got nil")
	}
	if cfg.Cmd != "/bin/zsh" {
		t.Errorf("Cmd = %q, want /bin/zsh", cfg.Cmd)
	}
	if len(cfg.Args) != 0 {
		t.Errorf("Args = %v, want empty", cfg.Args)
	}
	if len(cfg.Env) != 2 {
		t.Fatalf("Env has %d entries, want 2", len(cfg.Env))
	}

	wantZdotdir := filepath.Join(dir, "shellinit", "zsh")
	foundZdotdir := false
	foundOrig := false
	for _, e := range cfg.Env {
		if e == "ZDOTDIR="+wantZdotdir {
			foundZdotdir = true
		}
		if len(e) >= len("QUIL_ORIG_ZDOTDIR=") && e[:len("QUIL_ORIG_ZDOTDIR=")] == "QUIL_ORIG_ZDOTDIR=" {
			foundOrig = true
		}
	}
	if !foundZdotdir {
		t.Errorf("missing ZDOTDIR=%s in Env %v", wantZdotdir, cfg.Env)
	}
	if !foundOrig {
		t.Errorf("missing QUIL_ORIG_ZDOTDIR in Env %v", cfg.Env)
	}
}

func TestConfigurePwsh(t *testing.T) {
	for _, shell := range []string{"pwsh", "pwsh.exe", "powershell", "powershell.exe"} {
		cfg := Configure(shell, t.TempDir())
		if cfg == nil {
			t.Errorf("expected config for %s, got nil", shell)
			continue
		}
		if len(cfg.Args) < 4 {
			t.Errorf("Args for %s = %v, want at least 4 args", shell, cfg.Args)
			continue
		}
		if cfg.Args[0] != "-NoProfile" {
			t.Errorf("Args[0] for %s = %q, want -NoProfile", shell, cfg.Args[0])
		}
	}
}

func TestConfigureFish(t *testing.T) {
	cfg := Configure("/usr/bin/fish", t.TempDir())
	if cfg != nil {
		t.Errorf("expected nil for fish, got %+v", cfg)
	}
}

func TestConfigureUnknown(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "cmd.exe", "/usr/bin/unknown"} {
		cfg := Configure(shell, t.TempDir())
		if cfg != nil {
			t.Errorf("expected nil for %s, got %+v", shell, cfg)
		}
	}
}

func TestShellName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/usr/bin/bash", "bash"},
		{"/bin/zsh", "zsh"},
		{"/usr/bin/fish", "fish"},
		{"/bin/sh", "sh"},
		{"bash", "bash"},
	}
	for _, tt := range tests {
		got := shellName(tt.input)
		if got != tt.want {
			t.Errorf("shellName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
