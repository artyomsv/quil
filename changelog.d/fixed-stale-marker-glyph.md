---
headline: The stale-binary marker no longer eats the space beside it
---
- **The process dialog's stale marker renders correctly again.** A quil process
  running a binary that differs from the daemon's is flagged in the `QUIL`
  section, and that flag used `⚠` — a codepoint terminals are free to render
  with a colour emoji face, drawing it about two cells wide while advancing
  only one. The result overpainted the space after it, so the row read
  `⚠stale bridge`, and in some terminals every column after it drifted.

  It now uses an outline triangle, which has no emoji presentation to fall back
  to. The same rule already governs the sidebar's status glyphs, for the same
  reason.
