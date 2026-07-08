# Per-Pane Input History for AI Panes — Design Spec

**Date:** 2026-06-15
**Status:** Approved (design)
**Scope:** v1 = Claude Code panes only. OpenCode is an explicit follow-up.

## Problem

AI panes (Claude Code) produce large volumes of output. To recover the context
of a session, the user must scroll far up to find the prompt they originally
submitted. There is no fast way to review "what did I ask?" across a session.

We want to capture the history of **user inputs** (prompts submitted to the AI),
present them as a navigable list of short (max 3-line) previews, and let the user
open any entry in a read-only viewer to read the full text and copy it.

This capability is meaningful only for plugins that have a user-prompt signal
(AI panes). It must NOT apply to plugins like lazygit, stripe, k9s, lazysql, or
plain terminals.

## Non-Goals

- Reconstructing input from raw keystrokes (fragile for multiline TUIs).
- Shell command history for terminal panes (shell already has up-arrow / Ctrl+R).
- Grouping history by session id, search/filter, or cross-pane aggregation (v1).
- Editing or re-submitting a historical input (read-only only).
- OpenCode capture (follow-up — see "Follow-up: OpenCode").

## Key Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Data source | Hooks only (AI panes). v1: Claude `UserPromptSubmit`. |
| Full-text view | Reuse the existing full-screen read-only `TextEditor` overlay. |
| Persistence | Per-pane JSONL on disk, survives daemon restart. |
| List UI | New modal dialog + keybinding, scoped to the active pane. |
| Serving | Daemon-mediated over IPC (consistent with memory report). |

## Why hooks, not keystrokes

Raw user keystrokes reach the daemon as undelimited bytes (`MsgPaneInput`) with
no "submitted line" concept. Reconstructing a clean prompt from them is fragile
(editing keys, multiline shift+enter, bracketed paste, TUI redraws). The Claude
hook already receives the full prompt text (`claudehook/runhook.go`, `in.Prompt`)
on `UserPromptSubmit`; it currently discards all but a 60-char preview. The
authoritative, clean source already exists for exactly the panes we care about.

## Architecture

### 1. Capture (producer — claudehook)

In `internal/claudehook/runhook.go`, the `UserPromptSubmit` case additionally
appends the full prompt to the history store. The existing notification spool
(60-char preview) is unchanged.

- Gated by a new PTY env var **`QUIL_RECORD_HISTORY=1`**, set at spawn only for
  plugins that opt in (see §3). Independent of `QUIL_HOOK_MODE` — a user may run
  with notifications `off` but still want history.
- The full text is written through a per-entry cap of **64 KiB** (UTF-8-aware
  truncation with a trailing `…[truncated]` marker), so a pasted megabyte log
  cannot bloat the store.
- Empty / whitespace-only prompts are skipped.
- Writes via `os.OpenFile(..., O_CREATE|O_APPEND|O_WRONLY, 0o600)` — the same
  append discipline as the event spool. The hook process is short-lived, so it
  only appends; it never compacts.

### 2. Store

- Path: `$QUIL_HOME/history/<paneID>.jsonl`, append-only, one JSON object per line.
- Entry schema (versioned, forward-compatible):

  ```json
  {"v":1,"ts_ms":1750000000000,"seq":7,"session_id":"<uuid>","text":"..."}
  ```

  `seq` is a monotonic per-pane counter used as the stable id for the
  fetch-full-entry request. `session_id` is recorded (survives `/clear`,
  `/resume`, compaction rotation) but is **not** used for grouping in v1.
- **Exempt from the daemon's startup spool truncation.** The event spool
  (`events/<paneID>.jsonl`) is truncated on daemon start; the history dir is a
  separate tree and is left intact — surviving restart is the whole point.
- Bounded by daemon-side **compaction to the last 200 entries**, performed on
  pane destroy and on an oversize read (file exceeds entry cap). The hook
  subprocess cannot compact (it only appends), so compaction is daemon-owned.
