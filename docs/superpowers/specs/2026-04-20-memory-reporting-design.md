# Memory Reporting — Design Spec

- **Date:** 2026-04-20
- **Status:** Draft, pending user approval
- **Owner:** Artjoms Stukans

## Problem

Quil currently exposes no visibility into how much memory a session consumes.
A long-running daemon with many panes retains per-pane ring buffers, ghost
snapshots, plugin state, and PTY child processes; the user has no way to see
which panes or tabs are expensive, and an MCP-connected agent cannot reason
about whether it is accumulating scrollback that should be trimmed.

## Goals

1. **Visible, auto-scaled total** in the status bar so the user sees session
   cost at a glance.
2. **Per-pane + per-tab + grand-total breakdown** in an F1 dialog, with
   expand/collapse tree navigation matching the tab/pane mental model.
3. **Machine-readable reports** via two new MCP tools so AI agents can audit
   daemon-side memory cost without the TUI attached.
4. **Cross-platform** — Linux, macOS, Windows — for all three layers.

## Non-Goals

- No historical retention / time-series graphs. Single live snapshot only.
- No automatic eviction, trimming, or pressure alerting. Reporting only.
- No differentiation between shared vs. private memory; OS-reported resident
  memory is treated as opaque.
- No memory cost for dead panes (cleaned up on pane destroy).

## Concepts

Memory is accounted in three layers, with deliberate ownership boundaries:

| Layer | Owner | What it measures |
|---|---|---|
| **Go heap** | daemon | `OutputBuf` bytes + `GhostSnap` + `PluginState` map entries |
| **PTY RSS** | daemon | OS-reported resident memory of each pane's child process |
| **TUI** | TUI | VT emulator grid + open notes editor buffer, per pane |

The daemon owns and reports the first two layers. The TUI computes its own
third layer locally at render time. MCP tools return only the daemon-owned
layers because MCP can be invoked when the TUI is disconnected, and surfacing
a zero TUI value there would be misleading. The F1 dialog and status bar,
running inside the TUI, merge the daemon snapshot with local TUI values before
rendering so the user sees all three.

Snapshots are refreshed on a 5-second timer on the daemon side; the TUI issues
one IPC request per tick to fetch the latest snapshot, then recomputes its own
local values and renders.

## Architecture

```
┌──────────────── daemon ─────────────────┐     ┌──────────── TUI ────────────┐
│ internal/memreport/                      │     │ internal/tui/memory.go       │
│   collector.go  (5s ticker, snapshot)    │     │   memoryDialog (tree view)   │
│   procrss_*.go  (platform PTY RSS)       │     │   statusBarMem (total)       │
│                                          │     │   tuiLocalMem() (VT+notes)   │
│ session.Pane (unchanged)                 │     │                              │
│ daemon.go:                               │     │ on 5s tick:                  │
│   d.memReport *memreport.Collector       │     │   1. IPC MsgGetMemoryReport  │
│                                          │ ←── │   2. compute local TUI mem   │
│ IPC:                                     │     │   3. merge + render          │
│   MsgGetMemoryReport  (req/resp via ID)  │     │                              │
│                                          │     └──────────────────────────────┘
│ MCP (cmd/quil/mcp_tools.go):             │
│   get_memory_report  (summary totals)    │
│   get_pane_memory    (single pane)       │
└──────────────────────────────────────────┘
```

## Data Model

### Daemon-side (`internal/memreport`)

```go
type PaneMem struct {
    PaneID      string
    TabID       string
    GoHeapBytes uint64
    PTYRSSBytes uint64
    Total       uint64 // GoHeap + PTYRSS
}

type Snapshot struct {
    At    time.Time
    Panes []PaneMem // sorted by Total desc
    Total uint64    // sum of all panes' Total
}

type Collector struct {
    sm    *daemon.SessionManager
    last  atomic.Pointer[Snapshot]
    every time.Duration
}
```

The collector exposes two methods:

- `Run(ctx context.Context)` — starts the 5s ticker, runs until context
  cancellation. Calls `collect()` once at entry before sleeping so
  `Latest()` is never nil after `Run()` returns.
- `Latest() *Snapshot` — lock-free read via `atomic.Pointer.Load`.

### IPC

New message types in `internal/ipc/protocol.go`, following the existing
`Msg*Req` / `Msg*Resp` convention used for every MCP-style request-response
path:

