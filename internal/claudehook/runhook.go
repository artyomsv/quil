package claudehook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Prompt         string `json:"prompt"`
	Message        string `json:"message"`
	ToolName       string `json:"tool_name"`
	Reason         string `json:"reason"`
	AgentType      string `json:"agent_type"`
	// AgentID is present only when the hook fires INSIDE a subagent, which
	// makes its absence the test for "this came from the main agent".
	// agent_type cannot serve: a session started with --agent carries one on
	// every event, main-agent events included.
	AgentID string `json:"agent_id"`
	Content string `json:"content"`
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
	case "Notification":
		// Claude fires this for a permission prompt AND for its own idle
		// nudge; only the message text tells them apart, and this is the only
		// place that text is in hand. notifyKindData marks the idle case so
		// the TUI can leave a still-working pane alone — see its comment for
		// why the mark is positive.
		return spoolEvent(env, nowMs, "Notification", in.SessionID,
			truncate(in.Message, hookevents.MaxTitleBytes), hookevents.SeverityWarning,
			notifyKindData(in.Message))
	case "PermissionRequest":
		return spoolEvent(env, nowMs, "PermissionRequest", in.SessionID,
			truncate("Needs approval: "+in.ToolName, hookevents.MaxTitleBytes), hookevents.SeverityWarning,
			map[string]string{"tool": truncate(in.ToolName, hookevents.MaxDataValueBytes)})
	case "Stop":
		// Turn boundary: cheap, low-frequency, and already carrying the live
		// transcript path for modelUsageData — the natural place to notice that
		// the session has moved project directories since SessionStart.
		refreshTranscriptPath(env, in.SessionID, in.TranscriptPath)
		return spoolEvent(env, nowMs, "Stop", in.SessionID, "Reply ready", hookevents.SeverityWarning,
			modelUsageData(env, in.TranscriptPath))
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
	case "PreToolUse":
		// Work-spinner START edge for a turn no user prompt began. See the
		// PreToolUse case in hookevents.ClassifyWorkEvent for the trace.
		// Work-state only; the TUI suppresses it from the notification sidebar.
		//
		// Registered for EVERY tool, so this branch runs once per tool call —
		// the only hook Quil registers that does. It is throttled to a
		// heartbeat because the signal is a LEVEL ("this pane is working"),
		// not an edge: a pane Quil heard from moments ago needs no further
		// proof, and any later tool call in the same turn re-arms the
		// identical state. Dropping the line here also keeps it off the
		// ingester's rate limiter, which would otherwise spend a pane's whole
		// 100-events-per-2s budget on heartbeats and take a real permission
		// prompt down with it.
		// Hooks fire inside subagents too, and turnActive is a statement about
		// the MAIN turn alone. A background subagent outlives that turn's Stop
		// by design, so letting its tool calls through would reopen a turn that
		// has ended — and nothing would close it again, since the subagent's
		// own completion is a SubagentStop. The pane would hold a lit spinner
		// until SessionEnd. The subagent ledger already keeps such a pane
		// `working` via SubagentStart/Stop, so this costs no signal.
		if in.AgentID != "" {
			return nil
		}
		if spoolIsFresh(env, nowMs) {
			return nil
		}
		return spoolEvent(env, nowMs, "PreToolUse", in.SessionID, "Working", hookevents.SeverityInfo,
			map[string]string{"tool": truncate(in.ToolName, hookevents.MaxDataValueBytes)})
	case "PreCompact":
		title := "Compacting context"
		if in.Reason != "" {
			title = truncate("Compacting context ("+in.Reason+")", hookevents.MaxTitleBytes)
		}
		return spoolEvent(env, nowMs, "PreCompact", in.SessionID, title, hookevents.SeverityInfo,
			map[string]string{"reason": truncate(in.Reason, hookevents.MaxDataValueBytes)})
	case "PostCompact":
		// Do NOT read model/context usage here. Right after compaction the
		// reduced context size is not yet in the transcript: the compaction
		// summary is written as system/user entries with no assistant usage, so
		// readTranscriptUsage would return the PRE-compaction turn's (now-stale)
		// count. Emit a compacting-reset signal instead — the next completed
		// turn's Stop reports the true reduced size via modelUsageData.
		return spoolEvent(env, nowMs, "PostCompact", in.SessionID, "Compaction complete", hookevents.SeverityInfo,
			map[string]string{"compacting": "1"})
	case "SubagentStart":
		return spoolEvent(env, nowMs, "SubagentStart", in.SessionID,
			truncate("Spawned: "+in.AgentType, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"agent_type": truncate(in.AgentType, hookevents.MaxDataValueBytes)})
	case "SubagentStop":
		if in.AgentType == "" {
			// Claude Code fires one SubagentStop with an EMPTY agent_type at
			// the end of EVERY main turn (measured 1:1 against Stop across
			// every AI pane). It is the root turn's own completion — its start
			// edge is UserPromptSubmit, not a SubagentStart — so it names no
			// background agent and reports nothing a user can act on. Spooled,
			// it became a sidebar card titled literally " done" once per turn,
			// aggregated to `" done" ×N` and re-promoted to the top each time.
			// The TUI work ledger already discards it (a stop matches only a
			// start it can name); dropping it here removes the noise at the
			// source and leaves that guard as defence in depth.
			return nil
		}
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

