package shellinit

import (
	"strings"
	"testing"
)

func envMap(t *testing.T, cfg *ShellConfig) map[string]string {
	t.Helper()
	if cfg == nil {
		t.Fatal("Configure returned nil for a shell it supports")
	}
	m := map[string]string{}
	for _, kv := range cfg.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		m[k] = v
	}
	return m
}

// Both halves gate the scripts. A name list with no token would arm functions
// whose marker the daemon must reject, which is a second of dead air before
// every agent launch and nothing converted.
func TestConfigure_InterceptNeedsBothHalves(t *testing.T) {
	cases := []struct {
		name      string
		intercept []string
		token     string
	}{
		{"neither", nil, ""},
		{"names without a token", []string{"claude"}, ""},
		{"token without names", nil, "tok"},
		{"token with an empty name list", []string{}, "tok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, shell := range []string{"/usr/bin/bash", "/bin/zsh", "/usr/bin/pwsh"} {
				env := envMap(t, Configure(shell, t.TempDir(), tc.intercept, tc.token))
				if _, ok := env["QUIL_INTERCEPT"]; ok {
					t.Errorf("%s: armed interception with %s", shell, tc.name)
				}
				if _, ok := env["QUIL_INTERCEPT_TOKEN"]; ok {
					t.Errorf("%s: exported a token with %s", shell, tc.name)
				}
			}
		})
	}
}

// bash takes its script by --rcfile and carried no Env at all before this
// feature, so it is the shell most likely to lose the pair in a refactor.
func TestConfigure_InterceptReachesEveryInjectedShell(t *testing.T) {
	for _, shell := range []string{"/usr/bin/bash", "/bin/zsh", "/usr/bin/pwsh", "/usr/bin/powershell"} {
		env := envMap(t, Configure(shell, t.TempDir(), []string{"claude", "codex"}, "tok123"))
		if got := env["QUIL_INTERCEPT"]; got != "claude,codex" {
			t.Errorf("%s: QUIL_INTERCEPT = %q, want \"claude,codex\"", shell, got)
		}
		if got := env["QUIL_INTERCEPT_TOKEN"]; got != "tok123" {
			t.Errorf("%s: token = %q, want \"tok123\"", shell, got)
		}
	}
}

// zsh's own two variables are what make the init script load at all.
func TestConfigure_InterceptDoesNotDisplaceZshdir(t *testing.T) {
	env := envMap(t, Configure("/bin/zsh", t.TempDir(), []string{"claude"}, "tok"))
	for _, k := range []string{"ZDOTDIR", "QUIL_ORIG_ZDOTDIR", "QUIL_INTERCEPT", "QUIL_INTERCEPT_TOKEN"} {
		if _, ok := env[k]; !ok {
			t.Errorf("zsh env lost %s", k)
		}
	}
}

// A name the scripts would skip must not appear in the list the daemon
// believes is armed, or the two ends disagree about what is intercepted. A
// comma is the worst case: it would split into two bogus names.
func TestConfigure_InterceptDropsNamesTheScriptsWouldReject(t *testing.T) {
	env := envMap(t, Configure("/usr/bin/bash", t.TempDir(),
		[]string{"claude", "a,b", "rm -rf /", "we$ird", "co-dex_2.0", ""}, "tok"))
	if got := env["QUIL_INTERCEPT"]; got != "claude,co-dex_2.0" {
		t.Fatalf("QUIL_INTERCEPT = %q, want \"claude,co-dex_2.0\"", got)
	}
}

func TestConfigure_InterceptDroppedEntirelyWhenEveryNameIsRejected(t *testing.T) {
	env := envMap(t, Configure("/usr/bin/bash", t.TempDir(), []string{"a,b", "$(x)"}, "tok"))
	if _, ok := env["QUIL_INTERCEPT_TOKEN"]; ok {
		t.Fatal("exported a token for an empty intercept list")
	}
}

// Fish gets no injection at all, so it gets no interception either. This is a
// real coverage hole, documented as one; the test exists so that a future
// change adding fish OSC 133 does not quietly imply interception with it.
func TestConfigure_FishGetsNothing(t *testing.T) {
	if cfg := Configure("/usr/bin/fish", t.TempDir(), []string{"claude"}, "tok"); cfg != nil {
		t.Fatalf("fish returned a config: %+v", cfg)
	}
}

// The scripts must not arm on the variables alone: every guard below is one the
// review found the hard way, and a missing one is a live defect rather than a
// style point.
func TestScripts_InterceptGuards(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   []string
	}{
		{"scripts/bash-init.sh", []string{
			`QUIL_INTERCEPT_TOKEN`,
			`BASH_VERSINFO[0]`,   // read -N needs bash >= 4.1; macOS ships 3.2
			`-t 0 && -t 1`,       // a captured stdout must not receive the marker
			`BASH_SUBSHELL > 0`,  // `claude &` would take SIGTTIN on the read
			`-p|--print`,         // not a session; never round-tripped
			`declare -F`,         // a user's own wrapper keeps winning
			`read -r -N 8 -t 1`,  // fixed length: no terminator to get wrong
			`> /dev/tty`,
			`stty -echo`,
		}},
		{"scripts/zsh-init.sh", []string{
			`QUIL_INTERCEPT_TOKEN`,
			`-t 0 && -t 1`,
			`ZSH_SUBSHELL > 0`,
			`-p|--print`,
			`${+functions[$__qn]}`,
			`read -t 1 -k 8`,
			`> /dev/tty`,
			`stty -echo`,
		}},
	} {
		t.Run(tc.script, func(t *testing.T) {
			body, err := scripts.ReadFile(tc.script)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(body), want) {
					t.Errorf("script lost the guard %q", want)
				}
			}
		})
	}
}

// Echo is applied by the line discipline when bytes ARRIVE, not when the read
// runs, and stty is a fork+exec — so a reply written after the marker but
// before `stty -echo` paints the answer on screen.
func TestScripts_EchoOffPrecedesTheMarker(t *testing.T) {
	for _, name := range []string{"scripts/bash-init.sh", "scripts/zsh-init.sh"} {
		body, err := scripts.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		s := string(body)
		echo, marker := strings.Index(s, "stty -echo"), strings.Index(s, `printf '\e]7770;`)
		if echo < 0 || marker < 0 {
			t.Fatalf("%s: missing stty -echo (%d) or marker (%d)", name, echo, marker)
		}
		if echo > marker {
			t.Errorf("%s: marker is written before echo is disabled", name)
		}
	}
}