```go
const (
    MsgMemoryReportReq  = "memory_report_req"
    MsgMemoryReportResp = "memory_report_resp"
)

type MemoryReportReqPayload struct{}

type PaneMemInfo struct {
    PaneID      string `json:"pane_id"`
    TabID       string `json:"tab_id"`
    GoHeapBytes uint64 `json:"go_heap_bytes"`
    PTYRSSBytes uint64 `json:"pty_rss_bytes"`
    TotalBytes  uint64 `json:"total_bytes"`
}

type MemoryReportRespPayload struct {
    SnapshotAt int64         `json:"snapshot_at"`  // Unix nanos
    Panes      []PaneMemInfo `json:"panes"`
    Total      uint64        `json:"total"`
}
```

To avoid a package cycle (`ipc` must not import `memreport`), the wire payload
uses a flat `PaneMemInfo` slice rather than directly embedding
`memreport.Snapshot`. The daemon constructs the response by copying from
`memreport.Snapshot` at write time; the TUI reconstructs a snapshot struct on
read.

Uses the existing `Message.ID` correlation pattern (same as every other
request-response IPC path). The daemon responds only to the requesting
connection, never broadcasts.

### TUI-side

```go
// internal/tui/memory.go
type memoryTreeNode struct {
    kind     nodeKind // tabNode | paneNode
    id       string
    label    string
    total    uint64
    children []*memoryTreeNode
    expanded bool
}

type memoryDialog struct {
    snapAt  time.Time
    rows    []memoryRow // flattened visible tree
    cursor  int
    loading bool
}
```

### MCP output schemas

```go
type TabMemSummary struct {
    TabID      string `json:"tab_id"`
    TabName    string `json:"tab_name"`
    PaneCount  int    `json:"pane_count"`
    TotalBytes uint64 `json:"total_bytes"`
    TotalHuman string `json:"total_human"`
}

type MemoryReportOutput struct {
    SnapshotAt    string          `json:"snapshot_at"`
    TotalBytes    uint64          `json:"total_bytes"`
    TotalHuman    string          `json:"total_human"`
    GoHeapBytes   uint64          `json:"go_heap_bytes"`
    PTYRSSBytes   uint64          `json:"pty_rss_bytes"`
    Tabs          []TabMemSummary `json:"tabs"`
}

type GetPaneMemoryInput struct {
    PaneID string `json:"pane_id" jsonschema:"required"`
}

type PaneMemoryOutput struct {
    SnapshotAt  string `json:"snapshot_at"`
    PaneID      string `json:"pane_id"`
    TabID       string `json:"tab_id"`
    PaneName    string `json:"pane_name"`
    Type        string `json:"type"`
    GoHeapBytes uint64 `json:"go_heap_bytes"`
    PTYRSSBytes uint64 `json:"pty_rss_bytes"`
    TotalBytes  uint64 `json:"total_bytes"`
    TotalHuman  string `json:"total_human"`
    ChildPID    int    `json:"child_pid"`
}
```

## Components

### 1. `internal/memreport/collector.go`

Responsibilities:

- Iterate `SessionManager.Panes()` under `sm.mu.RLock()`, snapshot pane
  pointers into a local slice, release the read lock.
- For each pane, compute Go-heap bytes inline:
  `len(OutputBuf.Bytes()) + len(GhostSnap) + sum(plugin state)`.
- Query the platform-specific `procRSS(pid int)` for each pane whose PTY is
  alive (i.e., `ExitCode == nil`); use 0 for exited panes.
- Sort by `Total` desc, aggregate grand total, store via
  `atomic.Pointer.Store`.
- Guard re-entry with `atomic.Bool` — skip a tick if the previous
  `collect()` is still running.

### 2. `internal/memreport/procrss_*.go`

Per platform, a single function:

```go
// procRSS returns resident memory in bytes for the given PID.
// Returns 0 and logs WARN on any error.
func procRSS(pid int) uint64
```

Implementations:

- **Linux** (`procrss_linux.go`, `//go:build linux`):
  Read `/proc/<pid>/status`, scan for `VmRSS:` line, parse the kilobyte value,
  multiply by 1024.
- **Darwin** (`procrss_darwin.go`, `//go:build darwin`):
  Shell out to `ps -o pid=,rss= -p <pid1>,<pid2>,...` in a single batched
  call per collection, wrapped in `exec.CommandContext(ctx, ...)` with a
  2-second timeout so a hung `ps` cannot stall the collector. Parse stdout
  as `pid rss` lines; `rss` is kilobytes. Build a map keyed by PID. No cgo.