- `DestroyPane` unlinks `history/<paneID>.jsonl` (mirrors the spool cleanup).
  Restart != destroy: a normal daemon restart leaves the file in place.

### 3. Plugin opt-in

New plugin field under `[command]`:

```toml
[command]
record_history = true
```

- Set in `internal/plugin/defaults/claude-code.toml`. (OpenCode's default sets it
  in the follow-up, once the producer exists.)
- Not set by lazygit / stripe / k9s / lazysql / terminal (and they have no hook
  producer regardless).
- Plumbed to the PTY env as `QUIL_RECORD_HISTORY=1` at spawn for opted-in plugins
  (alongside the existing `QUIL_PANE_ID` / `QUIL_HOOK_MODE` / `QUIL_HOOK_HOME`).
- Bump `schema_version` in the claude-code default and add a migration-dialog
  entry path (existing `EnsureDefaultPlugins` stale-detection handles this).

### 4. Serving — daemon-mediated IPC

New package `internal/panehistory/` (read + parse + preview + compact only; the
daemon never writes entries — the hook does):

- `Read(quilDir, paneID) ([]Entry, error)` — reads the JSONL, skips a trailing
  partial line (concurrent-append safety), returns entries oldest→newest.
- `Preview(text string, maxLines, maxWidth int) []string` — first N logical
  lines, each width-truncated with `…`.
- `Compact(quilDir, paneID, keepLast int) error` — rewrite keeping the last N
  entries (atomic temp+rename).

New IPC message pairs (`internal/ipc/protocol.go`), request-response correlated
via the existing `Message.ID` field:

- `MsgPaneHistoryReq{PaneID}` → `MsgPaneHistoryResp{Entries []HistoryEntryMeta}`
  where `HistoryEntryMeta = {Seq, TsMs, Preview []string}` — **previews only**,
  newest first.
- `MsgPaneHistoryEntryReq{PaneID, Seq}` → `MsgPaneHistoryEntryResp{Text string}`
  — the full text of one entry, fetched only when the user opens it.

Two-tier fetch avoids shipping up to 200×64 KiB over the socket for a list view.
Works for lazily-restored / pending panes — reading the file needs no spawn.

### 5. TUI

- New keybinding `command_history` in `[keybindings]`, default **`alt+shift+i`**
  ("Input history"; `Alt+H` and `Alt+Shift+H` are already taken by PTY
  fall-through and horizontal split respectively). Multi-binding capable via the
  existing `kbMatches` machinery. Exempt in notes mode via `notesKeyExempt`.
- Also surfaced in the F1 About menu (like Memory).
- New dialog screen `dialogCommandHistory` (`internal/tui/`):
  - On open, sends `MsgPaneHistoryReq` for the **active** pane.
  - Renders a list of 3-line previews, newest first, with a selection cursor;
    `↑/↓` navigate, `Enter` opens, `Esc` closes.
  - `Enter` sends `MsgPaneHistoryEntryReq{Seq}`; on response, opens the full text
    in the existing read-only `TextEditor` overlay via a new
    `openReadonlyText(label, content)` — a sibling of `openLogViewer(label, path)`
    that takes an in-memory string instead of reading a file. Scroll / select /
    copy already work there (`ReadOnly=true`, `HighlightPlain`).
  - Active pane whose plugin lacks `record_history` (or has no entries) → an
    empty state: "No input history for this pane type."

## Data Flow

```
user submits prompt in claude pane
  -> claude fires UserPromptSubmit hook
     -> quild claude-hook subprocess (RunHook)
        -> spoolEvent(...)              (unchanged: 60-char notification preview)
        -> appendHistory(...)           (NEW: full text -> history/<paneID>.jsonl)

user presses Alt+Shift+I
  -> TUI dialogCommandHistory opens
     -> MsgPaneHistoryReq{paneID}
        -> daemon panehistory.Read + Preview
        -> MsgPaneHistoryResp{previews, newest first}
  -> user selects an entry, presses Enter
     -> MsgPaneHistoryEntryReq{paneID, seq}
        -> daemon returns full text
     -> openReadonlyText(label, fullText)  (read-only TextEditor overlay)
```

