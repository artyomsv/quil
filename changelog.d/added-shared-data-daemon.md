---
headline: Groups, recent folders and pane notes follow you to every client
---
- **Project groups, recent folders and pane notes now live on the daemon.** A second
  TUI on the same machine, or one attached over ssh, sees the same groups, the same
  recent-folder list in `Ctrl+N` and the same notes. Group order and collapsed state
  stay per client. Your existing `project-groups.json`, `recent-cwds*.json` and
  `notes/*.md` are imported once, automatically, and never modified.
- **Editing one note from two places is safe.** A save from stale text is refused;
  the editor keeps what you typed and offers `Ctrl+R` reload or `Ctrl+S` overwrite.
  Text the daemon refused after the editor closed, or at quit, is kept under
  `~/.quil/notes-conflicts/`.
