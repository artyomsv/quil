---
headline: Alt+R resumes Claude panes, and restored ones stay visible
---
- **Restarting a Claude Code pane resumes its conversation.** Alt+R (and the
  MCP `restart_pane` tool) respawned the pane with `--session-id` and the id it
  already had, and Claude refuses an id that already has a transcript on disk
  ("Session ID … is already in use", exit 129) — so every restart of a pane
  that had exchanged a message landed on that error screen, and had since at
  least August. The daemon now checks for the pane's transcript the way a
  daemon restart does and passes `--resume` when it is there. A pane whose
  session was never persisted (restarted on the trust screen) still starts
  fresh with its preassigned id, which is what Claude accepts for an unused
  one.
- **A restored Claude pane shows what its process drew, instead of a black
  rectangle.** On the first attach after a daemon restart, quil skipped the
  replay for Claude panes — the respawned process repaints its own transcript
  — and asked it to redraw with Ctrl+L. That is right while the process is
  still starting, and wrong once it has drawn a screen that ignores Ctrl+L:
  Claude's first-run setup, its login prompt, a startup error. The pane
  stayed empty and nothing said why. The daemon now replays the process's own
  output when it has already written, exactly as a reconnect does, and keeps
  the skip for a process that has not painted yet.
