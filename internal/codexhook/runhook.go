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
// never blocked. It returns an error only so the subcommand and tests can
// observe failures. nowMs is injected for deterministic tests.
func RunHook(r io.Reader, env HookEnv, nowMs int64) error {
	if env.PaneID == "" {
		return nil // invoked outside Quil
	}
	// The pane id arrives via $QUIL_PANE_ID and builds paths under sessions/
	// and events/; a hostile value must not escape them or forge a log line.
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

// dispatchHookEvent routes a decoded payload to the record writer
// (SessionStart) or the spool (every other registered event).
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
		// Off mode is checked before the usage read so a disabled tier never
		// pays the rollout retries for a line that will not be written.
		if env.Mode == "off" {
			return nil
		}
		return spoolEvent(env, nowMs, "Stop", in.SessionID, "Reply ready", hookevents.SeverityWarning,
			modelUsageData(env, in.Model, in.TranscriptPath))
	case "PreToolUse":
		// Work-spinner START edge for a turn no user prompt began — a
		// heartbeat, not a per-call stream (see claudehook for the measured
		// trace). Subagent calls are dropped: turnActive is a statement
		// about the MAIN turn, and the subagent ledger already keeps such a
		// pane working via SubagentStart/Stop.
		if in.AgentID != "" {
			return nil
		}
		// Checked here as well as inside spoolEvent: that one returns before
		// writing, so the spool never exists and the throttle below would
		// stat a missing file once per tool call forever.
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
			// Names no agent: the TUI ledger could match it to nothing, and
			// spooled it would only become a " done" card. Same refusal as
			// claudehook's.
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
		// Forward-compat: codex may add events (Interrupt is one). Drop with
		// a breadcrumb rather than erroring.
		hookLog(env.QuilDir, env.PaneID, "unhandled hook_event: "+in.HookEventName)
		return nil
	}
}

// rolloutRetryDelays paces the re-reads in modelUsageData: codex appends the
// final token_count line around the moment Stop hooks fire.
var rolloutRetryDelays = []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond}

// modelUsageData returns the model + context-token Data keys for a Stop, or
// nil when either half is unavailable — the daemon sets the status segment
// only when both are present. The model comes from the payload; the token
// count from the rollout tail. A failed read leaves one breadcrumb.
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

// spoolDir and spoolPath are the ONE definition of where a pane's spool lives;
// the reader (spoolIsFresh) and the writer (spoolEvent) must agree, or the
// throttle stats a file nobody writes and silently stops throttling.
func spoolDir(env HookEnv) string  { return filepath.Join(env.QuilDir, "events") }
func spoolPath(env HookEnv) string { return filepath.Join(spoolDir(env), env.PaneID+".jsonl") }

// workHeartbeatInterval is how long a pane may stay silent before a tool call
// is worth spooling as proof of work. Same value as claudehook's.
const workHeartbeatInterval = 15 * time.Second

// spoolIsFresh reports whether Quil has heard from this pane within
// workHeartbeatInterval of nowMs, judged by the spool's mtime. A missing spool
// or a future mtime both resolve toward speaking: a surplus line costs one
// sidebar-suppressed event, a missing one costs the only cue that work is
// happening.
func spoolIsFresh(env HookEnv, nowMs int64) bool {
	fi, err := os.Stat(spoolPath(env))
	if err != nil {
		return false
	}
	age := time.UnixMilli(nowMs).Sub(fi.ModTime())
	return age >= 0 && age < workHeartbeatInterval
}

// spoolEvent appends one hookevents.Payload JSONL line to the pane's spool.
// Off-mode drops the event; session-id tracking runs separately.
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
	const ellipsis = "…" // 3 bytes UTF-8
	budget := maxBytes - len(ellipsis)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i := range s { // i is the byte index of each rune start
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + ellipsis
}
