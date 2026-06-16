package claudehook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/panehistory"
)

// HookEnv carries the per-invocation context the hook needs, sourced from the
// QUIL_* environment the daemon sets on a claude-code pane at spawn.
type HookEnv struct {
	PaneID  string // QUIL_PANE_ID — empty means "invoked outside Quil" (no-op)
	QuilDir string // resolved via QUIL_HOOK_HOME (QUIL_HOME fallback) — root for sessions/ and events/
	Mode    string // QUIL_HOOK_MODE: "default" | "verbose" | "off"

	RecordHistory bool // QUIL_RECORD_HISTORY=1 — append full prompts to the history store
}

// maxStdinBytes caps how much of Claude's hook stdin we read. The payload can
// carry a full user prompt; 1 MiB is far above any realistic hook JSON while
// still bounding a pathological producer.
const maxStdinBytes = 1 << 20

// claudeStdin mirrors the subset of Claude Code's hook JSON Quil reads. Extra
// fields in the payload are ignored by encoding/json.
type claudeStdin struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Prompt        string `json:"prompt"`
	Message       string `json:"message"`
	ToolName      string `json:"tool_name"`
	Reason        string `json:"reason"`
	AgentType     string `json:"agent_type"`
	Content       string `json:"content"`
}

// sessionIDRe matches the Claude session-id shape (uuid-ish hex). Mirrors the
// `^[0-9a-fA-F-]{32,64}$` guard the shell hooks used before writing the file.
var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{32,64}$`)

// RunHook processes one Claude Code hook invocation. It reads the hook JSON
// from r and routes by hook_event_name:
//
//   - SessionStart writes the rotating session-id file (resume infrastructure)
//   - every other forwarded event appends one hookevents.Payload JSONL line to
//     the pane's spool file, which the daemon's watcher picks up within 200 ms.
//
// Best-effort by contract: an empty pane id is a no-op (Claude invoked outside
// Quil), and filesystem failures are logged to $QuilDir/claudehook/hook.log.
// It returns an error only so the subcommand and tests can observe failures;
// the subcommand always exits 0 so Claude is never blocked. nowMs is injected
// for deterministic tests (the subcommand passes time.Now().UnixMilli()).
//
// Unlike the shell producers this replaces, the spool line is built with
// encoding/json — no hand-rolled escaping, no codepage/BOM hazard.
func RunHook(r io.Reader, env HookEnv, nowMs int64) error {
	if env.PaneID == "" {
		return nil // invoked outside Quil
	}
	// Defense-in-depth: the pane id arrives via $QUIL_PANE_ID and is used to
	// build file paths under sessions/ and events/. The daemon only ever sets
	// a validated uuid-hex id, but a future/hostile caller must not be able to
	// escape those dirs or forge a log line. Log without echoing the raw id.
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
	// Tolerate a leading UTF-8 BOM defensively — some upstream wrappers add one.
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	var in claudeStdin
	if err := json.Unmarshal(raw, &in); err != nil {
		hookLog(env.QuilDir, env.PaneID, "parse stdin failed")
		return err
	}
	return dispatchHookEvent(env, in, nowMs)
}

// dispatchHookEvent routes a decoded Claude hook payload to the session-file
// writer (SessionStart) or the spool (every other forwarded event). Split out
// of RunHook so the read/decode/validate path stays short and the per-event
// mapping is a single focused unit.
func dispatchHookEvent(env HookEnv, in claudeStdin, nowMs int64) error {
	switch in.HookEventName {
	case "SessionStart":
		return writeSessionFile(env, in.SessionID)
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
	case "Notification":
		return spoolEvent(env, nowMs, "Notification", in.SessionID,
			truncate(in.Message, hookevents.MaxTitleBytes), hookevents.SeverityWarning, nil)
	case "PermissionRequest":
		return spoolEvent(env, nowMs, "PermissionRequest", in.SessionID,
			truncate("Needs approval: "+in.ToolName, hookevents.MaxTitleBytes), hookevents.SeverityWarning,
			map[string]string{"tool": truncate(in.ToolName, hookevents.MaxDataValueBytes)})
	case "Stop":
		return spoolEvent(env, nowMs, "Stop", in.SessionID, "Reply ready", hookevents.SeverityWarning, nil)
	case "PostToolUse":
		// Work-spinner RESUME edge. Registered with a tool-name matcher
		// (claudehook.promptToolMatcher) so Claude only fires it for the
		// interactive-prompt tools, whose PostToolUse marks the moment the user
		// answered and the agent resumes work. The defensive tool gate below
		// mirrors the matcher in case a future settings change widens it — we
		// never want to spool a Read/Bash/Edit completion here. This event drives
		// work-state only; the TUI suppresses it from the notification sidebar.
		if !isPromptTool(in.ToolName) {
			return nil
		}
		hookLog(env.QuilDir, env.PaneID, "PostToolUse resume tool="+in.ToolName)
		return spoolEvent(env, nowMs, "PostToolUse", in.SessionID,
			truncate("Resumed after "+in.ToolName, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"tool": truncate(in.ToolName, hookevents.MaxDataValueBytes)})
	case "PreCompact":
		title := "Compacting context"
		if in.Reason != "" {
			title = truncate("Compacting context ("+in.Reason+")", hookevents.MaxTitleBytes)
		}
		return spoolEvent(env, nowMs, "PreCompact", in.SessionID, title, hookevents.SeverityInfo,
			map[string]string{"reason": truncate(in.Reason, hookevents.MaxDataValueBytes)})
	case "PostCompact":
		return spoolEvent(env, nowMs, "PostCompact", in.SessionID, "Compaction complete", hookevents.SeverityInfo, nil)
	case "SubagentStart":
		return spoolEvent(env, nowMs, "SubagentStart", in.SessionID,
			truncate("Spawned: "+in.AgentType, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"agent_type": truncate(in.AgentType, hookevents.MaxDataValueBytes)})
	case "SubagentStop":
		return spoolEvent(env, nowMs, "SubagentStop", in.SessionID,
			truncate(in.AgentType+" done", hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"agent_type": truncate(in.AgentType, hookevents.MaxDataValueBytes)})
	case "TaskCreated":
		return spoolEvent(env, nowMs, "TaskCreated", in.SessionID,
			truncate("Task: "+in.Content, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"content": truncate(in.Content, hookevents.MaxDataValueBytes)})
	case "TaskCompleted":
		return spoolEvent(env, nowMs, "TaskCompleted", in.SessionID,
			truncate("✓ "+in.Content, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"content": truncate(in.Content, hookevents.MaxDataValueBytes)})
	default:
		// Forward-compat: Claude may add events at any time. Drop with a
		// breadcrumb rather than erroring.
		hookLog(env.QuilDir, env.PaneID, "unhandled hook_event: "+in.HookEventName)
		return nil
	}
}

// isPromptTool reports whether tool is an interactive-prompt tool whose
// completion (PostToolUse) should re-arm the work spinner. Keep this set in
// sync with claudehook.promptToolMatcher (the registration-side regex).
func isPromptTool(tool string) bool {
	switch tool {
	case "AskUserQuestion", "ExitPlanMode":
		return true
	default:
		return false
	}
}

// spoolEvent appends one hookevents.Payload JSONL line to the pane's spool
// file. Off-mode drops the event (session-id tracking still runs separately).
func spoolEvent(env HookEnv, nowMs int64, hookEvent, sessionID, title, sev string, data map[string]string) error {
	if env.Mode == "off" {
		return nil
	}
	eventsDir := filepath.Join(env.QuilDir, "events")
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		hookLog(env.QuilDir, env.PaneID, "mkdir events dir failed: "+err.Error())
		return err
	}
	p := hookevents.Payload{
		V:         hookevents.SchemaVersion,
		TsMs:      nowMs,
		Seq:       0,
		PaneID:    env.PaneID,
		Source:    hookevents.SourceClaude,
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
	spoolFile := filepath.Join(eventsDir, env.PaneID+".jsonl")
	f, err := os.OpenFile(spoolFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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

// writeSessionFile validates and atomically writes the rotating session id to
// $QuilDir/sessions/<paneID>.id, consumed by the daemon's restore path.
func writeSessionFile(env HookEnv, sessionID string) error {
	if sessionID == "" {
		hookLog(env.QuilDir, env.PaneID, "no session_id extracted from stdin")
		return nil
	}
	if !sessionIDRe.MatchString(sessionID) {
		hookLog(env.QuilDir, env.PaneID, "session_id rejected as non-uuid: "+sessionID)
		return nil
	}
	sessionsDir := filepath.Join(env.QuilDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		hookLog(env.QuilDir, env.PaneID, "mkdir sessions dir failed: "+err.Error())
		return err
	}
	out := filepath.Join(sessionsDir, env.PaneID+".id")
	if err := atomicWrite(out, []byte(sessionID+"\n"), 0o600); err != nil {
		hookLog(env.QuilDir, env.PaneID, "write session file failed: "+err.Error())
		return err
	}
	return nil
}

// hookLog appends a best-effort breadcrumb to $QuilDir/claudehook/hook.log.
// Never returns an error — a failure to log must not surface to Claude.
func hookLog(quilDir, paneID, msg string) {
	logDir := filepath.Join(quilDir, "claudehook")
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

// truncate returns s unchanged if its UTF-8 byte length is within maxBytes;
// otherwise it cuts on a rune boundary so the result (with a trailing "…")
// stays within maxBytes and is always valid UTF-8.
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
