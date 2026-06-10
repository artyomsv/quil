# Snapshot rewrites every ghost buffer to disk even when unchanged

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/daemon.go:329-380` (snapshot), `internal/persist/ghostbuf.go:18-66` |
| Found during | Whole-project performance audit |
| Date | 2026-06-10 |

## Issue

Every snapshot (30 s timer + every debounced structural change) copies each
pane's full ring buffer via `OutputBuf.Bytes()` (14 × up to 512 KB ≈ 7 MB of
memcpy) and rewrites **all** buffer files (WriteFile + remove + rename per
pane) — including panes that produced zero output since the last snapshot.
The 14 create/remove/rename triples dominate the observed 9-15 ms snapshot
time on Windows. Steady-state disk writes ≈ 7 MB per 30 s ≈ 20 GB/day of
mostly identical bytes.

## Risks

SSD wear, AV-filter overhead per file operation on Windows, snapshot latency
growing linearly with pane count.

## Suggested Solutions

Add a monotonic write-generation counter to `RingBuffer` (incremented in
`Write`); `snapshot()` records the per-pane generation and skips
`Bytes()`+`SaveBuffer` when unchanged. Crash safety unaffected — the on-disk
file already matches.
