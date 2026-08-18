---
headline: Large workspaces feel less sluggish to type in
---
- **The TUI stops rebuilding the screen for messages that change nothing.** Bubble Tea asks
  the model for a frame once per message it receives, and a frame on a 37-tab workspace costs
  around 2.8 ms — nearly three quarters of it spent measuring the width of text that had not
  changed since the last one. Timers drove most of those frames: a one-second terminal-size
  poll, a five-second memory refresh, and messages the TUI does not handle at all.

  Those now reuse the previous frame. Anything that genuinely changes the screen still
  repaints immediately; rendering remains the default and only cases audited as inert are
  skipped.

- **The work-in-progress spinner animates at 200 ms instead of 100 ms.** It was the single
  largest source of repaints — around 65% of them — and it never stops while any agent is
  mid-turn. The ten braille frames now cycle in two seconds rather than one, which still
  reads as motion at half the cost.
