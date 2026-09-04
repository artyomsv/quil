# Codex Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `codex` AI pane plugin with hook-driven notifications, work-state indicators, model/context status, input history and per-pane session resume — parity with `claude-code` minus the setup-dialog resume picker.

**Architecture:** A new hook producer package `internal/codexhook/` (sibling of `claudehook`, no shared code) builds the `-c hooks=…` override that registers a `quild codex-hook` command under ten codex hook events together with the trust hashes codex requires, and handles each hook invocation by writing the session record or a `hookevents` spool line. The daemon injects the override at spawn, reads the record at restore (`codex resume <id>`), and the existing hookevents → daemon → TUI pipeline gains a third source, `codex`.

**Tech Stack:** Go 1.25, stdlib only (`crypto/sha256`, `encoding/json`), BurntSushi/toml for the plugin file. Build and test via `./scripts/dev.sh` (Docker; no Go on the host).

**Spec:** `docs/superpowers/specs/2026-09-04-codex-plugin-design.md`

## Global Constraints

- Never touch `~/.quil/` or the production daemon; run everything through dev mode (`.claude/rules/dev-environment.md`).
- Never read or write anything under `~/.codex/`; the hook and its trust ride the `-c` argv token only.
- `resume --last` / any "most recent session" lookup is never emitted; a pane with no recorded session starts FRESH.
- Hook events stay synchronous (`async` omitted = false): async handlers run concurrently and could deliver a Stop before its UserPromptSubmit.
- Trust hash = `sha256:` + hex(SHA-256(compact JSON, keys sorted, HTML escaping OFF)) of `{"event_name":"<snake>","hooks":[{"async":false,"command":"<cmd>","timeout":<t>,"type":"command"}]}`; `<t>` is the explicit `timeout` written into the override (600, SessionEnd 3). Key = `C:\<session-flags>\config.toml:<snake>:0:0` on Windows, `/<session-flags>/config.toml:<snake>:0:0` elsewhere.
- Probe-verified golden: command `echo hooked > hookfired.txt`, `session_start`, timeout 600 → `sha256:5be9d5089b64165ae3661e509b80789b0e314361111505f1b2c86a5490dbb86e`.
- Commit messages: imperative, ≤72 chars subject, no AI attribution trailers.
- Tests run with `./scripts/dev.sh test ./internal/<pkg>` (ONE package argument; extra args are silently dropped).

---

### Task 1: `internal/codexhook` — hook command, trust hashes, config override

**Files:**
- Create: `internal/codexhook/codexhook.go`
- Create: `internal/codexhook/codexhook_test.go`

**Interfaces:**
- Produces: `HookCommand(exePath string) string`; `BuildConfigOverride(cmd, goos string) (string, error)`; `ConfigOverrideArgs(cmd, goos string) ([]string, error)`; `IsShim(resolvedCmd string) bool`; `registeredEvents []hookEvent` (name, key, timeout).

- [ ] **Step 1: Write the failing tests**

`internal/codexhook/codexhook_test.go`:

```go
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
			t.Errorf("%s must not be registered in v1 (see spec)", ev.name)
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
		`C:\Users\x\AppData\Roaming\npm\codex.cmd`: true,
		`C:\tools\codex.CMD`:                       true,
		`C:\tools\codex.bat`:                       true,
		`C:\Users\x\AppData\Local\Programs\OpenAI\Codex\bin\codex.exe`: false,
		"/usr/local/bin/codex": false,
		"codex":                false,
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
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/codexhook`
Expected: FAIL — package does not exist / undefined symbols.

- [ ] **Step 3: Implement `codexhook.go`**

```go
// Package codexhook manages the Codex CLI hook Quil registers per pane.
//
// Codex (openai/codex) has a Claude-compatible hook system: the same event
// names, the same stdin JSON. Two things differ from Claude and shape this
// package. There is no `--settings <file>`; hooks reach codex only through
// its config layers, and the one layer Quil can write without touching
// ~/.codex is the session-flags layer — the `-c key=value` overrides. And a
// hook does not run until it is TRUSTED: codex hashes each handler's
// normalised identity and runs it only when `hooks.state."<key>".trusted_hash`
// (from the user layer or the session-flags layer) matches. So the daemon
// passes ONE argv token, `-c hooks={...,state={...}}`, carrying every event
// registration and the trust for each, computed here. Verified against
// codex-cli 0.146.0 on 2026-09-04: with the hash the hook fires, without it
// codex silently skips it, and nothing under ~/.codex is read or written.
//
// The hook command is `quild codex-hook` (runhook.go), which reads the hook
// JSON on stdin and writes $QUIL_HOME/sessions/codex-<paneID>.id or appends a
// hookevents JSONL line to $QUIL_HOME/events/<paneID>.jsonl.
package codexhook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// HookCommand returns the command codex runs for each registered event. The
// exe path is double-quoted because codex runs hook commands through a shell
// (%COMSPEC% /C on Windows, $SHELL -lc elsewhere), and the daemon's own binary
// may live under "Program Files". exePath is OS-controlled, never user input.
func HookCommand(exePath string) string {
	return fmt.Sprintf(`"%s" codex-hook`, exePath)
}

// hookEvent is one registered codex hook event: the event name codex expects
// in the config, the snake_case label codex uses in trust keys and hash
// identities, and the timeout written into the override.
//
// The timeout is EXPLICIT for every event so the trust hash depends on a value
// this package chose rather than on codex's per-event default (600 s for most
// events, 1 s for SessionEnd, which is capped at 3 s). A wrong guess would not
// fail loudly: the hook would be reported Untrusted and codex's startup review
// would prompt in every pane.
type hookEvent struct {
	name    string
	key     string
	timeout int
}

// registeredEvents lists what Quil registers. Not PostToolUse: codex has no
// AskUserQuestion / ExitPlanMode, and an answered permission prompt is cleared
// by the keystroke (tui.answerBlockedByInput). Not Interrupt: absent from
// codex 0.146.0, and the TUI already synthesises the ESC stop for every pane.
var registeredEvents = []hookEvent{
	{name: "SessionStart", key: "session_start", timeout: 600},
	{name: "SessionEnd", key: "session_end", timeout: 3},
	{name: "UserPromptSubmit", key: "user_prompt_submit", timeout: 600},
	{name: "PermissionRequest", key: "permission_request", timeout: 600},
	{name: "PreToolUse", key: "pre_tool_use", timeout: 600},
	{name: "Stop", key: "stop", timeout: 600},
	{name: "SubagentStart", key: "subagent_start", timeout: 600},
	{name: "SubagentStop", key: "subagent_stop", timeout: 600},
	{name: "PreCompact", key: "pre_compact", timeout: 600},
	{name: "PostCompact", key: "post_compact", timeout: 600},
}

// sessionFlagsPath is the synthetic source path codex assigns to the
// session-flags config layer (codex-rs/hooks/src/engine/discovery.rs,
// synthetic_layer_path): "<session-flags>/config.toml" resolved against
// `C:\` on Windows and `/` elsewhere. It is part of every trust key.
func sessionFlagsPath(goos string) string {
	if goos == "windows" {
		return `C:\<session-flags>\config.toml`
	}
	return "/<session-flags>/config.toml"
}

// hookKey is codex's hook_key(): "<source>:<event>:<group>:<handler>". Quil
// registers one matcher group with one handler per event, so both indexes are 0.
func hookKey(goos, eventKey string) string {
	return sessionFlagsPath(goos) + ":" + eventKey + ":0:0"
}

// identityJSON is the canonical form codex hashes: its NormalizedHookIdentity
// serialised to TOML, converted to JSON, keys sorted, compact. Go's encoder
// sorts map keys, which gives the same bytes as codex's canonical_json; HTML
// escaping is turned off because serde_json leaves < > & alone.
func identityJSON(eventKey, cmd string, timeout int) ([]byte, error) {
	identity := map[string]any{
		"event_name": eventKey,
		"hooks": []map[string]any{{
			"async":   false,
			"command": cmd,
			"timeout": timeout,
			"type":    "command",
		}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// trustHash computes codex's trusted_hash for one handler.
func trustHash(eventKey, cmd string, timeout int) (string, error) {
	body, err := identityJSON(eventKey, cmd, timeout)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// tomlQuote renders s as a TOML basic string.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// BuildConfigOverride returns the TOML inline table for `-c hooks=<value>`:
// one matcher-less group with one command handler per registered event, plus
// a `state` table holding the trust hash for each under its session-flags key.
//
// goos selects the key's synthetic path and must be the DAEMON's GOOS — the
// key describes the machine codex runs on.
func BuildConfigOverride(cmd, goos string) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		return "", errors.New("codexhook: empty hook command")
	}
	if strings.ContainsAny(cmd, "\r\n\x00") {
		return "", errors.New("codexhook: hook command contains a control character")
	}
	var events, state strings.Builder
	for i, ev := range registeredEvents {
		h, err := trustHash(ev.key, cmd, ev.timeout)
		if err != nil {
			return "", fmt.Errorf("codexhook: hash %s: %w", ev.name, err)
		}
		if i > 0 {
			events.WriteByte(',')
			state.WriteByte(',')
		}
		fmt.Fprintf(&events, `%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`,
			ev.name, tomlQuote(cmd), ev.timeout)
		fmt.Fprintf(&state, `%s={trusted_hash=%s}`, tomlQuote(hookKey(goos, ev.key)), tomlQuote(h))
	}
	return "{" + events.String() + ",state={" + state.String() + "}}", nil
}

// ConfigOverrideArgs returns the argv prefix the daemon prepends to a codex
// spawn. `-c` is a global codex flag, so it is valid before the `resume`
// subcommand as well as before a fresh start.
func ConfigOverrideArgs(cmd, goos string) ([]string, error) {
	v, err := BuildConfigOverride(cmd, goos)
	if err != nil {
		return nil, err
	}
	return []string{"-c", "hooks=" + v}, nil
}

// IsShim reports whether the resolved codex command is a cmd.exe batch shim —
// what an npm install puts on PATH on Windows. cmd.exe re-parses the command
// line with its own quoting rules, and the override value carries quotes, so
// an inline `-c` through a shim is the same bug class as the inline Claude
// `--settings` JSON was. There is no file form to fall back to here, so the
// caller skips the hook and logs.
func IsShim(resolvedCmd string) bool {
	switch strings.ToLower(filepath.Ext(resolvedCmd)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/codexhook`
Expected: PASS (all tests in this file).

---

### Task 2: `internal/codexhook` — session record + rollout usage reader

