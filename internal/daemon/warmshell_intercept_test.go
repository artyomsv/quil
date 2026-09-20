package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/shellinit"
)

// newWarmShellPool copies its config field by field so the pool owns its
// slices, and Intercept was left out of that copy. Nothing failed: the pool
// started, shells started, panes claimed them — and not one was armed, so every
// hand-started agent ran as typed and the feature was invisible.
//
// A field-by-field copy has no compiler check, so this test is the check.
func TestNewWarmShellPool_CarriesEveryConfigField(t *testing.T) {
	in := warmPoolShellConfig{
		Cmd:       "/bin/zsh",
		Args:      []string{"-l"},
		Env:       []string{"ZDOTDIR=/tmp/z"},
		Intercept: []string{"claude", "codex"},
	}
	p := newWarmShellPool(in, 1)
	t.Cleanup(p.Stop)

	if p.cfg.Cmd != in.Cmd {
		t.Errorf("Cmd = %q, want %q", p.cfg.Cmd, in.Cmd)
	}
	if strings.Join(p.cfg.Args, ",") != strings.Join(in.Args, ",") {
		t.Errorf("Args = %v, want %v", p.cfg.Args, in.Args)
	}
	if strings.Join(p.cfg.Env, ",") != strings.Join(in.Env, ",") {
		t.Errorf("Env = %v, want %v", p.cfg.Env, in.Env)
	}
	if strings.Join(p.cfg.Intercept, ",") != strings.Join(in.Intercept, ",") {
		t.Fatalf("Intercept = %v, want %v — a warm shell would start unarmed and "+
			"every hand-started agent would run as typed", p.cfg.Intercept, in.Intercept)
	}
}

// The pool must own its slices: the caller's backing array must not be shared,
// or a later append by the caller would mutate what the shells are started with.
func TestNewWarmShellPool_DoesNotAliasTheCallersSlices(t *testing.T) {
	names := []string{"claude"}
	p := newWarmShellPool(warmPoolShellConfig{Cmd: "/bin/zsh", Intercept: names}, 1)
	t.Cleanup(p.Stop)

	names[0] = "mutated"
	if p.cfg.Intercept[0] != "claude" {
		t.Fatal("the pool aliases the caller's Intercept slice")
	}
}

// The end of the chain: what a warm shell is actually started with. This is the
// assertion that would have caught the dropped field at the level a user feels
// it — the variables the init script gates on either reach the shell or they
// do not.
func TestWarmPool_ArmedShellEnvironmentCarriesBothHalves(t *testing.T) {
	p := newWarmShellPool(warmPoolShellConfig{
		Cmd:       "/bin/zsh",
		Env:       []string{"ZDOTDIR=/tmp/z"},
		Intercept: []string{"claude"},
	}, 1)
	t.Cleanup(p.Stop)

	env := strings.Join(append(append([]string(nil), p.cfg.Env...),
		shellinit.InterceptEnv(p.cfg.Intercept, "TOKEN")...), " ")
	for _, want := range []string{"ZDOTDIR=", "QUIL_INTERCEPT=claude", "QUIL_INTERCEPT_TOKEN=TOKEN"} {
		if !strings.Contains(env, want) {
			t.Errorf("a warm shell would start without %s (env=%q)", want, env)
		}
	}
}
