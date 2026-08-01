---
name: hookevents-taint-boundary
description: Quantified trust boundary + resource bounds for the internal/hookevents pipeline — the numbers every security review of hook-driven state must reason against
metadata:
  type: project
---

Everything in a `hookevents.Payload` (`title`, `data[*]`, `hook_event`, `agent_type`, …) is
attacker-influenced at the same level as [[osc7-cwd-taint]]: the producer is the pane's own
child process. `$QUIL_HOME/events/<paneID>.jsonl` is same-user writable, and `parseAndValidate`
only enforces *filename == payload.pane_id* — it does not authenticate the writer, so any code
running as the user can forge events for any pane by appending to that pane's spool file.

The bounds that make this tolerable (verify they still hold before relying on them):

- **Rate**: `Ingester.allowAndRecord` — 100 events / 2 s rolling window per pane, then a 10 s
  drop penalty. A *well-paced* producer that stays just under the window never trips, so the
  real sustained ceiling is ≈50 events/s per pane, not the ≈10/s the tripped cycle implies.
- **Size**: spool line ≤ `MaxTotalBytes` (2 KiB, `parseAndValidate`); each `Data` value is
  re-clamped to `maxEventDataValueBytes` (1 KiB) at the IPC boundary in `toPaneEventPayload`.
  The *hook producer* additionally truncates to `MaxDataValueBytes` (128) — but a direct spool
  writer bypasses that, so 1 KiB is the number to use, never 128.
- **Lifecycle**: `paneID`s are UUIDs and `safePaneID` rejects `/`, `\`, `\x00` and `..`, so a
  crafted value can never forge another pane's key prefix. `Ingester.Cancel(paneID)` matches
  `paneID + "\x00"`, which still holds after `agent_type` joined the coalesce key.

**Why:** review rounds keep re-deriving these numbers and have twice reasoned from the wrong
one (the round-1 `subagents` dismissal leaned on "single int"; the 128-byte producer cap is the
easy wrong answer for key size). Rate-limiting bounds a *rate*, never a *total* — any state
keyed by payload content is unbounded in space until a terminal edge clears it.

**How to apply:** when a change makes daemon or TUI state keyed by, or sized by, payload
content, compute growth as ≈50/s × 1 KiB per pane and ask what clears it. Then weigh it against
the precondition: writing to the spool already requires code execution as the user, who can
equally talk to `quild.sock` or kill the process — so this class caps out at LOW unless it
crosses a privilege boundary or survives the lifecycle clears.

Note on Go: `clear(m)` empties a map's entries but does NOT return the table to the allocator —
Go maps never shrink. A cardinality spike stays resident for the object's lifetime.
