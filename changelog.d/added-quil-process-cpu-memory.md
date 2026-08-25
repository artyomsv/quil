---
headline: The process dialog now shows quil's own CPU and memory
---
- **The process dialog reports quil's own processes, not just the panes'.** The
  `QUIL` section listed a role, version, uptime, PID and binary for the TUI, the
  daemon and every MCP bridge — but no resource columns at all. It could show you
  a pane's `claude` burning CPU while saying nothing about the TUI that was
  burning more than any of them.

  Each quil process now measures itself and reports it, so the section carries
  `MEM` and `CPU` alongside the existing columns. Nothing is inferred from the OS
  process table: a process describes itself over the socket the same way it
  already reports its version, which is also the only thing that works when the
  daemon is on another machine.

  A process that has not reported yet, one whose platform has no per-process CPU
  counter, and one that has stopped reporting all render as `—`. None of them
  render as `0%`, which would read as idle — the wrong claim in a dialog you
  opened to find something that is spinning.
