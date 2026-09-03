---
headline: A remote host no longer hides tools installed locally
---
- **A remote host no longer reports "not installed" for tools you have locally.** Every
  daemon is asked which plugins its own machine can run, but the client kept a single
  answer for all of them — so the last host to reply spoke for every project. Connect a
  remote box without `claude` on it and `Ctrl+N` offered "Claude Code (not installed)" in
  your local project too, on a machine where `claude` was running at that moment.

  Nothing recovered from it either: availability detection only ever turns a tool *on*,
  so the grey-out survived until the client was restarted, and returned as soon as that
  host attached again.

  Each daemon's answer is now filed against the daemon that gave it, and every place that
  greys out a tool — the `Ctrl+N` list, the command palette, the pane context menu, the
  `Alt+G` / `Alt+D` overlays and the `F1` → Plugins list — asks about the machine the pane
  would actually be created on.