**Files:**
- Create: `internal/codexhook/session.go`
- Create: `internal/codexhook/o_nofollow_unix.go`, `internal/codexhook/o_nofollow_windows.go`
- Create: `internal/codexhook/rollout.go`
- Create: `internal/codexhook/session_test.go`, `internal/codexhook/rollout_test.go`

**Interfaces:**
- Produces: `type SessionRecord struct{ ID, TranscriptPath string; ModTime time.Time }`; `ReadPersistedSession(quilDir, paneID string) (SessionRecord, error)`; `ReadPersistedSessionID(quilDir, paneID string) (string, time.Time, error)`; `IsValidSessionID(id string) bool`; `writeSessionFile(env HookEnv, id, transcript string) error` (used by Task 3); `readRolloutUsage(path string) (int64, bool)`.

- [ ] **Step 1: Write the failing tests**

`internal/codexhook/session_test.go`:

```go
package codexhook

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSessionRecord_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	if err := writeSessionFile(env, "01a05db1-9f44-73b2-b426-8aad5f5232f4", `C:\Users\x\.codex\sessions\2026\09\01\rollout-2026-09-01T17-58-36-01a05db1-9f44-73b2-b426-8aad5f5232f4.jsonl`); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "01a05db1-9f44-73b2-b426-8aad5f5232f4" {
		t.Errorf("ID = %q", rec.ID)
	}
	if filepath.Base(rec.TranscriptPath) != "rollout-2026-09-01T17-58-36-01a05db1-9f44-73b2-b426-8aad5f5232f4.jsonl" {
		t.Errorf("TranscriptPath = %q", rec.TranscriptPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "codex-pane-abc.id")); err != nil {
		t.Errorf("record must live at sessions/codex-<paneID>.id: %v", err)
	}
	id, _, err := ReadPersistedSessionID(dir, "pane-abc")
	if err != nil || id != rec.ID {
		t.Errorf("ReadPersistedSessionID = %q, %v", id, err)
	}
}

func TestSessionRecord_MissingIsNotExist(t *testing.T) {
	t.Parallel()
	_, err := ReadPersistedSession(t.TempDir(), "pane-none")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

func TestWriteSessionFile_RejectsNonUUIDAndNewlinePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	if err := writeSessionFile(env, "not-a-uuid", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "codex-pane-abc.id")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a non-uuid id must not be recorded")
	}
	if err := writeSessionFile(env, "01a05db1-9f44-73b2-b426-8aad5f5232f4", "evil\nline"); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.TranscriptPath != "" {
		t.Errorf("a path with a newline must be dropped, got %q", rec.TranscriptPath)
	}
}

func TestReadPersistedSession_RejectsBadPaneID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "../x", `a\b`, "a/b", "bad\nid"} {
		if _, err := ReadPersistedSession(t.TempDir(), id); err == nil {
			t.Errorf("paneID %q accepted", id)
		}
	}
}

func TestReadPersistedSession_RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.id")
	if err := os.WriteFile(target, []byte("01a05db1-9f44-73b2-b426-8aad5f5232f4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "sessions", "codex-pane-abc.id")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPersistedSession(dir, "pane-abc"); err == nil {
		t.Error("symlinked record must be refused")
	}
}

func TestIsValidSessionID(t *testing.T) {
	t.Parallel()
	if !IsValidSessionID("01a05db1-9f44-73b2-b426-8aad5f5232f4") {
		t.Error("canonical uuid rejected")
	}
	for _, bad := range []string{"", "--last", "01a05db19f4473b2b4268aad5f5232f4", "x\n", "01a05db1-9f44-73b2-b426-8aad5f5232f4 "} {
		if IsValidSessionID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
```

`internal/codexhook/rollout_test.go`:

```go
package codexhook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tokenCountLine = `{"timestamp":"2026-09-01T15:58:43.196Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":14064,"cached_input_tokens":9984,"cache_write_input_tokens":0,"output_tokens":8,"reasoning_output_tokens":0,"total_tokens":14072},"last_token_usage":{"input_tokens":14064,"cached_input_tokens":9984,"cache_write_input_tokens":0,"output_tokens":8,"reasoning_output_tokens":0,"total_tokens":14072},"model_context_window":258400},"rate_limits":{"limit_id":"codex"}}}`

func writeRollout(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-09-01T17-58-36-01a05db1-9f44-73b2-b426-8aad5f5232f4.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadRolloutUsage_LastTokenCountWins(t *testing.T) {
	t.Parallel()
	older := strings.Replace(tokenCountLine, `"total_tokens":14072},"model_context_window"`, `"total_tokens":999},"model_context_window"`, 1)
	p := writeRollout(t,
		`{"type":"session_meta","payload":{"id":"x"}}`,
		older,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`,
		tokenCountLine,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	)
	tokens, ok := readRolloutUsage(p)
	if !ok || tokens != 14072 {
		t.Errorf("readRolloutUsage = %d, %v; want 14072, true", tokens, ok)
	}
}

// A token_count whose info is null (rate-limit-only update) must not shadow
// the last real usage line.
func TestReadRolloutUsage_SkipsNullInfo(t *testing.T) {
	t.Parallel()
	p := writeRollout(t, tokenCountLine,
		`{"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{}}}`)
	tokens, ok := readRolloutUsage(p)
	if !ok || tokens != 14072 {
		t.Errorf("readRolloutUsage = %d, %v; want 14072, true", tokens, ok)
	}
}

func TestReadRolloutUsage_Refusals(t *testing.T) {
	t.Parallel()
	if _, ok := readRolloutUsage("relative/rollout.jsonl"); ok {
		t.Error("relative path accepted")
	}
	if _, ok := readRolloutUsage(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Error("missing file reported ok")
	}
	p := writeRollout(t, `{"type":"session_meta"}`)
	if _, ok := readRolloutUsage(p); ok {
		t.Error("rollout without token_count reported ok")
	}
	notJSONL := filepath.Join(t.TempDir(), "rollout.txt")
	os.WriteFile(notJSONL, []byte(tokenCountLine+"\n"), 0o600)
	if _, ok := readRolloutUsage(notJSONL); ok {
		t.Error("non-.jsonl path accepted")
	}
}

