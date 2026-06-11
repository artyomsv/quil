# Tab + Pane "Unseen" Indicator — Design (rev 2)

**Date:** 2026-06-11
**Status:** Approved (rev 2 — per-pane state, green pane border added)
**Replaces:** 5-second green tab flash on work completion

## Problem

When an agent pane (claude-code, opencode) finishes a turn or parks for user
input, the containing tab's label flashes green for 5 seconds
(`tabFlashDuration`) and then reverts. If the user is away from the keyboard —
the common case for long agent turns — the flash expires unseen and the signal
is lost. And with multiple agent panes split inside one tab, the tab-level cue
cannot say *which* pane needs attention.

## Decision

Replace the timed flash with a persistent **unseen** mark on the *pane*:

- The pane's border turns green and stays green until the user **focuses that
  pane** (click, Alt+Arrow, or arriving on it as the tab's active pane).
- The tab label turns green when the tab is in the background and contains at
  least one unseen pane — purely **derived** from pane state, no separate tab
  flag.

User decisions captured during design:

1. **Focused pane of the active tab never gets a mark.** If work finishes in
   the pane being looked at, the completion is seen by definition. An
   *unfocused* pane on the active tab IS marked — the user may be typing in a
   sibling split and the green border is the live attention cue.
2. **Park-for-input events mark the pane the same as completed turns.** Any
   "agent needs your attention" edge (Claude `Notification`/`PermissionRequest`,
   opencode `permission.ask`) routes through the same `workStop` transition.
3. **Acknowledgement = focusing the pane** (rev 2, supersedes rev 1's
   tab-selection clear). Single source of truth: `PaneModel.unseen`. For
   single-pane tabs this degrades to exactly rev 1's behavior — switching to
   the tab focuses its only pane. For multi-pane tabs, switching to the tab
   shows which pane is green; the label may re-green if the user switches away
   without focusing it, because the mark is genuinely unacknowledged.

## State

`internal/tui/pane.go` — `PaneModel`:

- Add `unseen bool` — work finished/parked in this pane while it was not the
  focused pane of the active tab; cleared on focus

`internal/tui/tab.go` — `TabModel`:

- Remove `flashUntil time.Time` (and the now-unused `time` import)

Not persisted. Same as today's work state: TUI restart resets to idle/seen and
the next hook event corrects it.

## Transitions

`internal/tui/workstate.go` — `applyWorkTransition`:

| Transition | Effect on `pane.unseen` |
|---|---|
| `workStop` (pane `wasWorking`, pane is NOT the active pane of the active tab) | set `true` |
| `workStop` on the focused pane of the active tab | unchanged (never set) |
| `workStop` on an already-idle pane | unchanged (no-op, as today) |
| `workStart` | set `false` — spinner supersedes green; "finished" is stale once a new turn begins |
| `workAbort` (`process_exit`) | unchanged (a crash is not a completed turn; an existing mark survives, as today) |

`applyWorkTransition` no longer returns a `tea.Cmd` — the only command it ever
returned was the flash-expiry tick. The caller in `model.go` (`paneEventMsg`
handler) drops the cmd append.

## Clearing on focus — single choke point

`ActivePane` is assigned at 13 call sites across `model.go`/`tab.go`; chasing
each is fragile. Instead, acknowledge at the **top of `Model.Update`**:

```go
// ackFocusedPane clears the unseen mark on the focused pane of the active
// tab, called once at the top of Update. Focusing the pane is itself the
// acknowledgement. Unfocused panes keep their mark until focused.
func (m *Model) ackFocusedPane() { ... }
```

Why this is correct: it does not depend on a render having happened between
messages (the Bubble Tea renderer coalesces frames, so multiple Updates can
run between renders). The invariant that actually holds is that a focused
pane never renders the mark — `tabUnseen` excludes the active tab and the
pane border gives the active style precedence — and focusing the pane is
itself the acknowledgement, so clearing without a guaranteed intervening
render loses nothing. Newly-focused panes are cleared one message later (the
1 s size poll guarantees an upper bound).

This runs on every message; cost is one slice scan of the active tab's cached
`Leaves()` — negligible next to the per-message work already done.

## Rendering

**Pane border** (`pane.go:View`, inline `borderColor` chain): insert the
unseen check between the base color and the `Active` check, so an
active/focused pane always shows the normal active border (green never
renders on the pane being looked at):

```go
borderColor := lipgloss.Color("238")
if p.unseen {
    borderColor = lipgloss.Color("28") // green — finished/parked, awaiting focus
}
if p.Active { ... 57 ... }
if p.ghost || ... { ... 95 ... }
if p.mcpHighlight { ... 208 ... }
```

Ghost/resuming and MCP-highlight keep precedence over unseen (a ghost pane
cannot be working, and an MCP highlight is an explicit "look here" command).

**Render cache** (`pane.go:renderKey`): add `unseen` to `paneRenderKey` —
without it a border change would not invalidate the cached view.

**Tab label** (`workstate.go`): `tabFlashing(idx)` becomes derived
`tabUnseen(idx)` — true iff `idx != m.activeTab` and any leaf pane of the tab
has `unseen` set. `model.go:tabStyle` precedence unchanged in shape:
unseen > custom tab color > active/inactive. `styles.go`: rename
`flashTabStyle` → `unseenTabStyle`, same green (256-color background 28).

## Deletions

- `tabFlashDuration` const (`workstate.go`)
- `flashTickMsg` type + its `msgName` case + its `Update` handler (`model.go`)
- The `tea.Tick(tabFlashDuration, …)` in `applyWorkTransition`
- `TabModel.flashUntil` field

## Docs

Update the work-in-progress indicators bullet in `.claude/CLAUDE.md` (it
documents the 5 s flash).

## Testing

Rewrite the flash tests in `internal/tui/workstate_test.go` to unseen
semantics:

- stop on a background-tab pane → pane unseen, derived `tabUnseen` true
- stop on the focused pane of the active tab → no mark
- stop on an unfocused pane of the active tab → pane unseen (border cue), but
  `tabUnseen(activeTab)` false (label suppressed while tab is active)
- park events (Notification, PermissionRequest, permission.ask) → pane unseen
- start clears a set mark
- abort (`process_exit`) does not set, does not clear
- stop without prior start (idle pane) does not set
- `ackFocusedPane` clears only the focused pane of the active tab; integration
  via `m.Update(...)` confirms the entry-point wiring
- pane border: unseen+unfocused renders green (38;5;28); focused renders
  active border; `renderKey` invalidates on unseen change
- style precedence: unseen background tab renders green label; active tab
  never does

Success check: `./scripts/dev.sh test` green; manual check in dev mode
(`./quil-dev.exe`) — finish a claude turn on a background tab: label + pane
border stay green indefinitely; switching to the tab clears the label-green
only when the green pane is (or becomes) the focused pane; clicking the green
pane clears its border.
