# Tab "Unseen" Indicator — Design

**Date:** 2026-06-11
**Status:** Approved
**Replaces:** 5-second green tab flash on work completion

## Problem

When an agent pane (claude-code, opencode) finishes a turn or parks for user
input, the containing tab's label flashes green for 5 seconds
(`tabFlashDuration`) and then reverts. If the user is away from the keyboard —
the common case for long agent turns — the flash expires unseen and the signal
is lost.

## Decision

Replace the timed flash with a persistent **unseen** mark: the tab label stays
green until the user selects the tab. Selecting the tab — by any means — is the
acknowledgement.

User decisions captured during design:

1. **Active tab gets no mark.** If work finishes on the tab currently being
   viewed, the completion is seen by definition. Green only ever appears on
   background tabs (matches current behavior — the active tab never flashed).
2. **Park-for-input events mark the tab the same as completed turns.** Any
   "agent needs your attention" edge (Claude `Notification`/`PermissionRequest`,
   opencode `permission.ask`) routes through the same `workStop` transition.
   One code path, one meaning: come look at this tab.

## State

`internal/tui/tab.go` — `TabModel`:

- Remove `flashUntil time.Time`
- Add `unseen bool` — true when a pane in this tab completed/parked while the
  tab was in the background, and the user has not selected the tab since

Not persisted. Same as today's work state: TUI restart resets to idle/seen and
the next hook event corrects it.

## Transitions

`internal/tui/workstate.go` — `applyWorkTransition`:

| Transition | Effect on `unseen` |
|---|---|
| `workStop` (pane `wasWorking` AND `tabIdx != m.activeTab`) | set `true` |
| `workStop` on the active tab | unchanged (never set) |
| `workStop` on an already-idle pane | unchanged (no-op, as today) |
| `workStart` in the tab | set `false` — spinner supersedes green; "finished" is stale once a new turn begins (same as today's flash-clear on start) |
| `workAbort` (`process_exit`) | unchanged (a crash is not a completed turn; an existing mark from an earlier completion survives, as today) |

`applyWorkTransition` no longer returns a `tea.Cmd` — the only command it ever
returned was the flash-expiry tick. Signature changes from
`func (m *Model) applyWorkTransition(paneID, eventType string) tea.Cmd` to
`func (m *Model) applyWorkTransition(paneID, eventType string)`. The caller in
`model.go` (`paneEventMsg` handler) drops the cmd append.

## Clearing on selection

New helper on `Model`:

```go
// setActiveTab makes the tab at idx active and marks it seen. Every code
// path that activates a tab must route through this so the green "unseen"
// indicator is acknowledged by selection, regardless of input method.
func (m *Model) setActiveTab(idx int) {
    if idx < 0 || idx >= len(m.tabs) {
        return
    }
    m.activeTab = idx
    m.tabs[idx].unseen = false
}
```

Replace direct `m.activeTab = …` assignments at all activation sites in
`model.go`:

| Site | Path |
|---|---|
| `switchTab` | keyboard Alt+1-9 / next-prev, mouse click on tab |
| `setActivePaneMsg` handler | MCP `set_active_pane` |
| `handleNotificationKey` "navigate" | sidebar event jump |
| `popPaneHistory` | Alt+Backspace history |
| `applyWorkspaceState` reconciliation (both branches: ID match and clamp fallback) | daemon-driven, incl. MCP `switch_tab` |

Tab drag reorder (`m.activeTab = to` in the reorder function) keeps a direct
assignment: it tracks the dragged tab's new ordinal — tab *identity* doesn't
change, nothing new is selected. (Routing it through the setter would also be
harmless — the active tab can never carry `unseen` — but the direct assignment
states the intent.)

## Rendering

`model.go:tabStyle` precedence is unchanged in shape:
unseen > custom tab color > active/inactive.

- `tabFlashing(idx)` → `tabUnseen(idx)`: returns
  `m.tabs[idx].unseen` guarded by bounds check. The `!active` guard at the
  call site stays (belt-and-suspenders; the invariant says active tabs are
  never unseen).
- `styles.go`: rename `flashTabStyle` → `unseenTabStyle`, same green
  (256-color background 28).

## Deletions

- `tabFlashDuration` const (`workstate.go`)
- `flashTickMsg` type + its `msgName` case + its `Update` handler (`model.go`)
- The `tea.Tick(tabFlashDuration, …)` in `applyWorkTransition`

## Testing

Rewrite the flash tests in `internal/tui/workstate_test.go` to unseen
semantics:

- stop on background tab with prior start → `unseen == true`
- stop on **active** tab with prior start → `unseen == false`
- park events (Notification, PermissionRequest, permission.ask) on background
  tab → `unseen == true`
- start clears a set mark
- abort (`process_exit`) does not set, does not clear
- stop without prior start (idle pane) does not set
- `setActiveTab` clears the mark; out-of-bounds idx is a no-op
- style precedence: unseen inactive tab renders green background; active tab
  never renders green (existing `TestTabStyle_FlashOverridesInactive`,
  renamed)

Success check: `./scripts/dev.sh test` green; manual check in dev mode
(`./quil-dev.exe`) — finish a claude turn on a background tab, label stays
green indefinitely, goes normal the moment the tab is clicked or switched to
via Alt+&lt;digit&gt;.
