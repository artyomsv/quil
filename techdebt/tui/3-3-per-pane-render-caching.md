# No per-pane render caching — full active-tab rebuild on every Update

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/tui/model.go:1298-1370` (View), `internal/tui/layout.go:524-542`, `internal/tui/pane.go:203-253, 376-400, 633-676` |
| Found during | Whole-project TUI performance audit |
| Date | 2026-06-10 |

## Issue

Bubble Tea calls `View()` after every Update. View walks every leaf of the
active tab: `vt.Render()` (full-grid ANSI string), `insertCursor`
re-`strings.Split`s the whole frame and rebuilds the cursor line cell-by-cell,
then a fresh `lipgloss.NewStyle().Border(...).Render(...)` re-measures every
line, then JoinHorizontal/JoinVertical. This runs per `PaneOutputMsg`
(potentially hundreds/s per busy pane), per 100 ms spinner tick, and per 1 s
size-poll echo — even when the changed pane is on a background tab or only a
spinner glyph changed. `perf.go`'s `slowViewThreshold = 30ms` exists because
this is the observed hot path.

Related multipliers found in the same audit:

- `Leaves()` allocates a fresh slice per walk and is called ~30+ times per
  frame (`tabLabel` → `tabHasEagerPane`/`tabHasWorkingPane` = 2 walks × 13
  tabs per render; `anyPaneWorking` walks all tabs 10×/s) —
  `internal/tui/layout.go:44-52`.
- While scrolled or selecting, per-cell `SafeEmulator` access takes a mutex
  per cell — 10k+ locked calls/frame for a 200×50 pane
  (`pane.go:403-490`, `selection.go:111-150`).
- `listenForMessages` returns one IPC message per Update cycle, so a burst of
  8 KB frames becomes N separate Update+View cycles — no batching.
- `perfStats.recordMsg(fmt.Sprintf("%T", msg))` allocates per Update
  (`model.go:370-373`).

## Risks

Visible CPU usage during streaming output (AI panes are exactly this
workload); input latency under load; battery drain. Worst case is mouse-drag
selection over a pane receiving output.

## Suggested Solutions

1. `dirty bool` per PaneModel (set in AppendOutput/ResizeVT/scroll/selection/
   active/ghost changes); `pane.View()` returns a cached string when clean.
   Makes background-tab frames and idle polls near-free.
2. Cache `[]*PaneModel` per TabModel, invalidated on layout mutation; compute
   hasEager/hasWorking once per Update.
3. Batch-drain available IPC messages into one Update.
4. Type-switch in `recordMsg` for the ~15 known message types.
