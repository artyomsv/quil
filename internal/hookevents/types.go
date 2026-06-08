// Package hookevents defines the wire format and ingest pipeline for
// notifications sourced from Claude Code and OpenCode hooks.
//
// Wire path (v1):
//
//   hook fires (claude .sh / opencode .js)
//       │
//       ├─ writes one JSONL line to $QUIL_HOME/events/<paneID>.jsonl  (primary)
//       │  ─ append-only, single-write per line (atomic under PIPE_BUF)
//       │  ─ daemon polls every 200 ms via Spool.Tick
//       │
//       └─ (future) framed MsgHookEvent over the daemon's unix socket   (opt)
//          ─ OpenCode JS plugin can use this directly via node:net
//          ─ cmd/quil-hook helper binary deferred (Phase D measurement)
//
//   daemon ingest goroutine
//       │
//       ├─ Ingester.Submit(p Payload)
//       │      ├─ rate limit  (per-pane sliding window, 100 / 2s; storm → drop 10s)
//       │      ├─ coalesce    (per-(paneID, hook_event) 50ms debounce; last-wins)
//       │      └─ emit(p)     (callback to daemon → daemon.PaneEvent → existing
//       │                      eventQueue.Push + Broadcast machinery)
//       │
//       └─ Pane.LastHookEventAt + Pane.HookHealthy updated, so checkIdlePanes
//          can skip the legacy idle excerpt for panes whose hooks are healthy
//          (and re-enable it as fallback when hooks fail to load).
package hookevents

import "errors"

// SchemaVersion is the wire-protocol version of Payload. The hook side stamps
// every JSONL line with v: SchemaVersion; the daemon rejects payloads at
// other versions so a breaking schema change can be detected and surfaced as
// a user-visible diagnostic instead of silently misbehaving.
const SchemaVersion = 1

// Severity values used by the hook side. The daemon translates these to
// PaneEvent.Severity 1:1 and the TUI sidebar renders the colour from the
// existing severityNameStyle map.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Source values for Payload.Source. Hooks stamp their own source so the
// daemon can disambiguate when a single pane runs both (e.g. an opencode
// pane that internally invokes claude).
const (
	SourceClaude   = "claude"
	SourceOpenCode = "opencode"
)

// Hook-side wire-size caps. The hook is responsible for truncating before
// the line hits the spool / socket; the daemon enforces an outer cap as
// belt-and-suspenders during ingest. These are smaller than the PaneEvent
// caps in internal/daemon/event.go (4 KiB Message, 1 KiB per Data value)
// because the wire schema duplicates the title/severity/event-type fields
// outside of Data; we keep total below the 2 KiB ceiling to leave plenty
// of headroom for the IPC fan-out broadcast.
const (
	MaxTitleBytes     = 200
	MaxDataValueBytes = 128
	MaxTotalBytes     = 2 * 1024
)

// Sentinel errors returned by validators / parsers. Callers compare with
// errors.Is so the surface is stable across error-wrapping changes.
var (
	ErrMissingPaneID    = errors.New("hookevents: payload missing pane_id")
	ErrMissingTitle     = errors.New("hookevents: payload missing title")
	ErrUnknownSeverity  = errors.New("hookevents: unknown severity")
	ErrSchemaVersion    = errors.New("hookevents: unsupported schema version")
	ErrOversizePayload  = errors.New("hookevents: payload exceeds 2 KiB cap")
	ErrEmptyHookEvent   = errors.New("hookevents: payload missing hook_event")
	ErrUnknownSource    = errors.New("hookevents: unknown source")
)

// Payload is the wire schema for a single hook event. Hook scripts JSON-encode
// one Payload per spool line; the daemon decodes and validates each line
// before feeding it to the Ingester.
//
// Fields use snake_case JSON tags because the JS plugin (OpenCode) and shell
// `printf` (Claude) producers find that easier than camelCase. Optional
// fields use omitempty so a minimal Payload is short enough to keep small
// events well under the 2 KiB cap.
type Payload struct {
	// V is the schema version. Must equal SchemaVersion (1) currently.
	V int `json:"v"`

	// TsMs is the wall-clock timestamp at the hook's perspective (Unix ms).
	// The daemon translates to time.Time via UnixMilli at the IPC boundary.
	TsMs int64 `json:"ts_ms"`

	// Seq is a per-(PaneID, Source) monotonic counter set by the hook side.
	// Two events landing in the same millisecond need a tiebreaker so the
	// 50ms coalesce buffer can preserve order; Seq supplies it.
	Seq uint64 `json:"seq"`

	// PaneID is the Quil pane id (passed to the hook via QUIL_PANE_ID env).
	PaneID string `json:"pane_id"`

	// Source identifies which tool emitted the event: SourceClaude or
	// SourceOpenCode. Determines which set of HookEvent values are valid.
	Source string `json:"src"`

	// HookEvent is the raw event name from the upstream tool (e.g. claude's
	// "PermissionRequest", opencode's "permission.ask"). Kept as a string
	// rather than enum so a future event type from upstream doesn't require
	// a daemon update before the hook can forward it.
	HookEvent string `json:"hook_event"`

	// SessionID is the AI tool's current session id when known. Optional.
	SessionID string `json:"session_id,omitempty"`

	// TranscriptPath is Claude's transcript path when the upstream event
	// provides it. Optional, claude-only.
	TranscriptPath string `json:"transcript_path,omitempty"`

	// CWD is the pane's working directory at the time the hook fired.
	// Optional but typically set so a notification card can show context.
	CWD string `json:"cwd,omitempty"`

	// Title is the human-readable summary that lands as PaneEvent.Title.
	// Capped to MaxTitleBytes; hook side truncates with "…" suffix.
	Title string `json:"title"`

	// Severity is one of SeverityInfo, SeverityWarning, SeverityError.
	Severity string `json:"sev"`

	// Data carries event-specific structured metadata (tool name, exit
	// code, command preview, file paths, etc.). Each value is capped to
	// MaxDataValueBytes; the daemon's PaneEvent ingest applies a second
	// per-event 1 KiB Data cap (see internal/daemon/event.go) as a backstop.
	Data map[string]string `json:"data,omitempty"`
}

// Validate checks the schema invariants enforced before ingest. The hook
// scripts are responsible for producing well-formed payloads; this is the
// daemon's safety net.
func (p Payload) Validate() error {
	if p.V != SchemaVersion {
		return ErrSchemaVersion
	}
	if p.PaneID == "" {
		return ErrMissingPaneID
	}
	if p.HookEvent == "" {
		return ErrEmptyHookEvent
	}
	if p.Title == "" {
		return ErrMissingTitle
	}
	switch p.Severity {
	case SeverityInfo, SeverityWarning, SeverityError, "":
		// "" tolerated and treated as Info downstream; some hooks emit no
		// explicit severity for low-stakes events.
	default:
		return ErrUnknownSeverity
	}
	switch p.Source {
	case SourceClaude, SourceOpenCode:
	default:
		return ErrUnknownSource
	}
	return nil
}
