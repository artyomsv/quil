package codexhook

import (
	"strings"
	"testing"
)

// The probe on 2026-09-04 (codex-cli 0.146.0) ran a SessionStart hook with
// exactly this command and this hash, and refused it without the hash. Any
// change to the identity encoding must keep this value.
const probeCommand = "echo hooked > hookfired.txt"
const probeHash = "sha256:5be9d5089b64165ae3661e509b80789b0e314361111505f1b2c86a5490dbb86e"

// TestHookCommand_NamesTheExeEnvVar pins the shape that survives codex's argv
// handling AND the shell codex runs hooks in: the command carries NO path and
// NO quote, only the environment variable the shell resolves. Measured
// 2026-09-04 with codex 0.146.0 on Windows (PowerShell): a quoted path is a
// parse error (exit 1, nothing written), a bare path breaks on a space, and
// `%VAR%` is never expanded; `& $env:QUIL_HOOK_EXE codex-hook` recorded the
// session from a directory WITH a space under both PowerShell 5.1 and pwsh.
func TestHookCommand_NamesTheExeEnvVar(t *testing.T) {
	t.Parallel()
	if got := hookCommandFor("windows"); got != `& $env:QUIL_HOOK_EXE codex-hook` {
		t.Errorf("windows command = %q", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := hookCommandFor(goos); got != `"$QUIL_HOOK_EXE" codex-hook` {
			t.Errorf("%s command = %q", goos, got)
		}
	}
	if strings.ContainsRune(hookCommandFor("windows"), '"') {
		t.Error("the Windows command must carry no quote: PowerShell reads a quoted path as an expression")
	}
	if got := HookCommand(); !strings.HasSuffix(got, " codex-hook") || !strings.Contains(got, HookExeEnvVar) {
		t.Errorf("HookCommand = %q", got)
	}
}

// TestHookExeEnv pins how the path travels: bare, on every platform — the
// shell side takes the variable's contents as one token.
func TestHookExeEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, exe, want string
		wantErr         bool
	}{
		{"windows path with a space", `C:\Program Files\quil\quild.exe`, `QUIL_HOOK_EXE=C:\Program Files\quil\quild.exe`, false},
		{"windows path with metacharacters", `C:\R&D\a;b\quild.exe`, `QUIL_HOOK_EXE=C:\R&D\a;b\quild.exe`, false},
		{"unix path with a space and a dollar", "/opt/quil tools/$x/quild", "QUIL_HOOK_EXE=/opt/quil tools/$x/quild", false},
		{"empty path refused", "", "", true},
		{"newline refused", "/opt/q\nuild", "", true},
		{"NUL refused", "C:\\q\x00uild.exe", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := HookExeEnv(tt.exe)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("env = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrustHash_MatchesProbe(t *testing.T) {
	t.Parallel()
	got, err := trustHash("session_start", probeCommand, 600)
	if err != nil {
		t.Fatalf("trustHash: %v", err)
	}
	if got != probeHash {
		t.Errorf("trustHash = %s, want %s", got, probeHash)
	}
}

// Go's encoder escapes < > & as \u003c etc. by default; codex's serde_json does
// not. A quild path never carries them, but the identity must still be
// byte-identical to what codex hashes, and a `>` in the probe command is what
// pinned this.
func TestTrustHash_DoesNotEscapeHTML(t *testing.T) {
	t.Parallel()
	a, _ := trustHash("stop", "a > b", 600)
	b, _ := trustHash("stop", "a \u003e b", 600)
	if a != b {
		t.Fatal("identity must not depend on how > was spelled")
	}
	body, err := identityJSON("stop", "a > b", 600)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `\u003e`) {
		t.Errorf("identity escaped >: %s", body)
	}
}

func TestHookKey_PerPlatform(t *testing.T) {
	t.Parallel()
	if got := hookKey("windows", "session_start"); got != `C:\<session-flags>\config.toml:session_start:0:0` {
		t.Errorf("windows key = %q", got)
	}
	if got := hookKey("linux", "stop"); got != "/<session-flags>/config.toml:stop:0:0" {
		t.Errorf("linux key = %q", got)
	}
}

func TestBuildConfigOverride_RegistersEveryEventWithTrust(t *testing.T) {
	t.Parallel()
	v, err := BuildConfigOverride(probeCommand, "windows")
	if err != nil {
		t.Fatalf("BuildConfigOverride: %v", err)
	}
	if !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") {
		t.Fatalf("override is not an inline table: %s", v)
	}
	for _, ev := range registeredEvents {
		if !strings.Contains(v, ev.name+"=[{hooks=[{type=\"command\",command=\"echo hooked > hookfired.txt\",timeout=") {
			t.Errorf("override missing registration for %s: %s", ev.name, v)
		}
		key := hookKey("windows", ev.key)
		h, _ := trustHash(ev.key, probeCommand, ev.timeout)
		want := tomlQuote(key) + "={trusted_hash=" + tomlQuote(h) + "}"
		if !strings.Contains(v, want) {
			t.Errorf("override missing trust for %s: want %s in %s", ev.name, want, v)
		}
	}
	// The probe itself is pinned by TestTrustHash_MatchesProbe at the probe's
	// own timeout; the override's hashes cover the SHORT timeouts it writes,
	// so the two must differ — a state entry equal to the probe hash would
	// mean the timeout is not part of the identity, and it is.
	if strings.Contains(v, probeHash) {
		t.Errorf("override carries the 600 s probe hash, so its timeout is not in the identity: %s", v)
	}
}