// transcriptRetryDelays paces the re-reads in modelUsageData. Claude appends
// the final assistant transcript line asynchronously around the moment Stop
// hooks fire — a live trace showed the line landing 30 ms AFTER the hook's
// first read — so a failed read is retried briefly before giving up. Package
// var so tests can shrink the waits.
var transcriptRetryDelays = []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond}

// modelUsageData tail-reads the session transcript and returns the model +
// context-token Data keys for a spool event, or nil when the transcript is
// missing or unreadable (the event is then emitted exactly as before this
// feature — no new failure mode). A failed read leaves one breadcrumb in the
// hook log; a silent failure here cost a live-debugging round when the
// transcript-flush race produced data-less Stop events without a trace.
func modelUsageData(env HookEnv, transcriptPath string) map[string]string {
	if transcriptPath == "" {
		return nil
	}
	var (
		model  string
		tokens int64
		ok     bool
	)
	for _, delay := range transcriptRetryDelays {
		time.Sleep(delay)
		if model, tokens, ok = readTranscriptUsage(transcriptPath); ok {
			break
		}
	}
	if !ok {
		hookLog(env.QuilDir, env.PaneID, "transcript usage read failed after retries: "+truncate(transcriptPath, 200))
		return nil
	}
	return map[string]string{
		"model":          truncate(model, hookevents.MaxDataValueBytes),
		"context_tokens": strconv.FormatInt(tokens, 10),
	}
}

// idleNudgePhrase identifies Claude's idle notification, observed verbatim as
// "Claude is waiting for your input". Matched as a substring, case-folded,
// because the sentence around it is not ours to depend on.
const idleNudgePhrase = "waiting for your input"

// notifyKindData marks a Notification the TUI may safely ignore on a pane
// whose turn is already over: Claude's idle nudge, which fires once the user
// has been idle at the prompt and reports nothing the agent is waiting for.
// Every other message — a permission prompt, a rewording, anything new
// upstream adds — returns nil and is parked by the consumer.
//
// Only the idle case is matched, and that direction is the whole point.
// Recognising upstream English prose is fragile, so the unrecognised message
// must fall toward the visible amber tab the next Stop clears, never toward a
// permission prompt that never surfaces at all (a parked agent emits no
// further hook to recover it). The "permission" exclusion covers the one
// overlap that is not upstream's wording: the permission message embeds a TOOL
// NAME, and an MCP server may call its tool anything it likes.
//
// Runs in the per-event hook process, so it stays two substring scans.
func notifyKindData(message string) map[string]string {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, idleNudgePhrase) || strings.Contains(lower, "permission") {
		return nil
	}
	return map[string]string{hookevents.DataNotifyKind: hookevents.NotifyKindIdle}
}

// workHeartbeatInterval is how long a pane may stay silent before a tool call
// is worth spooling as proof of work. It bounds how late the indicator can
// appear on a turn Quil never saw start; every tool call in between costs a
// single stat and no line.
const workHeartbeatInterval = 15 * time.Second

