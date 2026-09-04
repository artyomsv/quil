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

func TestHookCommand_InvokesNativeSubcommand(t *testing.T) {
	t.Parallel()
	exe := `C:\Program Files\quil\quild.exe`
	cmd := HookCommand(exe)
	if !strings.HasPrefix(cmd, `"`+exe+`"`) {
		t.Errorf("HookCommand %q must double-quote the exe path", cmd)
	}
	if !strings.HasSuffix(cmd, " codex-hook") {
		t.Errorf("HookCommand %q must end with the codex-hook subcommand", cmd)
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
	if !strings.Contains(v, tomlQuote(hookKey("windows", "session_start"))+"={trusted_hash="+tomlQuote(probeHash)+"}") {
		t.Errorf("SessionStart trust does not carry the probe hash: %s", v)
	}
}

// SessionEnd is the one event codex caps at 3 s; the override says so
// explicitly so the hash never depends on codex's default for that event.
func TestBuildConfigOverride_SessionEndTimeout(t *testing.T) {
	t.Parallel()
	v, err := BuildConfigOverride(probeCommand, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `SessionEnd=[{hooks=[{type="command",command="echo hooked > hookfired.txt",timeout=3}]}]`) {
		t.Errorf("SessionEnd must carry timeout=3: %s", v)
	}
	if !strings.Contains(v, `Stop=[{hooks=[{type="command",command="echo hooked > hookfired.txt",timeout=600}]}]`) {
		t.Errorf("Stop must carry timeout=600: %s", v)
	}
}

func TestBuildConfigOverride_QuotesBackslashesAndQuotes(t *testing.T) {
	t.Parallel()
	cmd := HookCommand(`C:\quil\quild.exe`)
	v, err := BuildConfigOverride(cmd, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `command="\"C:\\quil\\quild.exe\" codex-hook"`) {
		t.Errorf("command not TOML-quoted: %s", v)
	}
	if !strings.Contains(v, `"C:\\<session-flags>\\config.toml:stop:0:0"`) {
		t.Errorf("key not TOML-quoted: %s", v)
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
