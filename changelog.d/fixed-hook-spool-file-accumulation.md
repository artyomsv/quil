---
headline: The daemon no longer polls spools of long-dead panes
---
- **The daemon stops re-polling hook spools for panes that no longer exist.** It reads
  `$QUIL_HOME/events/<paneID>.jsonl` every 200 ms, opening and stat-ing every file it
  finds. On daemon start these were *truncated* rather than deleted, so a zero-byte file
  survived for every pane that ever existed — across every restart, for the life of the
  install.

  The cost is the file count, not their size. One measured workspace had 349 spool files
  for 37 live panes, 332 of them empty husks, driving roughly 7,000 file-handle
  operations a second and holding a full CPU core at 21% in kernel time with the session
  otherwise idle. Typing echo, tab switching and scrolling all lagged behind it.

  Startup now unlinks stale spools instead of emptying them, falling back to the old
  truncation if the file cannot be deleted so a previous session's notifications still
  never replay.
