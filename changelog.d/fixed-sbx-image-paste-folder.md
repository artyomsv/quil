---
headline: Image paste into an sbx pane works again
---
- **Pasting an image into an sbx sandbox pane works again.** With sbx 0.53 the
  paste failed with `destination directory "/tmp/quil-paste-…" does not exist`:
  newer `sbx cp` no longer makes missing folders. Quil now makes the paste folder
  in the sandbox first, then copies the image into it.
