//go:build linux || darwin || freebsd

package pty

import (
	"os/exec"
	"strings"
	"testing"
)

// TestChildEnv_SuppliesTERMWhenAbsent pins the remote-mode fix.
//
// `ssh -T` allocates no TTY and exports no TERM, so a daemon started through
// `quil --stdio` has none and every pane child inherits the gap. tcell-based
// tools — k9s and lazysql among the shipped plugins — exit 1 within
// milliseconds without it, which presents as a pane that opens and instantly
// dies rather than as a missing variable.
func TestChildEnv_SuppliesTERMWhenAbsent(t *testing.T) {
	t.Setenv("TERM", "")

	if got := envValue(childEnv("", nil), "TERM"); got != defaultTERM {
		t.Errorf("TERM = %q, want %q — a pane child with no TERM kills every tcell tool", got, defaultTERM)
	}
}

// An inherited value describes the attached terminal accurately, so replacing
// it would be a behaviour change for local users to fix a problem they do not
// have.
func TestChildEnv_NeverOverridesAnInheritedTERM(t *testing.T) {
	t.Setenv("TERM", "screen-256color")

	if got := envValue(childEnv("", nil), "TERM"); got != "screen-256color" {
		t.Errorf("TERM = %q, want the inherited screen-256color", got)
	}
}

// The extras are the plugin's own vars (QUIL_PANE_ID, hook config, …). They must
// survive, and a plugin that sets TERM itself must win.
//
// This asserts SLICE ORDER only. envValue encodes the same last-wins rule, so on
// its own it cannot tell a correct precedence from a broken one — see
// TestChildEnv_PluginValueReachesTheChild, which checks what a real child
// actually receives.
func TestChildEnv_KeepsExtrasLastSoTheyWin(t *testing.T) {
	t.Setenv("TERM", "")

	env := childEnv("", []string{"QUIL_PANE_ID=pane-1", "TERM=vt100"})

	if got := envValue(env, "QUIL_PANE_ID"); got != "pane-1" {
		t.Errorf("QUIL_PANE_ID = %q, want pane-1 — plugin env was dropped", got)
	}
	if got := envValue(env, "TERM"); got != "vt100" {
		t.Errorf("TERM = %q, want vt100 — a plugin-supplied value must override the default", got)
	}
}

// TestChildEnv_PluginValueReachesTheChild closes the gap the ordering test
// cannot: it runs a real child and asks what IT sees.
//
// The precedence comes from os/exec, which runs dedupEnv over Cmd.Env and keeps
// the last occurrence — not from execve, which passes duplicates through and
// leaves the answer to the consumer (glibc and musl getenv() both return the
// FIRST match). So a future change that hands this slice to syscall.Exec or
// posix_spawn would invert plugin-vs-default precedence, and only a test that
// goes through the real spawn path would notice.
func TestChildEnv_PluginValueReachesTheChild(t *testing.T) {
	t.Setenv("TERM", "")

	cmd := exec.Command("sh", "-c", `printf %s "$TERM"`)
	cmd.Env = childEnv("", []string{"TERM=vt100"})

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the probe child: %v", err)
	}
	if string(out) != "vt100" {
		t.Errorf("child saw TERM=%q, want vt100 — the default outranked the plugin's value", out)
	}
}

// A pane with no plugin vars at all still needs TERM. The previous code only
// assigned cmd.Env when the extra slice was non-empty, so exactly those panes
// inherited the daemon's environment verbatim — including its missing TERM.
func TestChildEnv_NoExtrasStillGetsTERM(t *testing.T) {
	t.Setenv("TERM", "")

	if got := envValue(childEnv("", []string{}), "TERM"); got != defaultTERM {
		t.Errorf("TERM = %q, want %q for a pane with no plugin env", got, defaultTERM)
	}
}

// TestChildEnv_SetsPWDToThePaneDirectory pins what os/exec stopped doing for us.
//
// exec sets PWD from Cmd.Dir only when Cmd.Env is nil (go.dev/issue/50599).
// Panes whose plugin contributes no variables used to hit exactly that case and
// got the fixup for free; assigning Env unconditionally took it away from them
// without any test noticing, leaving them with the daemon's PWD — under
// `quil --remote`, ssh's login directory.
func TestChildEnv_SetsPWDToThePaneDirectory(t *testing.T) {
	dir := t.TempDir()

	if got := envValue(childEnv(dir, nil), "PWD"); got != dir {
		t.Errorf("PWD = %q, want %q — a child trusting $PWD resolves relative paths against the wrong directory", got, dir)
	}
}

// A pane with no directory of its own inherits the daemon's, so there is
// nothing truthful to say and the variable must be left exactly as inherited.
// Writing an empty or guessed PWD would be worse than the inherited one.
func TestChildEnv_NoCWDLeavesPWDAlone(t *testing.T) {
	t.Setenv("PWD", "/inherited/from/daemon")

	if got := envValue(childEnv("", nil), "PWD"); got != "/inherited/from/daemon" {
		t.Errorf("PWD = %q, want the inherited value untouched", got)
	}
}

// Same last-wins rule as TERM: a plugin that sets PWD deliberately must not be
// overridden by the directory the pane happens to have been opened in.
func TestChildEnv_PluginSuppliedPWDWins(t *testing.T) {
	dir := t.TempDir()

	env := childEnv(dir, []string{"PWD=/set/by/plugin"})

	if got := envValue(env, "PWD"); got != "/set/by/plugin" {
		t.Errorf("PWD = %q, want the plugin's value", got)
	}
}

// envValue returns the LAST value for key, matching execve's precedence.
func envValue(env []string, key string) string {
	prefix := key + "="
	out := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = strings.TrimPrefix(kv, prefix)
		}
	}
	return out
}
