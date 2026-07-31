//go:build linux || darwin || freebsd

package pty

import (
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

	if got := envValue(childEnv(nil), "TERM"); got != defaultTERM {
		t.Errorf("TERM = %q, want %q — a pane child with no TERM kills every tcell tool", got, defaultTERM)
	}
}

// An inherited value describes the attached terminal accurately, so replacing
// it would be a behaviour change for local users to fix a problem they do not
// have.
func TestChildEnv_NeverOverridesAnInheritedTERM(t *testing.T) {
	t.Setenv("TERM", "screen-256color")

	if got := envValue(childEnv(nil), "TERM"); got != "screen-256color" {
		t.Errorf("TERM = %q, want the inherited screen-256color", got)
	}
}

// The extras are the plugin's own vars (QUIL_PANE_ID, hook config, …). They
// must survive, and a plugin that sets TERM itself must win — execve takes the
// last occurrence, so ordering is the whole mechanism here.
func TestChildEnv_KeepsExtrasLastSoTheyWin(t *testing.T) {
	t.Setenv("TERM", "")

	env := childEnv([]string{"QUIL_PANE_ID=pane-1", "TERM=vt100"})

	if got := envValue(env, "QUIL_PANE_ID"); got != "pane-1" {
		t.Errorf("QUIL_PANE_ID = %q, want pane-1 — plugin env was dropped", got)
	}
	if got := envValue(env, "TERM"); got != "vt100" {
		t.Errorf("TERM = %q, want vt100 — a plugin-supplied value must override the default", got)
	}
}

// A pane with no plugin vars at all still needs TERM. The previous code only
// assigned cmd.Env when the extra slice was non-empty, so exactly those panes
// inherited the daemon's environment verbatim — including its missing TERM.
func TestChildEnv_NoExtrasStillGetsTERM(t *testing.T) {
	t.Setenv("TERM", "")

	if got := envValue(childEnv([]string{}), "TERM"); got != defaultTERM {
		t.Errorf("TERM = %q, want %q for a pane with no plugin env", got, defaultTERM)
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
