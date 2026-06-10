# PTY output coalescer accumulator is unbounded under sustained output

| Field | Value |
|-------|-------|
| Criticality | High |
| Complexity | Trivial |
| Location | `internal/daemon/daemon.go:1307-1316` (streamPTYOutput coalescing loop) |
| Found during | Whole-project memory-leak audit |
| Date | 2026-06-10 |

## Issue

Each PTY chunk does `acc = append(acc, chunk...)` then
`flushTimer.Reset(coalesceDelay)` — a **debounce, not a throttle**. While the
PTY delivers chunks faster than every 2 ms (`cat bigfile`, `yes`, a fast
build), the timer never fires and `acc` grows without any size cap. The
eventual single flush also produces one giant IPC frame (JSON/base64
amplified ×1.33) broadcast to every client. After flush, `acc = acc[:0]`
retains the peak backing array for the pane's whole process lifetime — one
such array per live pane.

## Risks

A pane that dumps tens of MB without 2 ms gaps balloons daemon memory by that
amount (plus the amplified wire frame per client queue), then pins the peak
capacity forever. Multiple such panes compound.

## Suggested Solutions

Flush when `len(acc)` exceeds a cap (64-128 KB) regardless of the timer.
Optionally reallocate `acc` down when capacity greatly exceeds typical size
after a burst.
