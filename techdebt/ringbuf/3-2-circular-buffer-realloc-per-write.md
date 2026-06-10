# RingBuffer reallocates + copies the full buffer on every write once full

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/ringbuf/ringbuf.go:23-46` (Write), hot caller `internal/daemon/daemon.go` flushPaneOutput |
| Found during | Whole-project performance audit |
| Date | 2026-06-10 |

## Issue

The "ring buffer" is not circular. At steady state (any long-lived pane)
`len(rb.data) == rb.cap` exactly, because compaction sizes the backing array
to `cap`. Every subsequent `Write` then does: `append` → guaranteed realloc
(allocates ~2× cap) + full copy, followed by a second `make([]byte, cap)` +
full-capacity copy to compact. That is ~2 allocations of 256 KB+ and ~768 KB
of memcpy **per coalesced flush**, per pane. A chatty AI pane flushing
50-200×/s drives tens of MB/s of allocation churn — likely the daemon's
dominant GC pressure under load.

## Risks

Daemon CPU and GC pauses scale with output volume; with several active AI
panes this is sustained background cost on every machine running quil.

## Suggested Solutions

Implement a true circular buffer: fixed backing array allocated once at
`NewRingBuffer`, head/tail indices, zero per-write allocation. `Bytes()`
keeps copy semantics with a two-segment copy. Add a `Tail(n int)` method
while in there (see `techdebt/daemon/3-2-snapshot-skips-unchanged-buffers.md`
and the excerpt-extraction call sites that copy 512 KB to read 4 KB).
