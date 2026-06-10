# Notification sidebar tick chains stack without bound

| Field | Value |
|-------|-------|
| Criticality | High |
| Complexity | Trivial |
| Location | `internal/tui/model.go:810` (paneEventMsg), `:814-818` (sidebarTickMsg), `:1460/:1469` (Alt+N) |
| Found during | Whole-project TUI performance audit |
| Date | 2026-06-10 |

## Issue

Every `paneEventMsg` received while the sidebar is visible appends
`m.sidebarTick()`, starting a NEW self-perpetuating 10 s chain —
`sidebarTickMsg` unconditionally reschedules while `visible && Count() > 0`.
There is no running-guard (contrast `workTickRunning`, which is correct,
`model.go:804-807`). The Alt+N toggle handlers start additional chains.

50 events with the sidebar open → 50+ concurrent chains → ~5 extra
Update+full-View cycles per second, forever, until the sidebar stays hidden
long enough for every chain to observe `!visible`.

The same missing-guard pattern exists in miniature for `notesTick`
(`model.go:821-827`, `toggleNotesMode` at `:1074`): rapid notes-mode toggling
stacks 5 s chains, bounded by toggle count.

## Risks

CPU drain and battery cost that silently grows over a session; each stacked
chain multiplies the render cost of the sidebar (which builds ad-hoc lipgloss
styles per event per frame).

## Suggested Solutions

Mirror the `workTickRunning` pattern: `sidebarTickRunning` flag set when
scheduling, cleared when a tick fires and declines to reschedule. Same guard
for `notesTick`, or fold both into one shared housekeeping ticker.