func TestReadRolloutUsage_TailOnly(t *testing.T) {
	t.Parallel()
	pad := strings.Repeat(`{"type":"event_msg","payload":{"type":"agent_message","message":"`+strings.Repeat("x", 1000)+`"}}`+"\n", 400)
	p := filepath.Join(t.TempDir(), "big.jsonl")
	if err := os.WriteFile(p, []byte(pad+tokenCountLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, ok := readRolloutUsage(p)
	if !ok || tokens != 14072 {
		t.Errorf("tail read failed: %d %v", tokens, ok)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/codexhook`
Expected: FAIL — `HookEnv`, `writeSessionFile`, `ReadPersistedSession`, `readRolloutUsage` undefined.

- [ ] **Step 3: Implement**

`internal/codexhook/o_nofollow_unix.go`:

```go
//go:build !windows

package codexhook

import "syscall"

// oNoFollow makes os.OpenFile refuse a symlink atomically (no Lstat+Open
// TOCTOU window). Mirrors internal/opencodehook.
const oNoFollow = syscall.O_NOFOLLOW
```

`internal/codexhook/o_nofollow_windows.go`:

```go
//go:build windows

package codexhook

// oNoFollow is unavailable on Windows; symlink creation there requires
// elevated privilege, so the practical surface is narrower. Mirrors
// internal/opencodehook.
const oNoFollow = 0
```

`internal/codexhook/session.go`:

```go
package codexhook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HookEnv carries the per-invocation context the hook needs, sourced from the
// QUIL_* environment the daemon sets on a codex pane at spawn. Codex hands its
// own process environment to hook commands (shell_environment_policy defaults
// to inherit = all), so these arrive exactly as they do for Claude.
type HookEnv struct {
	PaneID        string // QUIL_PANE_ID — empty means "invoked outside Quil" (no-op)
	QuilDir       string // QUIL_HOOK_HOME (QUIL_HOME fallback) — root for sessions/ and events/
	Mode          string // QUIL_HOOK_MODE: "default" | "verbose" | "off"
	RecordHistory bool   // QUIL_RECORD_HISTORY=1 — append full prompts to the history store
}

// validatePaneID rejects pane ids that could escape the sessions directory or
// forge a log line. Same invariant as claudehook's.
func validatePaneID(paneID string) error {
	if paneID == "" {
		return errors.New("codexhook: empty paneID")
	}
	if strings.ContainsAny(paneID, `/\`) || strings.Contains(paneID, "..") {
		return fmt.Errorf("codexhook: paneID %q contains path separators or parent traversal", paneID)
	}
	for _, r := range paneID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("codexhook: paneID %q contains a control character", paneID)
		}
	}
	return nil
}

// sessionIDRe is the canonical UUID shape codex mints (UUIDv7). The value ends
// up as the operand of `codex resume`, so it is validated rather than trusted;
// a flag-shaped or partial token is refused outright.
var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidSessionID reports whether id has the shape codex mints.
func IsValidSessionID(id string) bool {
	return sessionIDRe.MatchString(id)
}

// sessionIDFile returns the record path. The "codex-" prefix keeps it disjoint
// from claudehook's <paneID>.id by construction, the way opencodehook's is.
func sessionIDFile(quilDir, paneID string) string {
	return filepath.Join(quilDir, "sessions", "codex-"+paneID+".id")
}

// SessionRecord is what the hook persists for a pane: the live codex session
// id and the absolute path of its rollout file (empty when SessionStart carried
// none — "unknown", never "missing").
type SessionRecord struct {
	ID             string
	TranscriptPath string
	ModTime        time.Time
}

const (
	maxIDBytes     = 256
	maxRecordBytes = 8 << 10
)

// parseSessionRecord splits the two-line record, trimming PER LINE.
func parseSessionRecord(body string) SessionRecord {
	var rec SessionRecord
	lines := strings.SplitN(body, "\n", 3)
	if id := strings.TrimSpace(lines[0]); len(id) <= maxIDBytes {
		rec.ID = id
	}
	if len(lines) > 1 {
		rec.TranscriptPath = strings.TrimSpace(lines[1])
	}
	return rec
}

// ReadPersistedSession returns the record the hook last wrote for paneID. A
// missing file satisfies errors.Is(err, os.ErrNotExist).
func ReadPersistedSession(quilDir, paneID string) (SessionRecord, error) {
	if quilDir == "" {
		return SessionRecord{}, errors.New("codexhook: empty quilDir")
	}
	if err := validatePaneID(paneID); err != nil {
		return SessionRecord{}, err
	}
	f, err := os.OpenFile(sessionIDFile(quilDir, paneID), os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return SessionRecord{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return SessionRecord{}, err
	}
	// Belt and braces where O_NOFOLLOW is 0: a record must be a regular file.
	if !info.Mode().IsRegular() {
		return SessionRecord{}, fmt.Errorf("codexhook: %s is not a regular file", sessionIDFile(quilDir, paneID))
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxRecordBytes))
	if err != nil {
		return SessionRecord{ModTime: info.ModTime()}, err
	}
	rec := parseSessionRecord(string(buf))
	rec.ModTime = info.ModTime()
	return rec, nil
}

// ReadPersistedSessionID is the id-only accessor, mirroring the other hook
// packages.
func ReadPersistedSessionID(quilDir, paneID string) (string, time.Time, error) {
	rec, err := ReadPersistedSession(quilDir, paneID)
	return rec.ID, rec.ModTime, err
}

// writeSessionFile validates and atomically writes the record. A non-uuid id
// is logged and NOT written; a transcript path with a newline is dropped (it
// would forge a second record line) while the id is still recorded.
func writeSessionFile(env HookEnv, sessionID, transcriptPath string) error {
	if sessionID == "" {
		hookLog(env.QuilDir, env.PaneID, "no session_id extracted from stdin")
		return nil
	}
	if !IsValidSessionID(sessionID) {
		hookLog(env.QuilDir, env.PaneID, fmt.Sprintf("session_id rejected as non-uuid (len=%d)", len(sessionID)))
		return nil
	}
	sessionsDir := filepath.Join(env.QuilDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		hookLog(env.QuilDir, env.PaneID, "mkdir sessions dir failed: "+err.Error())
		return err
	}
	body := sessionID + "\n"
	if transcriptPath != "" && !strings.ContainsAny(transcriptPath, "\r\n") {
		body += transcriptPath + "\n"
	}
	if err := atomicWrite(sessionIDFile(env.QuilDir, env.PaneID), []byte(body), 0o600); err != nil {
		hookLog(env.QuilDir, env.PaneID, "write session file failed: "+err.Error())
		return err
	}
	return nil
}

// atomicWrite writes via a temp file in the same directory and a rename.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// hookLog appends a best-effort breadcrumb to $QuilDir/codexhook/hook.log.
func hookLog(quilDir, paneID, msg string) {
	logDir := filepath.Join(quilDir, "codexhook")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "hook.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pane=%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), paneID, msg)
}
```

`internal/codexhook/rollout.go`:

```go
package codexhook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// rolloutTailBytes caps how much of the rollout the hook reads. The last
// token_count line is always within the final few KB.
const rolloutTailBytes = 256 << 10

// rolloutLine mirrors the subset of a codex rollout JSONL entry needed to read
// context usage: {"type":"event_msg","payload":{"type":"token_count","info":
// {"last_token_usage":{"total_tokens":N},"model_context_window":W}}}. info is
// nullable — a rate-limit-only update carries none.
type rolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Info *struct {
			LastTokenUsage struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// readRolloutUsage tail-reads a codex rollout and returns the context-token
// count of the most recent token_count line: codex's own
// tokens_in_context_window() is last_token_usage.total_tokens. Best-effort by
// contract — any failure returns ok=false and the Stop event goes out without
// usage, exactly as before the feature.
//
// The path arrives via the hook stdin; only absolute .jsonl paths are eligible.
func readRolloutUsage(path string) (contextTokens int64, ok bool) {
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".jsonl") {
		return 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return 0, false
	}
	offset := st.Size() - rolloutTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, false
	}
	buf, err := io.ReadAll(io.LimitReader(f, rolloutTailBytes))
	if err != nil {
		return 0, false
	}
	lines := bytes.Split(buf, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		ln := bytes.TrimSpace(lines[i])
		if len(ln) == 0 || !bytes.Contains(ln, []byte(`"token_count"`)) {
			continue
		}
		var rl rolloutLine
		if err := json.Unmarshal(ln, &rl); err != nil {
			continue // a truncated first line of the tail window, or noise
		}
		if rl.Type != "event_msg" || rl.Payload.Type != "token_count" || rl.Payload.Info == nil {
			continue
		}
		return rl.Payload.Info.LastTokenUsage.TotalTokens, true
	}
	return 0, false
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/codexhook`
Expected: PASS.

---

### Task 3: `internal/codexhook` — `RunHook` dispatch

**Files:**
- Create: `internal/codexhook/runhook.go`
- Create: `internal/codexhook/runhook_test.go`

**Interfaces:**
- Consumes: `HookEnv`, `writeSessionFile`, `readRolloutUsage`, `hookLog` (Task 2); `hookevents.Payload`, `hookevents.SourceCodex` (Task 4 adds the constant — implement Task 4 Step 3 first if compiling fails); `panehistory.Append`.
- Produces: `RunHook(r io.Reader, env HookEnv, nowMs int64) error`.

- [ ] **Step 1: Write the failing tests**

`internal/codexhook/runhook_test.go`:

```go
package codexhook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/panehistory"
)

const sid = "01a05db1-9f44-73b2-b426-8aad5f5232f4"

func readSpool(t *testing.T, quilDir, paneID string) []hookevents.Payload {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(quilDir, "events", paneID+".jsonl"))
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	var out []hookevents.Payload
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if ln == "" {
			continue
		}
		var p hookevents.Payload
		if err := json.Unmarshal([]byte(ln), &p); err != nil {
			t.Fatalf("decode %q: %v", ln, err)
		}
		out = append(out, p)
	}
	return out
}

func spoolMissing(t *testing.T, quilDir, paneID string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(quilDir, "events", paneID+".jsonl")); err == nil {
		t.Fatalf("spool exists; want nothing written")
	}
}

func run(t *testing.T, env HookEnv, stdin string, nowMs int64) {
	t.Helper()
	if err := RunHook(strings.NewReader(stdin), env, nowMs); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
}

func TestRunHook_OutsideQuilIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run(t, HookEnv{QuilDir: dir}, `{"hook_event_name":"Stop","session_id":"`+sid+`"}`, 1)
	spoolMissing(t, dir, "")
}

func TestRunHook_SessionStart_WritesRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":"/home/x/.codex/sessions/2026/09/01/rollout-x-`+sid+`.jsonl","source":"startup","cwd":"/w","model":"gpt-5","permission_mode":"default"}`, 1)
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != sid || !strings.HasSuffix(rec.TranscriptPath, sid+".jsonl") {
		t.Errorf("record = %+v", rec)
	}
	spoolMissing(t, dir, "pane-abc")
}

func TestRunHook_SessionStart_NullTranscriptPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":null}`, 1)
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil || rec.ID != sid || rec.TranscriptPath != "" {
		t.Errorf("record = %+v, %v", rec, err)
	}
}

func TestRunHook_SpoolMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		stdin     string
		wantEvent string
		wantTitle string
		wantSev   string
		wantData  map[string]string
	}{
		{"prompt", `{"hook_event_name":"UserPromptSubmit","session_id":"` + sid + `","prompt":"tell me a joke"}`,
			"UserPromptSubmit", "Working on: tell me a joke", hookevents.SeverityInfo, map[string]string{"prompt_preview": "tell me a joke"}},
		{"permission", `{"hook_event_name":"PermissionRequest","session_id":"` + sid + `","tool_name":"shell","tool_input":{"command":["ls"]}}`,
			"PermissionRequest", "Needs approval: shell", hookevents.SeverityWarning, map[string]string{"tool": "shell"}},
		{"session end", `{"hook_event_name":"SessionEnd","session_id":"` + sid + `","reason":"other"}`,
			"SessionEnd", "Session ended", hookevents.SeverityInfo, nil},
		{"subagent start", `{"hook_event_name":"SubagentStart","session_id":"` + sid + `","agent_id":"t1","agent_type":"explorer"}`,
			"SubagentStart", "Spawned: explorer", hookevents.SeverityInfo, map[string]string{"agent_type": "explorer"}},
		{"subagent stop", `{"hook_event_name":"SubagentStop","session_id":"` + sid + `","agent_id":"t1","agent_type":"explorer"}`,
			"SubagentStop", "explorer done", hookevents.SeverityInfo, map[string]string{"agent_type": "explorer"}},
		{"pre compact", `{"hook_event_name":"PreCompact","session_id":"` + sid + `","trigger":"auto"}`,
			"PreCompact", "Compacting context (auto)", hookevents.SeverityInfo, map[string]string{"trigger": "auto"}},
		{"post compact", `{"hook_event_name":"PostCompact","session_id":"` + sid + `","trigger":"manual"}`,
			"PostCompact", "Compaction complete", hookevents.SeverityInfo, map[string]string{"compacting": "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
			run(t, env, tt.stdin, 1700000000000)
			got := readSpool(t, dir, "pane-abc")
			if len(got) != 1 {
				t.Fatalf("spool lines = %d, want 1", len(got))
			}
			p := got[0]
			if p.V != hookevents.SchemaVersion || p.Source != hookevents.SourceCodex || p.PaneID != "pane-abc" || p.TsMs != 1700000000000 || p.SessionID != sid {
				t.Errorf("header = %+v", p)
			}
			if p.HookEvent != tt.wantEvent || p.Title != tt.wantTitle || p.Severity != tt.wantSev {
				t.Errorf("got %q/%q/%q, want %q/%q/%q", p.HookEvent, p.Title, p.Severity, tt.wantEvent, tt.wantTitle, tt.wantSev)
			}
			for k, v := range tt.wantData {
				if p.Data[k] != v {
					t.Errorf("data[%s] = %q, want %q", k, p.Data[k], v)
				}
			}
			if err := p.Validate(); err != nil {
				t.Errorf("payload does not validate: %v", err)
			}
		})
	}
}

func TestRunHook_Stop_CarriesModelAndContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rollout := writeRollout(t, tokenCountLine)
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	stdin, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop", "session_id": sid, "model": "gpt-5.6-terra",
		"transcript_path": rollout, "last_assistant_message": "done", "stop_hook_active": false,
	})
	run(t, env, string(stdin), 5)
	got := readSpool(t, dir, "pane-abc")
	if len(got) != 1 || got[0].HookEvent != "Stop" || got[0].Title != "Reply ready" || got[0].Severity != hookevents.SeverityWarning {
		t.Fatalf("spool = %+v", got)
	}
	if got[0].Data["model"] != "gpt-5.6-terra" || got[0].Data["context_tokens"] != "14072" {
		t.Errorf("data = %v", got[0].Data)
	}
}