- **Windows** (`procrss_windows.go`, `//go:build windows`):
  Use `golang.org/x/sys/windows` to call `OpenProcess` +
  `GetProcessMemoryInfo`; return `WorkingSetSize`. Close handle on every call.

Because Darwin batches across PIDs, the `Collector` passes the full PID list to
a platform entry point `procRSSBatch(pids []int) map[int]uint64` rather than
calling `procRSS(pid)` per pane. Linux and Windows implement `procRSSBatch`
as a simple loop calling their per-PID implementation.

### 3. PTY PID exposure

`pty.Session.Pid() int` already exists on the interface and both platform
implementations. No additional work here; the collector calls `p.PTY.Pid()`
where `p` is a `*daemon.Pane`.

### 4. Daemon wiring (`internal/daemon/daemon.go`)

- Add `memReport *memreport.Collector` field to `Daemon`.
- Construct in `NewDaemon(...)` with `every = 5 * time.Second`.
- Start in `Start()` as a goroutine bound to the daemon's shutdown context.
- Add `handleGetMemoryReport(msg ipc.Message, conn *ipc.Conn)` — reads
  `d.memReport.Latest()`, marshals, writes response to `conn` keyed by
  `msg.ID`.
- Register the handler in the IPC dispatch switch.

### 5. TUI dialog (`internal/tui/memory.go`, `internal/tui/dialog.go`)

- Add `dialogMemory` to the dialog iota.
- Insert `"Memory"` into the F1 About items slice between `"Plugins"` and
  `"View client log"`.
- Open handler issues `MsgGetMemoryReport` via existing IPC client with a new
  `Message.ID`; reply unblocks via `memoryReportMsg` Bubble Tea message.
- Tree flattening: walk `Snapshot.Panes`, group by `TabID`, preserve tab order
  from `SessionManager.TabOrder()`. Only expanded tabs contribute pane rows.
- Keys: `↑/↓` move cursor, `Enter`/`Space`/`→` expand tab node, `←` collapse,
  `R` force refresh (re-issue IPC immediately), `Esc` close.
- Row rendering uses `humanBytes` for all byte columns. Tab rows show total;
  pane rows show Go-heap, PTY RSS, TUI, Total. TUI column is filled from
  `tuiLocalMem(paneID)` at render time.

### 6. Status bar (`internal/tui/model.go`)

- Add `memoryTickMsg` Bubble Tea message, scheduled every 5s via
  `tea.Tick`.
- On tick, issue `MsgGetMemoryReport` (or reuse the dialog's outstanding
  request if one is in flight). Store result in `Model.lastMemSnap *memreport.Snapshot`.
- Status bar renderer appends ` · mem <humanBytes>` between the `[dev]`
  indicator and the notification badge, where the number is
  `snapshot.Total + sum(tuiLocalMem for visible panes)`.

### 7. TUI local memory (`internal/tui/memory.go`)

```go
func (m *Model) tuiLocalMem(paneID string) uint64 {
    var n uint64
    if vt := m.ptyVT(paneID); vt != nil {
        n += uint64(vt.Cols() * vt.Rows() * vtCellBytes)
    }
    if ne := m.notesEditorFor(paneID); ne != nil {
        n += ne.ApproxBytes()
    }
    return n
}
```

`vtCellBytes = 8` is a documented approximation; the VT emulator's internal
grid uses rune + style per cell. The exact figure is not important for
ranking, which is the primary use of the dialog.

`TextEditor.ApproxBytes()` is a new method on the existing editor struct:
`sum(len(line)) + len(lines)*newline`. Called only when notes editor is
mounted for that pane.

### 8. MCP tools (`cmd/quil/mcp_tools.go`)

Two new handlers registered with the MCP SDK, following the existing
typed-handler pattern (see `GetPaneStatus` / `ReadPaneOutput`). Each
handler:

1. Opens the daemon IPC connection (already cached by the MCP bridge).
2. Sends `MsgGetMemoryReport` with a fresh `Message.ID`.
3. Waits for the matched reply via the existing request-response helper.
4. For `get_memory_report`, aggregates by tab and returns totals only.
5. For `get_pane_memory`, filters by `PaneID` and fails with
   `tool error: pane not found` when absent.

### 9. Bytes helper

`internal/memreport/human.go`:

```go
func HumanBytes(n uint64) string
```

Auto-scaled units: `B`, `KB`, `MB`, `GB`, one decimal place, e.g. `812 KB`,
`4.2 MB`, `1.4 GB`, `0 B`. Reused by status bar, dialog, and MCP output.

## Error Handling

