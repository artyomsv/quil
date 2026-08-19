---
headline: Background panes no longer force the screen to redraw
---
- **Output from a pane you are not looking at no longer redraws the screen.** Only the
  active tab is ever rendered, but the client rebuilt its entire frame once per message
  regardless of which pane the message came from — so on a workspace with dozens of tabs,
  most redraws could not change a pixel. They are now served from the last frame instead.

  Measured on a 41-tab workspace, that was roughly 65% of all redraws. Four things a
  background pane does still count as a real change and still redraw: it finishes
  restoring, it paints its first live frame, it changes directory, or it is the pane on
  screen.

- **The daemon stops re-opening event spool files that have nothing new.** The scan runs
  five times a second across every pane, and the directory listing already carries the
  file size — so the common "nothing changed" case now costs no file handle at all.
