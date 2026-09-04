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
//
// This is a sibling of internal/claudehook that deliberately shares no code
// with it: that path is incident-hardened, and codex's differences (the
// override, the rollout usage reader, the record file, no prompt-tool gate)
// would otherwise live inside it as branches.
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
//
// Every handler is SYNCHRONOUS (no `async`): async handlers run concurrently
// on codex's side and could deliver a Stop before its own UserPromptSubmit,
// which the TUI would replay as a stop edge for a turn it never saw start.
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