// spoolIsFresh reports whether Quil has heard from this pane within
// workHeartbeatInterval of nowMs, judged by the spool file's mtime.
//
// The spool carries EVERY event for the pane, which is what makes it the right
// clock to read: the question is not "when did the last heartbeat fire" but
// "does Quil already know this pane is alive". A turn opened by a normal
// UserPromptSubmit therefore suppresses the heartbeats behind it for free.
//
// Both failure directions resolve toward speaking rather than staying silent.
// A missing or unreadable spool means Quil has heard NOTHING from this pane —
// the loudest possible reason to emit — and a future mtime (clock skew, a
// restored file) yields a negative age that must not read as "recently
// audible", or a skewed clock would mute the pane's indicator indefinitely. A
// surplus line costs one sidebar-suppressed event; a missing one costs the
// user the only cue that work is happening.
func spoolIsFresh(env HookEnv, nowMs int64) bool {
	fi, err := os.Stat(filepath.Join(env.QuilDir, "events", env.PaneID+".jsonl"))
	if err != nil {
		return false
	}
	age := time.UnixMilli(nowMs).Sub(fi.ModTime())
	return age >= 0 && age < workHeartbeatInterval
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

// writeSessionFile validates and atomically writes the rotating session id and
// its transcript path to $QuilDir/sessions/<paneID>.id, consumed by the
// daemon's restore path.
//
// transcriptPath may be empty (the record then carries the id alone, exactly
// as before it was recorded); it is written verbatim on its own line because
// only Claude knows which project directory the session actually lives in.
func writeSessionFile(env HookEnv, sessionID, transcriptPath string) error {
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
	body := sessionID + "\n"
	// A newline in the path would forge a record line; drop rather than
	// truncate, so a hostile value costs the path and never the id.
	if transcriptPath != "" && !strings.ContainsAny(transcriptPath, "\r\n") {
		body += transcriptPath + "\n"
	}
	out := filepath.Join(sessionsDir, env.PaneID+".id")
	if err := atomicWrite(out, []byte(body), 0o600); err != nil {
		hookLog(env.QuilDir, env.PaneID, "write session file failed: "+err.Error())
		return err
	}
	return nil
}

// refreshTranscriptPath keeps the recorded path correct for a session that
// moves project directories mid-flight — an agent cd-ing into a git worktree
// re-keys the transcript, leaving the path SessionStart recorded pointing at a
// file that is no longer there.
//
// It writes the SIDECAR only, never <paneID>.id. Hook invocations are
// independent processes with no locking, so a read-modify-write of the id file
// would let a Stop that read before a concurrent SessionStart write the
// PRE-rotation id back — resurrecting the session the user just left, which is
// the same wrong-conversation failure this path exists to prevent. Confining
// the write to the sidecar makes the id unreachable from here: the worst a lost
// race can do is leave the path stale, and a stale path never renames a session.
//
// The id is still recorded IN the sidecar so a reader can tell whether the path
// describes the session it is asking about. Best-effort throughout, and written
// only when the path actually changed, so the common Stop costs one read.
func refreshTranscriptPath(env HookEnv, sessionID, transcriptPath string) {
	if sessionID == "" || transcriptPath == "" {
		return
	}
	// Same guards writeSessionFile applies, for the same reasons: a newline
	// would forge a second record line, and a non-uuid id is not ours to record.
	if !sessionIDRe.MatchString(sessionID) || strings.ContainsAny(transcriptPath, "\r\n") {
		return
	}
	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil || rec.ID != sessionID || rec.TranscriptPath == transcriptPath {
		return
	}
	sessionsDir := filepath.Join(env.QuilDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		hookLog(env.QuilDir, env.PaneID, "mkdir sessions dir failed: "+err.Error())
		return
	}
	body := []byte(sessionID + "\n" + transcriptPath + "\n")
	if err := atomicWrite(transcriptFile(env.QuilDir, env.PaneID), body, 0o600); err != nil {
		hookLog(env.QuilDir, env.PaneID, "refresh transcript path failed: "+err.Error())
	}
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