func TestRunHook_Stop_NoUsageWhenRolloutUnreadable(t *testing.T) {
	t.Parallel()
	orig := rolloutRetryDelays
	rolloutRetryDelays = []time.Duration{0}
	t.Cleanup(func() { rolloutRetryDelays = orig })
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"Stop","session_id":"`+sid+`","model":"gpt-5","transcript_path":null}`, 5)
	got := readSpool(t, dir, "pane-abc")
	if len(got) != 1 || got[0].Data != nil {
		t.Errorf("want a bare Stop with no data, got %+v", got)
	}
}

func TestRunHook_UserPromptSubmit_History(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	on := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default", RecordHistory: true}
	run(t, on, `{"hook_event_name":"UserPromptSubmit","session_id":"`+sid+`","prompt":"fix the parser"}`, 12345)
	got, err := panehistory.Read(dir, "pane-abc")
	if err != nil || len(got) != 1 || got[0].Text != "fix the parser" || got[0].TsMs != 12345 || got[0].SessionID != sid {
		t.Fatalf("history = %+v, %v", got, err)
	}
	off := HookEnv{PaneID: "pane-off", QuilDir: dir, Mode: "default"}
	run(t, off, `{"hook_event_name":"UserPromptSubmit","session_id":"`+sid+`","prompt":"hello"}`, 1)
	if h, _ := panehistory.Read(dir, "pane-off"); len(h) != 0 {
		t.Errorf("history recorded without the opt-in: %+v", h)
	}
}

func TestRunHook_PreToolUse_HeartbeatRules(t *testing.T) {
	t.Parallel()
	const call = `{"hook_event_name":"PreToolUse","session_id":"` + sid + `","tool_name":"shell","tool_input":{},"tool_use_id":"c1"}`
	t.Run("first call on a quiet pane spools Working", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		run(t, env, call, time.Now().UnixMilli())
		got := readSpool(t, dir, "pane-abc")
		if len(got) != 1 || got[0].HookEvent != "PreToolUse" || got[0].Title != "Working" || got[0].Data["tool"] != "shell" {
			t.Errorf("spool = %+v", got)
		}
	})
	t.Run("second call within the interval is dropped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		now := time.Now().UnixMilli()
		run(t, env, call, now)
		run(t, env, call, now+1000)
		if got := readSpool(t, dir, "pane-abc"); len(got) != 1 {
			t.Errorf("spool lines = %d, want 1 (throttled)", len(got))
		}
	})
	t.Run("subagent tool calls are dropped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		run(t, env, `{"hook_event_name":"PreToolUse","session_id":"`+sid+`","tool_name":"shell","agent_id":"t1","agent_type":"explorer"}`, time.Now().UnixMilli())
		spoolMissing(t, dir, "pane-abc")
	})
	t.Run("off mode never creates the spool", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "off"}
		run(t, env, call, time.Now().UnixMilli())
		spoolMissing(t, dir, "pane-abc")
	})
}

func TestRunHook_SubagentStop_UnnamedIsDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"SubagentStop","session_id":"`+sid+`","agent_id":"t1","agent_type":""}`, 1)
	spoolMissing(t, dir, "pane-abc")
}

func TestRunHook_OffMode_StillRecordsSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "off"}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":null}`, 1)
	if _, err := ReadPersistedSession(dir, "pane-abc"); err != nil {
		t.Errorf("session tracking must survive off mode: %v", err)
	}
	run(t, env, `{"hook_event_name":"Stop","session_id":"`+sid+`","model":"m","transcript_path":null}`, 2)
	spoolMissing(t, dir, "pane-abc")
}

func TestRunHook_UnknownEventAndBadInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"Interrupt","session_id":"`+sid+`"}`, 1)
	spoolMissing(t, dir, "pane-abc")
	if err := RunHook(strings.NewReader("not json"), env, 1); err == nil {
		t.Error("malformed stdin must be reported to the caller (the subcommand still exits 0)")
	}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"Stop"}`), HookEnv{PaneID: "../x", QuilDir: dir}, 1); err == nil {
		t.Error("hostile pane id accepted")
	}
}

