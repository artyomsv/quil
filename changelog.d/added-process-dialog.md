---
headline: See what is running under every pane, and stop it
---
- **`F1 → Processes` shows the real process tree under each pane** — the shell or
  agent quil started, and everything it went on to spawn — with memory and CPU
  for each one. The build that will not stop, the language server that has
  doubled in size, the agent's child that outlived it: the thing you would
  otherwise open a process explorer for.

  Press `K` on any process *below* a pane's own shell to stop it and everything
  it started. The pane's own shell or agent is not offered here — that is
  `Restart pane` — and quil's own processes are never offered at all. Stopping
  asks the process to exit before forcing it, except on Windows, which has no
  graceful signal for another process and says so on the confirm.

- **It also lists quil's own processes** with version, uptime and PID: the TUI,
  the daemon, and every MCP bridge. A bridge still running an older binary is
  flagged, which is how you catch one pinned to a version an in-place upgrade
  renamed aside.

- **This replaces `F1 → Memory`**, which showed a subset of the same thing. The
  per-pane memory breakdown and the status bar's `mem` segment are unchanged,
  and both MCP tools (`get_memory_report`, `get_pane_memory`) are untouched.

- Everything is read on the **daemon's** machine — the one actually running the
  processes — so it works unchanged when the daemon is remote.

- Two honesty notes the dialog surfaces rather than hides. A process that has
  not been sampled twice yet shows `—`, not `0%`, because an unknown is not an
  idle. And CPU on macOS is the kernel's own decaying average rather than usage
  over quil's sample window, so those numbers are not comparable with a Linux
  host's.
