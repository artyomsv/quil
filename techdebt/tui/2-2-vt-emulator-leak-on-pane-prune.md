# VT emulator + drain goroutine leaked on every pane close

| Field | Value |
|-------|-------|
| Criticality | High |
| Complexity | Small |
| Location | `internal/tui/model.go:1932` (RemovePane path), `internal/tui/tab.go:324-345`, `internal/tui/pane.go:62-99` |
| Found during | Whole-project memory-leak audit |
| Date | 2026-06-10 |

## Issue

Every `PaneModel` owns a `vt.SafeEmulator` with a `drainVTResponses` goroutine
blocked on `em.Read()` — an internal `io.Pipe` that only unblocks on
`em.Close()`. The **only** `vt.Close()` call site in the TUI is `replaceVT`
(`pane.go:97`, used by `ResetVT`). When `applyWorkspaceState` prunes a pane via
`tab.RemovePane(id)`, or drops a whole tab absent from daemon state
(`m.tabs` rebuild), the `PaneModel` is unreferenced but its drain goroutine
still references the emulator: the goroutine parks forever and pins the
emulator, including its 10,000-line scrollback (`SetScrollbackSize(10000)`,
`pane.go:64`) and the raw ring buffer (~256 KB).

## Risks

Long-running TUI sessions leak one goroutine plus potentially several MB per
pane ever closed (Ctrl+W, Alt+W tab close, pane replace, daemon-side destroy).
Heavy multiplexer users close panes constantly — this compounds into tens of
goroutines and hundreds of MB over a day.

## Suggested Solutions

Add `PaneModel.Dispose()` calling `p.vt.Close()`. Invoke it from
`applyWorkspaceState` for every pruned pane and for all panes of dropped tabs
(diff the pre-reconciliation pane set against the surviving tree), and from any
direct local remove path. Goroutine-count regression test via
`runtime.NumGoroutine` delta (pattern exists in
`internal/ipc/conn_internal_test.go`).