func TestRunHook_BOMTolerated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, "\xEF\xBB\xBF"+`{"hook_event_name":"SessionEnd","session_id":"`+sid+`"}`, 1)
	if got := readSpool(t, dir, "pane-abc"); len(got) != 1 {
		t.Errorf("spool = %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/codexhook`
Expected: FAIL — `RunHook`, `rolloutRetryDelays` undefined (and `hookevents.SourceCodex` until Task 4 Step 3 lands; do that step now if so).

- [ ] **Step 3: Implement `runhook.go`**

```go
package codexhook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/panehistory"
)

// maxStdinBytes caps how much of codex's hook stdin is read; a payload can
// carry a full prompt, and 1 MiB is far above any realistic hook JSON.
const maxStdinBytes = 1 << 20

// codexStdin mirrors the subset of codex's hook JSON Quil reads. The field
// names are Claude's (codex reuses them); transcript_path is nullable upstream
// and decodes to "" here. agent_id is present only inside a subagent, which
// makes its absence the test for "this came from the main turn".
type codexStdin struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Prompt         string `json:"prompt"`
	ToolName       string `json:"tool_name"`
	AgentType      string `json:"agent_type"`
	AgentID        string `json:"agent_id"`
	Model          string `json:"model"`
	Trigger        string `json:"trigger"`
}

// RunHook processes one codex hook invocation: reads the JSON from r, routes
// by hook_event_name, and either writes the session record (SessionStart) or
// appends one hookevents.Payload line to the pane's spool. Best-effort by
// contract: an empty pane id is a no-op, failures are breadcrumbed to
// $QuilDir/codexhook/hook.log, and the subcommand always exits 0 so codex is
// never blocked. nowMs is injected for deterministic tests.
func RunHook(r io.Reader, env HookEnv, nowMs int64) error {
	if env.PaneID == "" {
		return nil // invoked outside Quil
	}
	if err := validatePaneID(env.PaneID); err != nil {
		hookLog(env.QuilDir, "invalid", "rejected pane id")
		return err
	}
	if env.Mode == "" {
		env.Mode = "default"
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxStdinBytes))
	if err != nil {
		hookLog(env.QuilDir, env.PaneID, "read stdin failed: "+err.Error())
		return err
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var in codexStdin
	if err := json.Unmarshal(raw, &in); err != nil {
		hookLog(env.QuilDir, env.PaneID, "parse stdin failed")
		return err
	}
	return dispatchHookEvent(env, in, nowMs)
}

func dispatchHookEvent(env HookEnv, in codexStdin, nowMs int64) error {
	switch in.HookEventName {
	case "SessionStart":
		return writeSessionFile(env, in.SessionID, in.TranscriptPath)
	case "SessionEnd":
		return spoolEvent(env, nowMs, "SessionEnd", in.SessionID, "Session ended", hookevents.SeverityInfo, nil)
	case "UserPromptSubmit":
		if env.RecordHistory {
			if err := panehistory.Append(env.QuilDir, env.PaneID, panehistory.Entry{
				TsMs:      nowMs,
				SessionID: in.SessionID,
				Text:      in.Prompt,
			}); err != nil {
				hookLog(env.QuilDir, env.PaneID, "append history failed: "+err.Error())
			}
		}
		preview := truncate(in.Prompt, 60)
		return spoolEvent(env, nowMs, "UserPromptSubmit", in.SessionID,
			truncate("Working on: "+preview, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"prompt_preview": preview})
	case "PermissionRequest":
		return spoolEvent(env, nowMs, "PermissionRequest", in.SessionID,
			truncate("Needs approval: "+in.ToolName, hookevents.MaxTitleBytes), hookevents.SeverityWarning,
			map[string]string{"tool": truncate(in.ToolName, hookevents.MaxDataValueBytes)})
	case "Stop":
		return spoolEvent(env, nowMs, "Stop", in.SessionID, "Reply ready", hookevents.SeverityWarning,
			modelUsageData(env, in.Model, in.TranscriptPath))
	case "PreToolUse":
		// Work-spinner START edge for a turn no user prompt began; a
		// heartbeat, not a per-call stream (see claudehook for the trace).
		// Subagent calls are dropped: turnActive is about the MAIN turn, and
		// the subagent ledger already keeps such a pane working.
		if in.AgentID != "" {
			return nil
		}
		if env.Mode == "off" {
			return nil
		}
		if spoolIsFresh(env, nowMs) {
			return nil
		}
		return spoolEvent(env, nowMs, "PreToolUse", in.SessionID, "Working", hookevents.SeverityInfo,
			map[string]string{"tool": truncate(in.ToolName, hookevents.MaxDataValueBytes)})
	case "SubagentStart":
		return spoolEvent(env, nowMs, "SubagentStart", in.SessionID,
			truncate("Spawned: "+in.AgentType, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"agent_type": truncate(in.AgentType, hookevents.MaxDataValueBytes)})
	case "SubagentStop":
		if in.AgentType == "" {
			// Names no agent: the TUI ledger could match it to nothing and it
			// would only become a " done" card. Same refusal as claudehook's.
			return nil
		}
		return spoolEvent(env, nowMs, "SubagentStop", in.SessionID,
			truncate(in.AgentType+" done", hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"agent_type": truncate(in.AgentType, hookevents.MaxDataValueBytes)})
	case "PreCompact":
		title := "Compacting context"
		if in.Trigger != "" {
			title = truncate("Compacting context ("+in.Trigger+")", hookevents.MaxTitleBytes)
		}
		return spoolEvent(env, nowMs, "PreCompact", in.SessionID, title, hookevents.SeverityInfo,
			map[string]string{"trigger": truncate(in.Trigger, hookevents.MaxDataValueBytes)})
	case "PostCompact":
		// Never read usage here: the reduced size is not in the rollout yet.
		// The compacting sentinel resets the status segment until the next
		// Stop reports the true size — same reasoning as claudehook.
		return spoolEvent(env, nowMs, "PostCompact", in.SessionID, "Compaction complete", hookevents.SeverityInfo,
			map[string]string{"compacting": "1"})
	default:
		hookLog(env.QuilDir, env.PaneID, "unhandled hook_event: "+in.HookEventName)
		return nil
	}
}

// rolloutRetryDelays paces the re-reads in modelUsageData: codex appends the
// final token_count line around the moment Stop hooks fire. Package var so
// tests can shrink the waits.
var rolloutRetryDelays = []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond}

// modelUsageData returns the model + context-token Data keys for a Stop, or
// nil when either half is unavailable (the daemon sets the status segment only
// when both are present). The model comes from the payload; the token count
// from the rollout tail.
func modelUsageData(env HookEnv, model, transcriptPath string) map[string]string {
	if model == "" || transcriptPath == "" {
		return nil
	}
	var (
		tokens int64
		ok     bool
	)
	for _, delay := range rolloutRetryDelays {
		time.Sleep(delay)
		if tokens, ok = readRolloutUsage(transcriptPath); ok {
			break
		}
	}
	if !ok {
		hookLog(env.QuilDir, env.PaneID, "rollout usage read failed after retries: "+truncate(transcriptPath, 200))
		return nil
	}
	return map[string]string{
		"model":          truncate(model, hookevents.MaxDataValueBytes),
		"context_tokens": strconv.FormatInt(tokens, 10),
	}
}

func spoolDir(env HookEnv) string  { return filepath.Join(env.QuilDir, "events") }
func spoolPath(env HookEnv) string { return filepath.Join(spoolDir(env), env.PaneID+".jsonl") }

// workHeartbeatInterval is how long a pane may stay silent before a tool call
// is worth spooling as proof of work. Same value as claudehook's.
const workHeartbeatInterval = 15 * time.Second

// spoolIsFresh reports whether Quil has heard from this pane within
// workHeartbeatInterval, judged by the spool's mtime. A missing spool or a
// future mtime both resolve toward speaking.
func spoolIsFresh(env HookEnv, nowMs int64) bool {
	fi, err := os.Stat(spoolPath(env))
	if err != nil {
		return false
	}
	age := time.UnixMilli(nowMs).Sub(fi.ModTime())
	return age >= 0 && age < workHeartbeatInterval
}

// spoolEvent appends one hookevents.Payload JSONL line. Off-mode drops the
// event; session-id tracking runs separately.
func spoolEvent(env HookEnv, nowMs int64, hookEvent, sessionID, title, sev string, data map[string]string) error {
	if env.Mode == "off" {
		return nil
	}
	if err := os.MkdirAll(spoolDir(env), 0o700); err != nil {
		hookLog(env.QuilDir, env.PaneID, "mkdir events dir failed: "+err.Error())
		return err
	}
	p := hookevents.Payload{
		V:         hookevents.SchemaVersion,
		TsMs:      nowMs,
		PaneID:    env.PaneID,
		Source:    hookevents.SourceCodex,
		HookEvent: hookEvent,
		SessionID: sessionID,
		Title:     title,
		Severity:  sev,
		Data:      data,
	}
	line, err := json.Marshal(p)
	if err != nil {
		hookLog(env.QuilDir, env.PaneID, "marshal payload failed: "+err.Error())
		return err
	}
	f, err := os.OpenFile(spoolPath(env), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		hookLog(env.QuilDir, env.PaneID, "open spool failed: "+err.Error())
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		hookLog(env.QuilDir, env.PaneID, "write spool failed: "+err.Error())
		return err
	}
	return nil
}

// truncate cuts s on a rune boundary so the result (with a trailing "…") stays
// within maxBytes and is valid UTF-8.
func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const ellipsis = "…"
	budget := maxBytes - len(ellipsis)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + ellipsis
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/codexhook`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codexhook
git commit -m "feat(codexhook): hook producer for the Codex CLI"
```

---

### Task 4: `internal/hookevents` — third source + work-state classification

**Files:**
- Modify: `internal/hookevents/types.go:44-52` (source consts), `:99-105` (Source doc), `:160-165` (Validate)
- Modify: `internal/hookevents/workstate.go:103-108` (IsWorkStateOnly), `:126-128` (IsWorkHeartbeat), `:131-215` (ClassifyWorkEvent)
- Test: `internal/hookevents/types_test.go`, `internal/hookevents/workstate_test.go`

**Interfaces:**
- Produces: `SourceCodex = "codex"`; `ClassifyWorkEvent` mappings for `hook.codex.*`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/hookevents/types_test.go`:

```go
// Codex is the third producer; a payload stamped with it must pass the
// daemon's ingest gate or every codex event is dropped at the spool.
func TestPayload_Validate_AcceptsCodex(t *testing.T) {
	t.Parallel()
	p := Payload{V: SchemaVersion, PaneID: "pane-abc", Source: SourceCodex, HookEvent: "Stop", Title: "Reply ready"}
	if err := p.Validate(); err != nil {
		t.Errorf("codex payload should validate; got %v", err)
	}
}
```

Add rows to the `tests` table in `TestClassifyWorkEvent` (`internal/hookevents/workstate_test.go`):

```go
		// Codex reuses Claude's event names; the mapping is the same minus
		// the events codex does not emit (Notification, StopFailure).
		{"hook.codex.UserPromptSubmit", WorkEventStart},
		{"hook.codex.PreToolUse", WorkEventStart},
		{"hook.codex.Stop", WorkEventStop},
		{"hook.codex.SessionEnd", WorkEventStopFinal},
		{"hook.codex.PermissionRequest", WorkEventPark},
		{"hook.codex.SubagentStart", WorkEventSubagentStart},
		{"hook.codex.SubagentStop", WorkEventSubagentStop},
		{"hook.codex.PreCompact", WorkEventNone},
		{"hook.codex.PostCompact", WorkEventNone},
```

Append to `internal/hookevents/workstate_test.go`:

```go
// The codex heartbeat must be kept off the sidebar and out of the daemon
// queue exactly like Claude's, and must not clear the unseen mark.
func TestCodexPreToolUse_IsWorkStateOnlyHeartbeat(t *testing.T) {
	t.Parallel()
	if !IsWorkStateOnly("hook.codex.PreToolUse") {
		t.Error("hook.codex.PreToolUse must be work-state-only")
	}
	if !IsWorkHeartbeat("hook.codex.PreToolUse") {
		t.Error("hook.codex.PreToolUse must be a heartbeat")
	}
	if IsWorkStateOnly("hook.codex.Stop") || IsWorkHeartbeat("hook.codex.UserPromptSubmit") {
		t.Error("only the heartbeat is work-state-only")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/hookevents`
Expected: FAIL — `SourceCodex` undefined; classifier rows return `WorkEventNone`.

- [ ] **Step 3: Implement**

`types.go`: add `SourceCodex = "codex"` to the source const block (update the block comment: "Hooks stamp their own source…"), extend the `Source` field comment to name all three, and make `Validate` accept it:

```go
	switch p.Source {
	case SourceClaude, SourceOpenCode, SourceCodex:
```

`workstate.go`:

```go
func IsWorkStateOnly(eventType string) bool {
	switch eventType {
	case "hook.claude.PostToolUse", "hook.claude.PreToolUse", "hook.codex.PreToolUse":
		return true
	}
	return false
}

func IsWorkHeartbeat(eventType string) bool {
	return eventType == "hook.claude.PreToolUse" || eventType == "hook.codex.PreToolUse"
}
```

In `ClassifyWorkEvent` extend the cases (codex reuses Claude's names; add each beside its Claude twin and a one-line comment "Codex emits Claude's event names; see internal/codexhook"):

```go
	case "hook.claude.UserPromptSubmit", "hook.opencode.chat.message", "hook.codex.UserPromptSubmit":
		return WorkEventStart
	…
	case "hook.claude.PreToolUse", "hook.codex.PreToolUse":
		return WorkEventStart
	case "hook.claude.Stop", "hook.codex.Stop",
		"hook.opencode.session.idle", "hook.opencode.session.error":
		return WorkEventStop
	…
	case "hook.claude.SessionEnd", "hook.codex.SessionEnd":
		return WorkEventStopFinal
	case "hook.claude.SubagentStart", "hook.codex.SubagentStart":
		return WorkEventSubagentStart
	case "hook.claude.SubagentStop", "hook.codex.SubagentStop":
		return WorkEventSubagentStop
	…
	case "hook.claude.PermissionRequest", "hook.opencode.permission.ask", "hook.codex.PermissionRequest":
		return WorkEventPark
```

Also update the package doc comment at the top of `types.go` ("sourced from Claude Code and OpenCode hooks" → "…, OpenCode and Codex hooks"; the wire-path diagram's "hook fires (claude .sh / opencode .js)" → "(quild claude-hook / codex-hook, opencode .js)").

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/hookevents`
Expected: PASS.

---

### Task 5: config knob, plugin name constant, default plugin TOML

**Files:**
- Modify: `internal/config/config.go:214-221` (struct), `:520-523` (defaults)
- Modify: `internal/plugin/plugin.go:254-262` (add `CodexPluginName`)
- Create: `internal/plugin/defaults/codex.toml`
- Test: `internal/plugin/defaults_test.go`, `internal/config/config_test.go` (append)

**Interfaces:**
- Produces: `config.HookNotificationsConfig.Codex string`; `plugin.CodexPluginName = "codex"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/plugin/defaults_test.go`:

```go
// TestEnsureDefaultPlugins_CodexShape pins the shipped codex plugin: the
// session_scrape strategy opts it into the restore branch's resume expansion,
// the EMPTY resume_args is what makes "no recorded session" a fresh start
// rather than codex's most-recent lookup, and record_history is what turns
// the hook's UserPromptSubmit into Alt+Shift+I history.
func TestEnsureDefaultPlugins_CodexShape(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	p, err := loadPluginTOML(filepath.Join(dir, "codex.toml"))
	if err != nil {
		t.Fatalf("load codex.toml: %v", err)
	}
	if p.Name != CodexPluginName || p.Command.Cmd != "codex" || p.Category != "ai" {
		t.Errorf("name/cmd/category = %q/%q/%q", p.Name, p.Command.Cmd, p.Category)
	}
	if !p.Command.PromptsCWD || !p.Command.RecordHistory || p.Command.Sessions != "" {
		t.Errorf("PromptsCWD=%v RecordHistory=%v Sessions=%q", p.Command.PromptsCWD, p.Command.RecordHistory, p.Command.Sessions)
	}
	if p.Persistence.Strategy != "session_scrape" {
		t.Errorf("strategy = %q, want session_scrape", p.Persistence.Strategy)
	}
	if len(p.Persistence.ResumeArgs) != 0 {
		t.Errorf("resume_args = %v, want empty (a pane with no recorded session starts fresh)", p.Persistence.ResumeArgs)
	}
	for _, a := range p.Command.Args {
		if a == "--last" {
			t.Error("codex.toml must never pass --last")
		}
	}
	if p.Persistence.GhostBuffer {
		t.Error("codex runs on the alternate screen; ghost replay must be off")
	}
	if !p.Display.WideCanvas {
		t.Error("codex is an AI transcript pane; wide_canvas must be on")
	}
	var groups = map[string]int{}
	var search bool
	for _, tg := range p.Command.Toggles {
		if tg.Group != "" {
			groups[tg.Group]++
		}
		for _, a := range tg.ArgsWhenOn {
			if a == "--search" {
				search = true
			}
			if a == "--last" || a == "--dangerously-bypass-hook-trust" {
				t.Errorf("toggle %q carries forbidden arg %q", tg.Name, a)
			}
		}
		if tg.Default {
			t.Errorf("toggle %q must default off", tg.Name)
		}
	}
	if groups["permission_mode"] != 2 {
		t.Errorf("want 2 toggles in the permission_mode group, got %d", groups["permission_mode"])
	}
	if !search {
		t.Error("want a --search toggle")
	}
}
```

Append to `internal/config/config_test.go` (check the file's existing helpers first; if there is a `Load` round-trip helper use it, else write the TOML to a temp `QUIL_HOME`):

```go
func TestConfig_HooksCodexKnob(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := Default()
	if cfg.Notification.Hooks.Codex != "default" {
		t.Errorf("default codex hooks tier = %q, want \"default\"", cfg.Notification.Hooks.Codex)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("QUIL_HOME"), "config.toml"), []byte("[notification.hooks]\ncodex = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Notification.Hooks.Codex != "off" {
		t.Errorf("loaded codex tier = %q, want off", loaded.Notification.Hooks.Codex)
	}
}
```

(Adjust `Default()`/`Load()` to the real names in `internal/config/config.go` — grep `func Default\|func Load` before writing.)

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/plugin` then `./scripts/dev.sh test ./internal/config`
Expected: FAIL — `codex.toml` missing; `Codex` field undefined.

- [ ] **Step 3: Implement**

`internal/config/config.go`:

```go
type HookNotificationsConfig struct {
	Claude   string `toml:"claude"`
	OpenCode string `toml:"opencode"`
	Codex    string `toml:"codex"`
}
```
and in the defaults block: `Codex: "default",`.

`internal/plugin/plugin.go`, after `ClaudeCodePluginName`:

```go
// CodexPluginName is the shipped Codex CLI plugin. Recognised by NAME at the
// spawn, restore and shutdown-refresh sites, the way opencode is: codex
// speaks Claude's hook JSON but not Claude's argv (`resume <id>`, no
// preassigned id), so it must never fall into the UsesClaudeSessions arm.
const CodexPluginName = "codex"
```

`internal/plugin/defaults/codex.toml`:

```toml
# Codex — OpenAI's coding agent CLI (github.com/openai/codex)
# Edit this file to customize the plugin. Delete it to restore defaults.
#
# Diagnostic logging: the hook Quil registers per pane (a `-c hooks=…`
# override carrying its own trust hash — nothing under ~/.codex is touched)
# appends breadcrumbs to $QUIL_HOME/codexhook/hook.log. Safe to delete.

[plugin]
name = "codex"
display_name = "Codex"
category = "ai"
description = "OpenAI Codex coding agent"
homepage = "https://github.com/openai/codex"
schema_version = 1

[command]
cmd = "codex"
# path = "/path/to/codex"  # uncomment to override PATH lookup
detect = "codex --version"
prompts_cwd = true  # ask which folder to open codex in (project trust + AGENTS.md live there)
record_history = true  # capture submitted prompts to <quilDir>/history/<paneID>.jsonl (hook producer)

# Runtime toggles — rendered in the pane setup dialog.
#
# The two below share group = "permission_mode" and are mutually exclusive
# (radio buttons). Both default off, which leaves codex's own interactive
# approvals in place.
[[command.toggles]]
name = "bypass_approvals_and_sandbox"
label = "Bypass approvals and sandbox (dangerous)"
args_when_on = ["--dangerously-bypass-approvals-and-sandbox"]
default = false
group = "permission_mode"

[[command.toggles]]
name = "auto_workspace_write"
label = "Auto: workspace-write sandbox, never ask"
args_when_on = ["-a", "never", "-s", "workspace-write"]
default = false
group = "permission_mode"

# Independent of the permission mode.
[[command.toggles]]
name = "search"
label = "Web search"
args_when_on = ["--search"]
default = false

[persistence]
# The daemon promotes the restore args to ["resume", "<session_id>"] when the
# hook recorded a session id for this pane (see codexResumeTemplate in
# internal/daemon/daemon.go). resume_args is deliberately EMPTY: a pane with no
# recorded session starts fresh. `resume --last` is codex's most-recent-session
# lookup, and on restore that finds the sibling pane that respawned a second
# earlier — the same trap `claude --continue` is.
strategy = "session_scrape"
resume_args = []
# Codex runs on the alternate screen by default, so a byte replay would paint
# garbage; `resume <id>` repaints the conversation itself.
ghost_buffer = false

[[error_handlers]]
pattern = '(?i)not logged in|run .*codex login|please log ?in'
title = "Codex Not Logged In"
message = "Run 'codex login' to sign in."
action = "dialog"

# Idle handlers — checked when pane goes idle (last 5 lines analyzed)
[[idle_handlers]]
pattern = '(?i)waiting for (confirmation|input|approval|permission)'
title = "Needs your approval"
severity = "warning"

[[idle_handlers]]
pattern = '(?i)(plan|question|choose|select|pick)'
title = "Waiting for your choice"
severity = "warning"

[[idle_handlers]]
pattern = '❯|> $'
title = "Waiting for input"
severity = "info"

[display]
# Window-sized canvas: the PTY never resizes with the pane rect; small
# panes show a soft-wrapped preview. Zoom (Ctrl+E) shows the native render.
wide_canvas = true
```

Check `internal/plugin/registry.go` `loadPluginTOML` accepts `homepage` under `[plugin]` (k9s.toml uses it) and an empty `resume_args = []`.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/plugin` then `./scripts/dev.sh test ./internal/config`
Expected: PASS.

---

### Task 6: daemon wiring — spawn prep, restore template, shutdown refresh, tier gate

**Files:**
- Modify: `internal/daemon/daemon.go` — imports; `refreshPluginStateFromHooks` (`:432-460`); after `readOpencodeSessionIDFn` (`:3752`) add `readCodexSessionFn` + `codexSpawnPrep`; `resumeTemplateFor` (`:3870`); after `opencodeResumeTemplate` (`:4100`) add `codexResumeTemplate`; `spawnPane` switch (`:4340-4352`); `emitHookEvent` (`:4566-4575`)
- Test: `internal/daemon/spawn_env_test.go`, `internal/daemon/spawn_args_test.go`

**Interfaces:**
- Consumes: `codexhook.HookCommand`, `codexhook.ConfigOverrideArgs`, `codexhook.IsShim`, `codexhook.ReadPersistedSession`, `codexhook.IsValidSessionID`, `plugin.CodexPluginName`, `hookevents.SourceCodex`, `config.HookNotificationsConfig.Codex`.
- Produces: `readCodexSessionFn` (test seam), `codexSpawnPrep(quilDir, paneID, hookMode, resolvedCmd string) (prefix, env []string)`, `codexResumeTemplate(p, pane) []string`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/spawn_env_test.go`:

```go
// TestCodexSpawnPrep_PaneEnvUsesHookHome mirrors the claude/opencode tests:
// children inherit the pane env, so QUIL_HOME must not appear there.
func TestCodexSpawnPrep_PaneEnvUsesHookHome(t *testing.T) {
	orig := claudeHookExeFn
	claudeHookExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { claudeHookExeFn = orig }()

	prefix, env := codexSpawnPrep("/data/quil", "pane-cx123", "default", "/usr/local/bin/codex")
	assertHookHomeOnly(t, env, "/data/quil")
	if len(prefix) != 2 || prefix[0] != "-c" || !strings.HasPrefix(prefix[1], "hooks={") {
		t.Errorf("prefix = %q, want [-c hooks={…}]", prefix)
	}
	if !strings.Contains(prefix[1], `codex-hook`) || !strings.Contains(prefix[1], "trusted_hash=") {
		t.Errorf("prefix must register the codex-hook command with trust: %s", prefix[1])
	}
	for _, kv := range env {
		if kv == "QUIL_PANE_ID=pane-cx123" || kv == "QUIL_HOOK_MODE=default" {
			continue
		}
		if strings.HasPrefix(kv, "QUIL_HOOK_HOME=") {
			continue
		}
		t.Errorf("unexpected env entry %q", kv)
	}
}

// An npm-installed codex is a cmd.exe shim that re-parses the quotes in the
// override; the spawn must then proceed WITHOUT the hook rather than with a
// mangled argument.
func TestCodexSpawnPrep_ShimDisablesHooks(t *testing.T) {
	orig := claudeHookExeFn
	claudeHookExeFn = func() (string, error) { return "/fake/quild", nil }
	defer func() { claudeHookExeFn = orig }()

	prefix, env := codexSpawnPrep("/data/quil", "pane-cx123", "default", `C:\Users\x\AppData\Roaming\npm\codex.cmd`)
	if prefix != nil || env != nil {
		t.Errorf("shim: prefix=%q env=%q, want nil/nil", prefix, env)
	}
}

func TestCodexSpawnPrep_UnresolvableExeDisablesHooks(t *testing.T) {
	orig := claudeHookExeFn
	claudeHookExeFn = func() (string, error) { return "", errors.New("no exe") }
	defer func() { claudeHookExeFn = orig }()

	prefix, env := codexSpawnPrep("/data/quil", "pane-cx123", "default", "/usr/local/bin/codex")
	if prefix != nil || env != nil {
		t.Errorf("prefix=%q env=%q, want nil/nil", prefix, env)
	}
}
```
(add `"errors"` to that file's imports.)

Append to `internal/daemon/spawn_args_test.go`:

```go
// TestResolveSpawnArgs_CodexResume covers the codex restore branch of
// resumeTemplateFor: a recorded id becomes `resume <id>`, anything else
// starts FRESH — the plugin ships resume_args = [] because `resume --last`
// is codex's most-recent-session lookup, the same sibling trap as
// `claude --continue`.
func TestResolveSpawnArgs_CodexResume(t *testing.T) {
	codexPlugin := &plugin.PanePlugin{
		Name:    plugin.CodexPluginName,
		Command: plugin.CommandConfig{Cmd: "codex"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "session_scrape",
			ResumeArgs: nil,
		},
	}
	const sid = "01a05db1-9f44-73b2-b426-8aad5f5232f4"

	tests := []struct {
		name string
		pane *Pane
		rec  codexhook.SessionRecord
		err  error
		want []string
	}{
		{"recorded id — resume by id", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{ID: sid}, nil, []string{"resume", sid}},
		{"recorded id keeps runtime toggles", &Pane{ID: "pane-abc", InstanceArgs: []string{"--search"}}, codexhook.SessionRecord{ID: sid}, nil, []string{"--search", "resume", sid}},
		{"no record — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{}, os.ErrNotExist, nil},
		{"empty id — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{}, nil, nil},
		{"malformed id — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{ID: "--last"}, nil, nil},
		{"non-uuid id — fresh start", &Pane{ID: "pane-abc"}, codexhook.SessionRecord{ID: "ses_abc"}, nil, nil},
	}

	orig := readCodexSessionFn
	t.Cleanup(func() { readCodexSessionFn = orig })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
				if paneID != tt.pane.ID {
					t.Errorf("read paneID = %q, want %q", paneID, tt.pane.ID)
				}
				return tt.rec, tt.err
			}
			got := resolveSpawnArgs(codexPlugin, tt.pane, true, "", claimAny)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
			if len(tt.rec.ID) > 0 && tt.want != nil {
				tt.pane.PluginMu.Lock()
				sidState := tt.pane.PluginState["session_id"]
				tt.pane.PluginMu.Unlock()
				if sidState != tt.rec.ID {
					t.Errorf("PluginState[session_id] = %q, want %q", sidState, tt.rec.ID)
				}
			}
		})
	}
}

// A codex pane must never take the claude arm even if its TOML is edited to
// say sessions = "claude": that arm would prepend --settings and read the
// wrong record file.
func TestResumeTemplateFor_CodexArmWinsOverClaudeCapability(t *testing.T) {
	orig := readCodexSessionFn
	t.Cleanup(func() { readCodexSessionFn = orig })
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: "01a05db1-9f44-73b2-b426-8aad5f5232f4"}, nil
	}
	p := &plugin.PanePlugin{
		Name:        plugin.CodexPluginName,
		Command:     plugin.CommandConfig{Cmd: "codex", Sessions: plugin.ClaudeSessionSource},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape"},
	}
	got := resumeTemplateFor(p, &Pane{ID: "pane-abc"}, claimAny)
	if !reflect.DeepEqual(got, []string{"resume", "{session_id}"}) {
		t.Errorf("template = %v", got)
	}
}
```
(add `"github.com/artyomsv/quil/internal/codexhook"` to that file's imports.)

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/daemon`
Expected: FAIL — `codexSpawnPrep`, `readCodexSessionFn` undefined.

- [ ] **Step 3: Implement**

Imports in `daemon.go`: add `"runtime"` (if absent) and `"github.com/artyomsv/quil/internal/codexhook"`.

After `readOpencodeSessionIDFn`:

```go
// readCodexSessionFn mirrors readOpencodeSessionIDFn for the codex pane type.
// Tests override it so the spawn-args matrix never touches $QUIL_HOME/sessions/.
var readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
	return codexhook.ReadPersistedSession(config.QuilDir(), paneID)
}

// codexSpawnPrep returns the argv prefix and env vars to add to a fresh codex
// spawn so the hook registers. The prefix is ONE `-c hooks=…` override that
// carries every event registration and the trust hash codex requires for
// each (see internal/codexhook) — codex has no `--settings <file>`, and the
// session-flags layer is the only one Quil can write without touching
// ~/.codex. Returns nil slices when the hook is unavailable so the spawn
// proceeds without it, matching the claude path.
//
// resolvedCmd is the codex binary the pane will run. A `.cmd`/`.bat` shim (an
// npm install on Windows) re-parses the command line through cmd.exe with
// different quoting rules, and the override value carries quotes — the same
// bug class the inline Claude --settings JSON hit, with no file form to fall
// back to. So a shim disables the hook rather than risk a split argument.
func codexSpawnPrep(quilDir, paneID, hookMode, resolvedCmd string) (prefix, env []string) {
	if codexhook.IsShim(resolvedCmd) {
		log.Printf("warning: pane %s: codex resolves to a cmd.exe shim (%s); the inline hook override cannot survive its re-parse — codex hooks disabled (notifications, work state, input history, session resume). Install the native codex binary or set [command] path in codex.toml", paneID, resolvedCmd)
		return nil, nil
	}
	exePath, err := claudeHookExeFn()
	if err != nil {
		log.Printf("warning: pane %s: cannot resolve quild executable: %v — codex hooks disabled (notifications, work state, input history, session resume)", paneID, err)
		return nil, nil
	}
	prefix, err = codexhook.ConfigOverrideArgs(codexhook.HookCommand(exePath), runtime.GOOS)
	if err != nil {
		log.Printf("warning: pane %s: build codex hook override: %v — codex hooks disabled", paneID, err)
		return nil, nil
	}
	mode := hookMode
	if mode == "" {
		mode = "default"
	}
	env = []string{
		"QUIL_PANE_ID=" + paneID,
		"QUIL_HOOK_MODE=" + mode,
		"QUIL_HOOK_HOME=" + quilDir,
	}
	return prefix, env
}
```

`resumeTemplateFor`:

```go
	switch {
	case p.Name == plugin.CodexPluginName && p.Persistence.Strategy == "session_scrape":
		return codexResumeTemplate(p, pane)
	case p.UsesClaudeSessions() && p.Persistence.Strategy == "preassign_id":
		return claudeResumeTemplate(p, pane, claim)
	case p.Name == "opencode" && p.Persistence.Strategy == "session_scrape":
		return opencodeResumeTemplate(p, pane)
```
(codex first: the claude arm is capability-based and a codex TOML may legally set `sessions = "claude"`.)

After `opencodeResumeTemplate`:

```go
// codexResumeTemplate decides how a restored codex pane respawns: the hook's
// recorded id becomes `resume <id>`; anything else falls back to the plugin's
// ResumeArgs, which the shipped TOML leaves empty so the pane starts FRESH.
// `resume --last` is never emitted — it is codex's most-recent-session-in-CWD
// lookup, and on restore that is the sibling that respawned a second earlier.
//
// The id is shape-checked (canonical UUID) before it becomes argv, and logged
// by length only: the value comes from a file a pane's own child wrote.
func codexResumeTemplate(p *plugin.PanePlugin, pane *Pane) []string {
	rec, err := readCodexSessionFn(pane.ID)
	if err != nil || rec.ID == "" {
		return p.Persistence.ResumeArgs
	}
	if !codexhook.IsValidSessionID(rec.ID) {
		log.Printf("warning: pane %s: recorded codex session id failed shape validation (len=%d); starting fresh", pane.ID, len(rec.ID))
		return p.Persistence.ResumeArgs
	}
	pane.PluginMu.Lock()
	if pane.PluginState == nil {
		pane.PluginState = make(map[string]string)
	}
	pane.PluginState["session_id"] = rec.ID
	if rec.TranscriptPath != "" {
		pane.PluginState["transcript_path"] = rec.TranscriptPath
	} else {
		delete(pane.PluginState, "transcript_path")
	}
	pane.PluginMu.Unlock()
	return []string{"resume", "{session_id}"}
}
```

`refreshPluginStateFromHooks` — add an arm before the claude one:

```go
			case pane.Type == plugin.CodexPluginName:
				if rec, err := readCodexSessionFn(pane.ID); err == nil {
					hookID, transcript = rec.ID, rec.TranscriptPath
				}
```

`spawnPane` switch — add before the claude arm:

```go
	case p.Name == plugin.CodexPluginName:
		// The hook needs the RESOLVED binary to refuse a cmd.exe shim; the
		// LookPath below runs after this switch, so resolve here as well.
		resolvedCmd := cmd
		if r, err := exec.LookPath(cmd); err == nil {
			resolvedCmd = r
		}
		prefix, hookEnv := codexSpawnPrep(config.QuilDir(), pane.ID, d.cfg.Notification.Hooks.Codex, resolvedCmd)
		if len(prefix) > 0 {
			// `-c` is global, so it precedes both a fresh start and the
			// `resume <id>` subcommand the restore branch appends.
			args = append(prefix, args...)
		}
		envVars = append(envVars, hookEnv...)
```

`emitHookEvent`:

```go
		case hookevents.SourceCodex:
			mode = d.cfg.Notification.Hooks.Codex
```

Also update the comment block above the switch in `spawnPane` (mentions Claude and OpenCode) with one sentence: "Codex rides a `-c hooks=…` override carrying its own trust hashes (see internal/codexhook)."

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/daemon`
Expected: PASS (whole package, including the pre-existing resume matrix).

---

### Task 7: `quild codex-hook` subcommand + TUI resume label

**Files:**
- Modify: `cmd/quild/hook.go` (add `runCodexHook`), `cmd/quild/main.go:49-52` (dispatch)
- Modify: `internal/tui/pane.go:947-956` (`resumeLabel`)
- Test: `internal/tui/pane_test.go` or wherever `resumeLabel` is tested (grep `resumeLabel(` in `internal/tui/*_test.go`)

- [ ] **Step 1: Write the failing test**

Find the existing `resumeLabel` test table and add:

```go
		{"codex", "01a05db1-9f44-73b2-b426-8aad5f5232f4", true, "resuming codex 01a05db1"},
```
(match the exact expected format the neighbouring `opencode` row uses.)

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui`
Expected: FAIL — label reads "starting codex …".

- [ ] **Step 3: Implement**

`internal/tui/pane.go`:

```go
	case "codex":
		base = "resuming codex"
```

`cmd/quild/hook.go` — add beside `runClaudeHook` (import `internal/codexhook`):

```go
// runCodexHook handles the `quild codex-hook` subcommand: one Codex hook
// invocation (JSON on stdin) → the session record or a spool line under
// $QUIL_HOOK_HOME. Same fast-path contract as runClaudeHook: no daemon, no
// logger, no config from disk, always exit 0.
func runCodexHook() {
	_ = codexhook.RunHook(os.Stdin, codexhook.HookEnv{
		PaneID:        os.Getenv("QUIL_PANE_ID"),
		QuilDir:       hookHomeDir(),
		Mode:          os.Getenv("QUIL_HOOK_MODE"),
		RecordHistory: os.Getenv("QUIL_RECORD_HISTORY") == "1",
	}, time.Now().UnixMilli())
}
```

`cmd/quild/main.go`:

```go
	if len(os.Args) > 1 && os.Args[1] == "claude-hook" {
		runClaudeHook()
		return
	}
	// Same contract for the Codex hook (see internal/codexhook).
	if len(os.Args) > 1 && os.Args[1] == "codex-hook" {
		runCodexHook()
		return
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/tui` and `./scripts/dev.sh vet`
Expected: PASS / clean.

- [ ] **Step 5: Commit**

```bash
git add internal/hookevents internal/config internal/plugin internal/daemon cmd/quild internal/tui/pane.go internal/tui/*_test.go
git commit -m "feat(codex): Codex pane plugin with hooks, work state and resume"
```

---

### Task 8: docs, agent rules, changelog fragment

**Files:**
- Modify: `docs/plugin-reference.md:13,84,91` ; `docs/features.md:11,69-75,235,253,402-415` ; `docs/configuration.md:69,192-197` ; `docs/mcp.md:188` ; `docs/troubleshooting.md:174,200,308` ; `README.md:73,206` ; `docs/quick-start.md:41,53` ; `docs/keybindings.md:30,105`
- Modify: `.claude/rules/hooks-and-sessions.md` (paths + new section), `.claude/CLAUDE.md:38,136`
- Create: `changelog.d/added-codex-plugin.md`
- Include: `docs/superpowers/specs/2026-09-04-codex-plugin-design.md`, `docs/superpowers/plans/2026-09-04-codex-plugin.md`

- [ ] **Step 1: Write the changelog fragment**

`changelog.d/added-codex-plugin.md`:

```markdown
---
headline: Codex panes with notifications, work state and resume
---
- **Codex (OpenAI's coding agent CLI) is a built-in pane type.** `Ctrl+N` → Codex
  opens it in a folder of your choice, with the same setup toggles Claude Code has
  (bypass approvals and sandbox, or auto workspace-write; web search).

  Quil registers its hook with codex per pane — a `-c hooks=…` override that carries
  its own trust hash, so nothing under `~/.codex` is touched and no trust prompt
  appears — and everything the Claude Code plugin derives from hooks works for
  Codex too: the notification sidebar (permission asks, "reply ready", compaction),
  the work-in-progress spinner and green/amber tab marks, subagent tracking, the
  model and context-token status segment, `Alt+Shift+I` input history, and
  per-pane session resume after a daemon restart (`codex resume <id>`; a pane
  with no recorded session starts fresh, never on a sibling's conversation).

  Tier knob: `[notification.hooks] codex = "default" | "verbose" | "off"`.
```

- [ ] **Step 2: Update the user docs**

Each site listed in **Files** gains codex beside opencode. Minimum wording:

- `docs/plugin-reference.md:13` → "5 built-in TOML plugins (claude-code, opencode, codex, ssh, stripe)"; `:84` add "(and `-c hooks=…` on the argv for codex)"; `:91` → "currently the `claude-code` and `codex` defaults".
- `docs/features.md`: TOC + a new `### Codex session-id tracking` after the OpenCode one: "Codex mints a new session id on `/new` and reports it through its Claude-compatible `SessionStart` hook. Quil registers `quild codex-hook` per pane with a `-c hooks=…` override that also carries the hook's trust hash (codex runs only trusted hooks; the hash is computed by Quil, so nothing under `~/.codex` is read or written). The record lands at `$QUIL_HOME/sessions/codex-<paneID>.id` and restore runs `codex resume <id>`; a pane with no record starts fresh — `resume --last` is never used." `:75` add "(or `codex resume <id>`)"; `:235` → "the built-in **Claude Code** and **Codex** plugins set it. OpenCode support is planned."; `:253` add a `| **Codex** | AI Assistant | Claude-compatible hooks via a trusted `-c hooks=…` override; restore via `resume <id>` |` row; `:402-415` name three sources and add "Codex `SessionEnd`, `UserPromptSubmit`, `PermissionRequest`, `Stop`, `PreCompact`/`PostCompact`, `SubagentStart/Stop`, plus the throttled `PreToolUse` heartbeat".
- `docs/configuration.md:69` add `codex = "default"            # same`; `:197` add a `codex` row: "Tier for Codex panes. `\"default\"` forwards SessionEnd, UserPromptSubmit, PermissionRequest, Stop, PreCompact/PostCompact, SubagentStart/Stop and the throttled PreToolUse heartbeat. `\"off\"` disables hook event forwarding (session-id tracking stays on)."
- `docs/mcp.md:188` add `codex` to the `type` list; `docs/troubleshooting.md:174` add `codex`; `:200` add "For Codex: `~/.quil/codexhook/hook.log` and `~/.quil/sessions/codex-<pane-id>.id`."; `:308` add a row `| ~/.quil/codexhook/hook.log | Breadcrumbs from the Codex hook subcommand |`.
- `README.md:73`, `docs/quick-start.md:41,53`, `docs/keybindings.md:30,105`: add "Codex" to the AI tool lists; `README.md:206` add a row `| **Codex** | AI coding session ([codex](https://github.com/openai/codex)) with per-pane session resume and hook-driven notifications. |`.

- [ ] **Step 3: Update the agent context**

`.claude/rules/hooks-and-sessions.md`: add `- "**/internal/codexhook/**"` to `paths:`; add after the `internal/opencodehook/` section:

```markdown
### `internal/codexhook/`

Codex CLI hook producer — sibling of `claudehook`, sharing NO code with it on purpose (that path is incident-hardened; codex differences would live inside it as branches). Codex speaks Claude's hook JSON (same `hook_event_name`/`session_id`/`transcript_path`/`agent_id` fields) but has no `--settings <file>` and runs only TRUSTED hooks. Quil passes ONE argv token, `-c hooks={<Event>=[{hooks=[{type="command",command="\"<quild>\" codex-hook",timeout=N}]}],…,state={"<key>"={trusted_hash="sha256:…"}}}` (`BuildConfigOverride`), where `<key>` is `C:\<session-flags>\config.toml:<event_snake>:0:0` (Windows) / `/<session-flags>/config.toml:…` and the hash is SHA-256 of codex's canonical identity JSON (`{"event_name","hooks":[{"async":false,"command","timeout","type":"command"}]}`, keys sorted, HTML escaping OFF). Verified on codex-cli 0.146.0 (2026-09-04): with the hash the hook runs, without it codex silently skips it, and `~/.codex` is never touched. Timeouts are written EXPLICITLY (600; SessionEnd 3, codex's cap) so the hash never depends on a codex default — a wrong guess is not a failed spawn, it is codex's startup review prompting in every pane. Registered: SessionStart, SessionEnd, UserPromptSubmit, PermissionRequest, PreToolUse (throttled heartbeat, `agent_id` gate, as Claude's), Stop, SubagentStart/Stop, PreCompact/PostCompact. NOT PostToolUse (no prompt tools to match; the keystroke clears a park) and NOT Interrupt (absent from 0.146.0; the TUI synthesises ESC for every pane). Hooks stay SYNCHRONOUS: async handlers run concurrently and could deliver a Stop before its UserPromptSubmit. `IsShim` refuses a `.cmd`/`.bat` codex (npm install on Windows) — cmd.exe would re-parse the quoted override, the Claude inline-JSON bug with no file fallback — and `codexSpawnPrep` (daemon.go) logs and spawns without hooks. Record: `$QUIL_HOME/sessions/codex-<paneID>.id`, two lines (id, rollout path), read by `readCodexSessionFn`; restore (`codexResumeTemplate`) emits `resume <id>` and otherwise the plugin's EMPTY `resume_args` — `resume --last` is codex's most-recent lookup, the `--continue` sibling trap. `Stop` carries `data.model` from the payload and `data.context_tokens` from the rollout's last `token_count` line (`last_token_usage.total_tokens`, codex's own `tokens_in_context_window`). Tier knob `[notification.hooks] codex`. Breadcrumbs: `$QUIL_HOME/codexhook/hook.log`. Follow-ups: `sessions = "codex"` picker (date-sharded tree, needs a source on the IPC), Interrupt once the installed codex has it.
```

Add `hook.codex.*` to the work-state section's start/stop edge lists (one clause each). `.claude/CLAUDE.md:38` add `internal/codexhook/`; `:136` add `codexhook/` to the rule's file list. Run `./scripts/dev.sh docs-size`.

- [ ] **Step 4: Validate**

Run: `sh scripts/promote-changelog.sh --validate` and `./scripts/dev.sh docs-size`
Expected: both clean.

- [ ] **Step 5: Commit**

```bash
git add docs README.md changelog.d/added-codex-plugin.md .claude/rules/hooks-and-sessions.md .claude/CLAUDE.md
git commit -m "docs(codex): document the Codex plugin and its hook trust model"
```

---

### Task 9: full verification

- [ ] **Step 1: Full suite + vet + race**

Run: `./scripts/dev.sh vet`, `./scripts/dev.sh test`, `./scripts/dev.sh test-race`
Expected: all green.

- [ ] **Step 2: Build**

Run: `./scripts/dev.sh build` — check `quild-dev.exe` mtime moved (a held binary makes build refuse; see `refuse_if_binaries_held`).

- [ ] **Step 3: Runtime check (dev daemon, per `.claude/rules/dev-environment.md` and the `verify` skill)**

1. Launch `./quil-dev.exe`; confirm `[dev]` in the status bar.
2. `Ctrl+N` → Codex → a folder → Continue. Codex starts with NO hook-trust prompt.
3. Send a prompt. Expect: spinner on the tab while codex works; a "Reply ready" sidebar card and a green tab when it finishes; the status bar shows `<model> · <n>k ctx`.
4. `Alt+Shift+I` lists the prompt.
5. Note the id in `.quil/sessions/codex-<paneID>.id` (dev tree, NOT `~/.quil`). Quit the TUI, stop the dev daemon by its pid in `.quil/quild.pid`, relaunch `./quil-dev.exe`. Expect the pane to come back on the same conversation ("resuming codex <id>" label, then codex's own history repainted).
6. `.quil/quild.log` shows `spawn: … args=[-c hooks={… resume <id>]`.

- [ ] **Step 4: Record the observed hook stdin** if anything differs from the schemas (e.g. an unpaired SubagentStop shape) — update `runhook.go` and its tests, never a guess.
