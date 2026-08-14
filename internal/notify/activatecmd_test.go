package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The registry command is the ENTIRE surface a registered URI scheme can reach,
// so what it resolves to is worth a test that CI actually runs. These live in a
// platform-neutral file for that reason — the Windows-tagged tests beside them
// never compile on the Linux runner.
func TestActivateCommand_PrefersTheWindowlessHelper(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quil.exe")
	helper := filepath.Join(dir, ActivateHelperName)
	if err := os.WriteFile(helper, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := activateCommand(Options{Scheme: "quil-dev"}, exe, "/home/u/.quil")

	if !strings.Contains(got, ActivateHelperName) {
		t.Errorf("command = %q, want it to run %s", got, ActivateHelperName)
	}
	if strings.Contains(got, " activate ") {
		t.Errorf("command = %q, want the helper rather than the console subcommand", got)
	}
	// The helper cannot derive either value: Windows launches it with the
	// shell's environment, not the registering process's.
	if !strings.Contains(got, `--scheme "quil-dev"`) {
		t.Errorf("command = %q, want it to carry the scheme", got)
	}
	if !strings.Contains(got, `--home "/home/u/.quil"`) {
		t.Errorf("command = %q, want it to carry QUIL_HOME", got)
	}
	if !strings.HasSuffix(got, `"%1"`) {
		t.Errorf("command = %q, want it to end with the URI placeholder", got)
	}
}

// An install upgraded in place can have a new quil.exe beside a directory with
// no helper. A registry entry naming a file that does not exist is a click that
// does nothing at all, so the fallback has to hold.
func TestActivateCommand_FallsBackWhenHelperIsAbsent(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quil.exe")

	got := activateCommand(Options{Scheme: "quil"}, exe, dir)

	if !strings.Contains(got, `" activate "%1"`) {
		t.Errorf("command = %q, want the console subcommand fallback", got)
	}
	if strings.Contains(got, ActivateHelperName) {
		t.Errorf("command = %q, must not name a helper that is not there", got)
	}
}

// A trailing separator in QUIL_HOME must not reach the registry. Inside double
// quotes, CommandLineToArgvW reads a trailing \" as an escaped quote, so the
// home value swallows the URI that follows and the click silently does nothing.
func TestActivateCommand_StripsATrailingSeparatorFromHome(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quil.exe")
	if err := os.WriteFile(filepath.Join(dir, ActivateHelperName), []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := activateCommand(Options{Scheme: "quil"}, exe, `D:\quil\`)

	if strings.Contains(got, `\" "%1"`) {
		t.Errorf("command = %q, want no escaped quote before the URI placeholder", got)
	}
	if !strings.HasSuffix(got, `"%1"`) {
		t.Errorf("command = %q, want it to end with the URI placeholder", got)
	}
}

// A DIRECTORY named quil-activate.exe is not a program. Stat alone would accept
// it and register a command Windows cannot run.
func TestActivateCommand_IgnoresADirectoryNamedLikeTheHelper(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quil.exe")
	if err := os.Mkdir(filepath.Join(dir, ActivateHelperName), 0o755); err != nil {
		t.Fatal(err)
	}

	got := activateCommand(Options{Scheme: "quil"}, exe, dir)

	if strings.Contains(got, ActivateHelperName) {
		t.Errorf("command = %q, want the fallback — a directory cannot be executed", got)
	}
}

// The helper is looked for BESIDE the binary being registered, never on PATH: a
// dev tree and a real install must each register their own copy, or a dev toast
// click routes into the production TUI.
func TestActivateCommand_LooksBesideTheRegisteredBinary(t *testing.T) {
	install := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, ActivateHelperName), []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := activateCommand(Options{Scheme: "quil"}, filepath.Join(install, "quil.exe"), install)

	if strings.Contains(got, other) {
		t.Errorf("command = %q, want nothing from an unrelated directory", got)
	}
}