## Edge Cases

| Case | Handling |
|---|---|
| Multiline prompt | Preview = first 3 logical lines, each width-truncated with `…`; full text preserved verbatim. |
| Very long single line | Preview truncated with `…`; full text preserved. |
| Empty / whitespace prompt | Skipped — not recorded. |
| Huge pasted input (logs/exceptions) | Per-entry 64 KiB cap (producer), `…[truncated]` marker; outer cap re-checked on daemon read. |
| Session-id rotation (`/clear`,`/resume`,compaction) | History keyed by pane id, survives. `/clear` does NOT clear history. |
| Pane destroy | History file unlinked (explicit removal). |
| Daemon restart | History file retained (NOT truncated like the event spool). |
| Lazily-restored / pending pane | History readable from disk without spawning the pane. |
| Concurrent append (hook) + read (daemon) | Reader skips a trailing partial line. |
| Unicode | UTF-8-aware truncation (reuse existing truncate helpers). |
| Notifications off, history on | Separate `QUIL_RECORD_HISTORY` gate, independent of `QUIL_HOOK_MODE`. |
| Ring overflow (>200 entries) | Daemon compaction keeps last 200. |

## Follow-up: OpenCode

OpenCode's `chat.message` handler in
`internal/opencodehook/scripts/quil-session-tracker.js:340` currently spools a
static `"Working…"` and ignores the message text (`_input, _output` unused). To
extend history to OpenCode:

1. Determine the `chat.message` event input shape (the field carrying the
   submitted user message text) against the opencode plugin API.
2. Append the extracted text to `history/<paneID>.jsonl` using the same schema
   and the same `QUIL_RECORD_HISTORY` gate.
3. Set `record_history = true` in `internal/plugin/defaults/opencode.toml`
   (+ schema_version bump).

The store, IPC, daemon serving, and entire TUI are plugin-agnostic, so the
follow-up is producer-only.

## Testing / Verifiable Success

- **Unit (`internal/panehistory`)**: parse well-formed JSONL; skip trailing
  partial line; preview building (multiline, long-line, unicode, 3-line cap);
  compaction keeps last N atomically.
- **Unit (`internal/claudehook`)**: `UserPromptSubmit` appends a history entry
  when `QUIL_RECORD_HISTORY=1`; skips when unset; skips empty prompts; enforces
  64 KiB cap with marker. (Reuse the injectable env/dir pattern already used by
  the hook tests — no real `~/.claude` or `$QUIL_HOME` touched.)
- **Unit (`internal/ipc`)**: round-trip the new message payloads.
- **Manual (dev mode)**: type 3 prompts into a Claude pane; `Alt+Shift+I` lists
  them newest-first with 3-line previews; `Enter` opens full text; copy works;
  history survives `quil daemon restart`; a lazygit pane shows the empty state.

## Files Touched (estimate)

- `internal/claudehook/runhook.go` — append-history producer + cap helper.
- `internal/panehistory/` (new) — read / preview / compact + tests.
- `internal/ipc/protocol.go` — new message types + payloads.
- `internal/daemon/` — handle the new requests; set `QUIL_RECORD_HISTORY` at
  spawn for opted-in plugins; unlink history on `DestroyPane`; compaction hook.
- `internal/plugin/plugin.go` + `defaults/claude-code.toml` — `record_history`
  field + schema_version bump.
- `internal/tui/` — `dialogCommandHistory`, `openReadonlyText`, keybinding,
  F1 About entry.
- `internal/config/` — `command_history` keybinding default.
- Docs: `docs/keybindings.md`, `docs/plugin-reference.md`, `.claude/CLAUDE.md`.
