# `paneSourceAdapter` takes `PluginMu` multiple times per collection

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/session.go` — `paneSourceAdapter` methods (`Alive`, `GhostSnap`, `PID`, `PluginState`) |
| Found during | Final code review of memory reporting feature (commits 1dd5d6a..HEAD) |
| Date | 2026-04-20 |

## Issue

`collectFrom` in `internal/memreport/collector.go` calls each adapter
method at least once per pane per 5-second tick, and `Alive()` + `PID()`
twice (once during the alive-PID-gathering first pass, once during the
main accumulation pass). Each method acquires `PluginMu`. With N panes,
that is up to 6N lock acquisitions per tick for data that is logically
a single consistent snapshot.

## Risks

- **Lock contention** at high pane counts (50+) is minor but measurable.
- **Inconsistent reads** across six sequential acquisitions: a pane that
  exits mid-collection can appear "alive with PID 0" or "dead with a
  non-zero RSS lookup" in rare interleavings. Current collector handles
  the torn state by treating missing RSS-map entries as zero, but the
  snapshot's layer-level consistency (GoHeap + PTYRSS belong to the same
  Pane state) is not guaranteed.

## Suggested Solutions

**Option A** (preferred): replace the seven-method `PaneSource` interface
with a single `Snapshot() PaneSourceSnapshot` method. The adapter takes
`PluginMu` once, copies all needed fields into a plain struct, and
returns it. `collectFrom` becomes a pure loop over the snapshot slice.

Signature sketch:
```go
type PaneSourceSnapshot struct {
    PaneID    string
    TabID     string
    Alive     bool
    PID       int
    HeapBytes uint64  // pre-computed: len(OutputBuf) + len(GhostSnap) + plugin state
}
type PaneSource interface {
    Snapshot() PaneSourceSnapshot
}
```

This also lets the adapter pre-compute `HeapBytes` under the lock, which
avoids the current collector's post-lock iteration over the plugin-state
map.

**Option B**: keep the seven-method interface but add a sibling
`SnapshotAll(panes []*Pane) []PaneSourceSnapshot` on `SessionManager`
that takes the session lock once, iterates all panes, takes each
`PluginMu` once, and returns the pre-computed slice. Less invasive
interface-wise, similar perf gain.

Both options eliminate the dead `paneSourceAdapter.PluginState()`
map-copy allocation on panes that don't have plugin state, which is most
of them.
