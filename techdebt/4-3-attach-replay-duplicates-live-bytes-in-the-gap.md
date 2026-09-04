# Attach replay can duplicate live bytes emitted in the reset-to-replay gap

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Medium |
| Location | `internal/daemon/daemon.go` (`handleAttach` replay loop), `internal/tui/reconnect.go:709` (`resetForReattach`), `internal/ipc/server.go:697` (`wantsFrame`) |
| Found during | code review of PR #200 (attach `child-stream` replay) |
| Date | 2026-09-04 |

## Issue

A conn receives `pane_output` broadcasts from the moment it connects — `wantsFrame`
filters only the MCP bridge's opt-out, never "has this client attached yet". `handleAttach`
then sends the workspace state and replays each pane's buffer as ghost chunks, one pane at a
time. The TUI resets every pane's VT BEFORE it sends `attach` (`resetForReattach`), so any
live frame that lands between that reset and the pane's replay snapshot is applied to the
fresh grid and then applied AGAIN inside the replay, which copies the whole buffer.

The window is the gap between the client-side reset and the daemon-side `OutputBuf.Bytes()`
for that pane — milliseconds on a small workspace, longer for the last panes of a big one
because the loop replays sequentially with backpressure. It is a property of every replay
path that copies a live child's stream: the `outputbuf` reconnect path since `ghost_buffer`
was flipped on, and now the `child-stream` first-attach path added in PR #200 (which has the
same shape by design). The `ghostsnap` path is unaffected: its bytes are a previous session's,
and `ghostScrollOut` pushes them off the visible grid anyway.

A fresh TUI is narrower than a reconnecting one: it has no pane models until the workspace
state arrives, so frames before that are dropped; the gap is only state-frame → replay.

## Risks

Duplicated or interleaved rows at the join of a reattach, for a child that happened to be
writing at that instant. Cosmetic, self-healing on the next repaint, and rare — but it
reads as "the replay is corrupt" and sends someone to re-diagnose the 2026-08-03 join bug.

## Suggested Solutions

1. Per-conn sequencing barrier: `handleAttach` suppresses live `pane_output` to THIS conn
   (a per-conn flag beside `noPaneOutput`) from the workspace-state send until each pane's
   replay is enqueued, then re-enables. Frames enqueued before the barrier are the problem,
   so the flag must also drop what is already queued on `outCh` for that conn — or the
   replay must be enqueued BEFORE the state frame's broadcast window opens.
2. Cheaper: stamp each pane's replay with the `OutputBuf.Gen()` it was cut at and tag live
   frames with the generation they were produced under; the TUI discards live frames with a
   generation ≤ the replayed one for that pane until the first newer frame. Needs a wire
   field on `PaneOutputPayload` (omitempty, backward compatible).
3. Accept: document that the reattach join may show a duplicated partial frame for a child
   mid-write, and rely on the next repaint.