// Every handler is synchronous, so its timeout is how long a wedged hook can
// hold codex's turn; the override writes a short one explicitly (never
// codex's 600 s default) and SessionEnd stays at the 3 s codex caps it to.
func TestBuildConfigOverride_ExplicitShortTimeouts(t *testing.T) {
	t.Parallel()
	v, err := BuildConfigOverride(probeCommand, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `SessionEnd=[{hooks=[{type="command",command="echo hooked > hookfired.txt",timeout=3}]}]`) {
		t.Errorf("SessionEnd must carry timeout=3: %s", v)
	}
	if !strings.Contains(v, `Stop=[{hooks=[{type="command",command="echo hooked > hookfired.txt",timeout=15}]}]`) {
		t.Errorf("Stop must carry timeout=15: %s", v)
	}
	if strings.Contains(v, "timeout=600") {
		t.Errorf("no handler may inherit codex's 600 s default: %s", v)
	}
	for _, ev := range registeredEvents {
		if ev.timeout > 30 || ev.timeout < 1 {
			t.Errorf("%s timeout %d is outside the bound a wedged synchronous hook may hold a turn", ev.name, ev.timeout)
		}
	}
}

func TestBuildConfigOverride_QuotesBackslashesAndQuotes(t *testing.T) {
	t.Parallel()
	v, err := BuildConfigOverride(hookCommandFor("windows"), "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `command="& $env:QUIL_HOOK_EXE codex-hook"`) {
		t.Errorf("windows command not carried verbatim: %s", v)
	}
	if !strings.Contains(v, `"C:\\<session-flags>\\config.toml:stop:0:0"`) {
		t.Errorf("key not TOML-quoted: %s", v)
	}
	// The Unix form's own double quotes round-trip through TOML escaping.
	v, err = BuildConfigOverride(hookCommandFor("linux"), "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `command="\"$QUIL_HOOK_EXE\" codex-hook"`) {
		t.Errorf("unix command not TOML-quoted: %s", v)
	}
	// A backslash in a command still escapes (the probe command has none;
	// this guards tomlQuote's use on the command position).
	v, err = BuildConfigOverride(`C:\x\y.exe codex-hook`, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `command="C:\\x\\y.exe codex-hook"`) {
		t.Errorf("backslashes not TOML-escaped: %s", v)
	}
}

func TestBuildConfigOverride_RejectsEmptyOrMultilineCommand(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"", "  ", "a\nb"} {
		if _, err := BuildConfigOverride(cmd, "linux"); err == nil {
			t.Errorf("BuildConfigOverride(%q) = nil error, want refusal", cmd)
		}
	}
}

func TestConfigOverrideArgs_IsOneCFlag(t *testing.T) {
	t.Parallel()
	args, err := ConfigOverrideArgs(probeCommand, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "-c" || !strings.HasPrefix(args[1], "hooks={") {
		t.Errorf("args = %q", args)
	}
}

func TestRegisteredEvents_NoDuplicatesNoPostToolUseNoInterrupt(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, ev := range registeredEvents {
		if seen[ev.name] {
			t.Errorf("duplicate event %s", ev.name)
		}
		seen[ev.name] = true
		if ev.name == "PostToolUse" || ev.name == "Interrupt" {
			t.Errorf("%s must not be registered in v1 (see the design spec)", ev.name)
		}
	}
	for _, want := range []string{"SessionStart", "SessionEnd", "UserPromptSubmit", "PermissionRequest", "PreToolUse", "Stop", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact"} {
		if !seen[want] {
			t.Errorf("missing event %s", want)
		}
	}
}

func TestIsShim(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		`C:\Users\x\AppData\Roaming\npm\codex.cmd`:                     true,
		`C:\tools\codex.CMD`:                                           true,
		`C:\tools\codex.bat`:                                           true,
		`C:\Users\x\AppData\Local\Programs\OpenAI\Codex\bin\codex.exe`: false,
		"/usr/local/bin/codex":                                         false,
		"codex":                                                        false,
	} {
		if got := IsShim(path); got != want {
			t.Errorf("IsShim(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestTomlQuote(t *testing.T) {
	t.Parallel()
	if got := tomlQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("tomlQuote = %s", got)
	}
	if got := tomlQuote("tab\there"); got != `"tab\there"` {
		t.Errorf("tomlQuote tab = %s", got)
	}
	if got := tomlQuote("bell\x07"); got != `"bell\u0007"` {
		t.Errorf("tomlQuote control = %s", got)
	}
}
