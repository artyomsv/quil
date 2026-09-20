package daemon

import (
	"strings"
	"sync/atomic"
	"testing"

	apty "github.com/artyomsv/quil/internal/pty"
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

// TryClaim re-wraps the parked session, and the token minted in fill lives on
// the value being wrapped — so it has to be carried across by hand. It was not.
// Every link in the chain above was correct: the pool held the names, the shell
// started armed, the function shadowed the binary, and the marker was authentic.
// spawnPane then bound "" to the pane, detectHandStart returned on the empty
// token before parsing anything, and the shell — hearing no reply — ran the
// agent as typed. A silent, whole-feature failure with no log line anywhere.
func TestWarmShellPool_ClaimCarriesTheInterceptToken(t *testing.T) {
	cwd := t.TempDir()
	s := newScriptedWarmSession()
	s.response = []byte("echoed cd\r\n\x1b]133;A\x07" + warmOSC7(cwd) + "prompt> ")
	s.chunks <- []byte("\x1b]133;A\x07old prompt")
	var created atomic.Int64
	p := warmTestPool(t, warmPoolShellConfig{
		Cmd:       "/bin/zsh",
		Intercept: []string{"claude"},
	}, 1, func() apty.Session {
		if created.Add(1) == 1 {
			return s
		}
		return newScriptedWarmSession()
	})
	warmReady(t, p, 1)

	claimed, ok := p.TryClaim(cwd, 120, 40)
	if !ok || claimed == nil {
		t.Fatal("warm shell was not claimed")
	}
	t.Cleanup(func() { claimed.Close() })

	var want string
	for _, kv := range s.env {
		if v, found := strings.CutPrefix(kv, "QUIL_INTERCEPT_TOKEN="); found {
			want = v
		}
	}
	if want == "" {
		t.Fatal("the warm shell was started without a token; nothing to carry")
	}
	if got := interceptTokenOf(claimed); got != want {
		t.Fatalf("claimed token = %q, want %q — spawnPane binds this to the pane, "+
			"and an empty one makes every authentic marker from this shell unreadable", got, want)
	}
}
