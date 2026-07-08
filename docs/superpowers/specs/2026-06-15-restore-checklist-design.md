# Per-Pane Restore Checklist — Design

**Date:** 2026-06-15
**Status:** Proposed
**Area:** `internal/tui` (pane restore indicator) + `internal/daemon` (broadcast fields)
**Builds on:** `2026-06-14-pane-restore-indicator-design.md` (the centered restore indicator)

## Problem

The centered restore indicator currently shows a single line (`⠹ Rebuilding session`)
plus a dim context line. It tells the user *that* a pane is restoring but not *what
is happening* or *how far along* it is. The user wants the indicator to show the
restoration steps as a small live checklist, each step ticking to `✓` as it
completes — making the multi-second claude-code restore legible.

## Constraint: honest data only

Per the project's no-synthetic-data rule, every checklist line must reflect a
**real, observed** restore fact:

- Real ghost-buffer line count (claude-code has none → the line must say so, not
  invent a number).
- Real session id (from the daemon's tracked id), never a placeholder.
- Step states derived from actual signals (`Pending`, ghost line count, blank
  screen), never a scripted timeline.

## Goals

1. Replace the single restore line with a 4-row per-pane checklist in the centered
   indicator.
2. Each row reflects a genuine restore phase and flips to `✓` when truly complete.
3. Animated spinner marks the one in-progress row.
4. Degrade gracefully (fall back to today's compact single line) on panes too short
   or narrow for the checklist.

## Non-goals

- No workspace-level / sidebar restore card (this is per-pane, in the existing
  centered indicator).
- No timestamps per step (the website hero's `[12.1s]` style). Honest per-step
  elapsed timing is deferred — out of scope for v1.
- No new daemon event types; only two additive broadcast fields.

## Design

### The checklist (4 fixed rows)

Rendered centered via `lipgloss.Place`, e.g. an active-tab claude pane mid-boot:

```
  ✓ session loaded
  ─ no saved history
  ✓ resuming claude · 8f2e1c
  ⠹ waiting for first output
```

A terminal pane (has a ghost buffer), still spawning:

```
  ✓ session loaded
  ✓ history restored (412 ln)
  ⠹ restarting shell
  · waiting for first output
```

Row definitions and the real signal each derives from:

| # | Row text | Marker logic | Source signal |
|---|----------|--------------|---------------|
| 1 | `session loaded` | always `✓` (metadata present once the indicator renders) | pane exists in `workspace_state` |
| 2 | `history restored (N ln)` **or** `no saved history` | `✓` with count when `HistoryLines > 0`; dim `─ no saved history` when `0` | daemon-supplied `history_lines` |
| 3 | resume label (see below) | `⠹` (active) while `Pending`; `✓` once spawned (`!Pending`) | `Pending` flag + plugin `Type` + daemon-supplied `SessionID` |
| 4 | `waiting for first output` | `⠹` (active) once row 3 is done **and** the screen is still blank; the whole indicator disappears when content paints | existing `screenBlank()` |

**Active-row / spinner rule:** the animated spinner (`spinnerFrame`) sits on the
first not-done row. Completed rows show `✓`; the no-history row shows a neutral
`─`; not-yet-reached rows show a dim `·`. Concretely:

- Deferred pane (`Pending`): rows 1 `✓`, 2 set, row 3 `⠹` (spawning), row 4 `·`.
- Spawned pane, screen blank: rows 1–3 done, row 4 `⠹` (the long claude wait).

**Resume labels** (row 3), by plugin type:

| Type | Label |
|------|-------|
| `claude-code` | `resuming claude` + ` · <id8>` when `SessionID != ""` |
| `opencode` | `resuming opencode` + ` · <id8>` when `SessionID != ""` |
| `terminal` (or `""`) | `restarting shell` |
| `ssh` | `reconnecting ssh` |
| `stripe` | `restarting stripe` |
| other | `starting <type>` |

`<id8>` is the first 8 runes of the session id (a real, tracked id; never shown
if empty).

**Colors:** `✓` green (28), `⠹` brand flame (208, animated), `·` / `─` dim (244),
the `· <id8>` suffix dim (244). Reuses `restoreAccentStyle` / `restoreDimStyle`
plus one new green style.

### Data flow

Two additive, **broadcast-only** fields per pane (gated on `includeOverlays`,
exactly like `pending` — never persisted to disk):

- `session_id` — copied from the pane's `plugin_state["session_id"]` (already
  tracked by the claude/opencode hooks and refreshed into `plugin_state` on stop).
- `history_lines` — line count of the pane's ghost buffer (`GhostSnap`/`OutputBuf`),
  the count that is/was replayed. `0` for ghost-disabled plugins (claude-code).

TUI side:

- `PaneInfo` (model.go) gains `SessionID string` and `HistoryLines int`; the
  `workspace_state` parser reads `pm["session_id"]` and `pm["history_lines"]`
  (JSON numbers decode to `float64` — convert).
- `PaneModel` gains `SessionID string` and `HistoryLines int`.
- `syncPaneMeta` copies both from `PaneInfo`.
- `paneRenderKey` gains `sessionID string`, `historyLines int`, and `paneType
  string` (the indicator now reads `Type`); `renderKey()` sets them.

### Rendering

`renderRestoreIndicator(innerW, innerH)` builds the 4 rows from
`(p.Type, p.SessionID, p.HistoryLines, p.Pending, p.screenBlank(), p.spinnerFrame)`:

1. Compute each row's marker (`done` / `active` / `pending` / `none`) per the table.
2. The active row is the first non-done row; its marker is the animated spinner.
3. Style each row, `lipgloss.JoinVertical(Center, rows...)`, then
   `lipgloss.Place(innerW, innerH, Center, Center, block)`.

**Fallback:** when `innerH < 6` or the widest row + 2 > `innerW`, render today's
compact single-line indicator (`⠹ <Rebuilding session|Building new pane>` + dim
context) instead. Keeps small splits readable.

Helper decomposition (one responsibility each, all unit-testable):

- `restoreSteps(p) []restoreStep` — pure: builds the ordered `{text, state}` rows
  from the pane's fields. `state ∈ {stepDone, stepActive, stepPending, stepNone}`.
- `resumeLabel(type, sessionID) string` — pure: row-3 text.
- `renderRestoreIndicator` — styles `restoreSteps` output + places it, or falls
  back.

### Render cache

`showRestoreIndicator()` is unchanged (gate stays `(resuming || preparing ||
Pending) && scrollBack==0 && screenBlank()`). The new visual inputs
(`SessionID`, `HistoryLines`, `Type`) are added to `paneRenderKey` so a change
(e.g. session-id arriving, or `Pending→spawned`) invalidates the cached frame.
`Pending`, `spinnerFrame`, and `contentGen` (covers `screenBlank`) are already in
the key.

## Files touched

- `internal/daemon/daemon.go` — add `session_id` + `history_lines` to
  `workspaceStateFromSnapshot` (broadcast-only).
- `internal/tui/model.go` — `PaneInfo` fields + `workspace_state` parse.
- `internal/tui/workstate.go` — `syncPaneMeta` copies the two fields.
- `internal/tui/pane.go` — `PaneModel` fields; `restoreStep` type + `restoreSteps`
  + `resumeLabel` + rewritten `renderRestoreIndicator`; `paneRenderKey` additions.
- `internal/tui/restore_indicator_test.go` — step-state-machine + render + fallback
  tests; cache-key cases.
- `internal/daemon/*_test.go` — broadcast carries the two fields.

## Testing / success criteria

Unit tests (construct `PaneModel` directly):

1. **claude, deferred** (`Type=claude-code, SessionID=8f2e1c…, HistoryLines=0,
   Pending=true`): rows = `✓ session loaded`, `─ no saved history`,
   `⠹ resuming claude · 8f2e1c`, `· waiting for first output`. Spinner on row 3.
2. **claude, spawned, blank** (`Pending=false`, screen blank): row 3 `✓`, row 4
   `⠹`.
3. **terminal, history** (`Type=terminal, HistoryLines=412, Pending=false`): row 2
   `✓ history restored (412 ln)`, row 3 `✓ restarting shell`, row 4 `⠹`.
4. **resumeLabel** table: each type → expected label; empty `SessionID` omits the
   ` · id` suffix.
5. **fallback**: `innerH=4` (and narrow width) → compact single line
   (`Rebuilding session` / `Building new pane`).
6. **View integration**: a blank resuming claude pane's `View()` contains
   `resuming claude` and `waiting for first output`.
7. **cache key**: flipping `SessionID`, `HistoryLines`, or `Type` invalidates the
   render cache (extend `TestPaneView_EveryKeyFieldInvalidates`).
8. **daemon**: `workspaceStateFromSnapshot` broadcast map contains `session_id`
   and `history_lines` for a restored claude/terminal pane; absent from the disk
   snapshot path (`includeOverlays=false`).

Manual (dev mode): restore a workspace; the active claude pane shows the 4-row
checklist with the spinner parked on `waiting for first output`, the session-id
prefix on row 3, and `no saved history`; a terminal pane shows
`history restored (N ln)`; switching to a deferred tab shows its pane's spinner on
`resuming …` then `waiting for first output`.

## Open questions

None — wording (`history restored (N ln)`) and showing the session-id prefix were
confirmed during brainstorming.
