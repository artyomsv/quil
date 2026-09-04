package codexhook

import (
	"errors"
	"strings"
	"testing"
)

// The probe on 2026-09-04 (codex-cli 0.146.0) ran a SessionStart hook with
// exactly this command and this hash, and refused it without the hash. Any
// change to the identity encoding must keep this value.
const probeCommand = "echo hooked > hookfired.txt"
const probeHash = "sha256:5be9d5089b64165ae3661e509b80789b0e314361111505f1b2c86a5490dbb86e"

// TestHookCommand_QuotingPerPlatform pins the one shape codex 0.146.0 can run
// on Windows — an UNQUOTED path — beside the ordinary quoted form for the
// Unix shells. Measured 2026-09-04: codex escapes the quotes as \" before
// cmd.exe sees them, so the quoted form exits 1 with nothing written, while
// the same path unquoted recorded the session.
func TestHookCommand_QuotingPerPlatform(t *testing.T) {
	t.Parallel()
	noShort := func(p string) (string, error) { return p, nil }
	shortOK := func(p string) (string, error) { return `C:\PROGRA~1\quil\quild.exe`, nil }
	shortFails := func(p string) (string, error) { return "", errors.New("8.3 names disabled") }

	tests := []struct {
		name     string
		goos     string
		exe      string
		short    func(string) (string, error)
		wantCmd  string
		wantNote bool
	}{
		{"linux quotes", "linux", "/usr/local/bin/quild", noShort, `"/usr/local/bin/quild" codex-hook`, false},
		{"darwin quotes even with a space", "darwin", "/Applications/Quil Tools/quild", noShort, `"/Applications/Quil Tools/quild" codex-hook`, false},
		{"windows bare path goes unquoted", "windows", `E:\Projects\quil\quild.exe`, noShort, `E:\Projects\quil\quild.exe codex-hook`, false},
		{"windows space uses the short name", "windows", `C:\Program Files\quil\quild.exe`, shortOK, `C:\PROGRA~1\quil\quild.exe codex-hook`, false},
		{"windows space without a short name falls back to quotes with a note", "windows", `C:\Program Files\quil\quild.exe`, shortFails, `"C:\Program Files\quil\quild.exe" codex-hook`, true},
		{"windows metacharacter falls back to quotes with a note", "windows", `C:\R&D\quild.exe`, noShort, `"C:\R&D\quild.exe" codex-hook`, true},
		// cmd.exe also splits a bare token at ; , and = — a path holding one
		// would go out unquoted and every hook would fail with no log line.
		{"windows delimiter takes the short name", "windows", `C:\a;b\quild.exe`, shortOK, `C:\PROGRA~1\quil\quild.exe codex-hook`, false},
		{"windows equals sign without a short name falls back with a note", "windows", `C:\k=v\quild.exe`, shortFails, `"C:\k=v\quild.exe" codex-hook`, true},
		{"unix path with a dollar is reported", "linux", "/opt/$HOME/quild", noShort, `"/opt/$HOME/quild" codex-hook`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd, note := hookCommandFor(tt.goos, tt.exe, tt.short)
			if cmd != tt.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, tt.wantCmd)
			}
			if (note != "") != tt.wantNote {
				t.Errorf("note = %q, wantNote %v", note, tt.wantNote)
			}
		})
	}
	// The production wrapper must route through the same function with the
	// real platform, never a hardcoded quoted form.
	cmd, _ := HookCommand("/opt/quild")
	if !strings.HasSuffix(cmd, " codex-hook") || !strings.Contains(cmd, "/opt/quild") {
		t.Errorf("HookCommand = %q", cmd)
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
	cmd, _ := hookCommandFor("windows", `C:\quil\quild.exe`, func(p string) (string, error) { return p, nil })
	v, err := BuildConfigOverride(cmd, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `command="C:\\quil\\quild.exe codex-hook"`) {
		t.Errorf("command not TOML-quoted: %s", v)
	}
	if !strings.Contains(v, `"C:\\<session-flags>\\config.toml:stop:0:0"`) {
		t.Errorf("key not TOML-quoted: %s", v)
	}
	// A quoted Unix form still round-trips through TOML escaping.
	unix, _ := hookCommandFor("linux", "/opt/q/quild", nil)
	v, err = BuildConfigOverride(unix, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, `command="\"/opt/q/quild\" codex-hook"`) {
		t.Errorf("unix command not TOML-quoted: %s", v)
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