| Case | Behaviour |
|---|---|
| Daemon not reachable from TUI | Existing IPC timeout error; dialog shows "snapshot unavailable", status bar suppresses the `mem` segment. |
| Snapshot not yet populated (<5s after daemon start) | `Run()` does one synchronous `collect()` before first sleep so `Latest()` is never nil. |
| Pane exited, child PID absent | `PTYRSSBytes: 0`; pane still reported until destroyed. |
| Darwin `ps` exits non-zero | Log WARN once per collection cycle, populate `PTYRSSBytes: 0` for all panes. |
| Windows `OpenProcess` fails (AccessDenied, PID race) | Log WARN per affected PID, `PTYRSSBytes: 0` for that pane. |
| PID reused after exit | Skipped by `ExitCode != nil` check before RSS lookup. |
| Collector tick starvation | `atomic.Bool` guard drops overlapping ticks. |
| TUI disconnected mid-poll | Daemon discards the response write; collector keeps running. Next attach gets a fresh snapshot. |

## Testing

### Unit

- `internal/memreport/collector_test.go`
  - `TestCollect_GoHeapOnly` — known-size `OutputBuf` + `GhostSnap`; assert sum.
  - `TestCollect_TotalAggregation` — 3 panes / 2 tabs; assert per-tab + total.
  - `TestCollect_ExitedPane` — nil-PTY pane → `PTYRSSBytes == 0`, still listed.
  - `TestCollect_SnapshotAtomicity` — race-detector test with concurrent
    `Latest()` readers and `collect()` writers.
  - `TestHumanBytes` — table: `0`, `1023`, `1024`, `1.5*MB`, `4.2*GB`, etc.

- `internal/memreport/procrss_{linux,darwin,windows}_test.go` (platform-gated)
  - Spawn `os.Executable()` as a child with `sleep`, wait for ready, read its
    RSS, assert `> 0` and within a reasonable range (e.g., `< 200 MB`).

### Integration

- `internal/daemon/memory_ipc_test.go` — boot daemon, create 2 panes,
  send `MsgGetMemoryReport`, assert response shape + non-zero totals.

### TUI

- `internal/tui/memory_test.go` — tree flatten: 2 tabs × 3 panes, assert
  visible-row ordering; toggle expand/collapse; cursor bounds.
- Status-bar render test: known snapshot + known TUI-local bytes, assert
  substring `mem 4.2 MB`.

### MCP

- Existing MCP harness: two cases — `get_memory_report` returns schema-valid
  JSON; `get_pane_memory` on a known PID returns matching `child_pid`.

## File Changes Summary

**New files:**

- `internal/memreport/collector.go`
- `internal/memreport/collector_test.go`
- `internal/memreport/human.go`
- `internal/memreport/human_test.go`
- `internal/memreport/procrss_linux.go`
- `internal/memreport/procrss_darwin.go`
- `internal/memreport/procrss_windows.go`
- `internal/memreport/procrss_{linux,darwin,windows}_test.go`
- `internal/tui/memory.go`
- `internal/tui/memory_test.go`
- `internal/daemon/memory_ipc_test.go`

**Modified files:**

- `internal/ipc/protocol.go` — add `MsgMemoryReportReq` / `MsgMemoryReportResp` + payload types.
- `internal/daemon/daemon.go` — construct, start, dispatch handler.
- `internal/tui/dialog.go` — add `"Memory"` item + `dialogMemory` screen.
- `internal/tui/model.go` — `memoryTickMsg`, `lastMemSnap`, status-bar segment.
- `internal/tui/editor.go` — `TextEditor.ApproxBytes()` method.
- `cmd/quil/mcp_tools.go` — two new tool handlers.
- `.claude/CLAUDE.md` — add Memory dialog to F1 About description.

## Migration / Backward Compatibility

Purely additive. New IPC message type; older clients that never send it are
unaffected. New MCP tools; older clients that never call them are unaffected.
No workspace or config schema change. No `schema_version` bump in any plugin
TOML.

## Caveats

- PTY RSS is not directly comparable across OS kernels (Windows Working Set,
  Linux VmRSS, Darwin `resident_size`). Dialog footer notes this explicitly.
- Go-heap calculation is a lower bound — it covers the dominant allocations
  (ring buffers, ghost snapshots, plugin state) but not per-pane goroutine
  stacks or transient IPC payload buffers. Documented in collector comments.
- TUI local memory is an approximation (`cols*rows*8`) sufficient for
  ranking, not exact. Documented in the dialog footer.
