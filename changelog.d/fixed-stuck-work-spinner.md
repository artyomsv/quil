---
headline: Work spinner no longer sticks after a subagent dies
---
- **A tab no longer spins forever after one of its subagents dies.** When a
  subagent's turn fails — the claude.ai usage limit is the common cause — Claude
  Code reports `StopFailure` for that agent and never a `SubagentStop`. Quil read
  it as the pane's own turn ending and kept the dead agent on its books, so the
  work spinner stayed lit for days; two production tabs sat like that after the
  usage limit hit on 2026-09-02. A subagent's failure is now recorded under the
  agent's name and drains it, exactly as its completion would have. The
  notification card names the agent and carries Claude's `error` field —
  "Turn failed" cards used to read a field the event does not have and always
  showed an empty reason.

- **Hook events reach the TUI in the order they happened.** Two events for one
  pane that landed in the same 200 ms spool read were delivered last-first (every
  run on Windows), because each coalesce key ran its own timer and Go schedules
  the newest timer goroutine first. A stop applied before its start left a pane
  lit until session end, and a permission prompt that followed a heartbeat had
  its "needs you" mark erased on arrival. The coalescer now releases events in
  arrival order.
