---
headline: Full-screen panes come back clean after a reconnect
---
- **Reconnecting no longer paints a torn frame into a full-screen pane.** Quil
  replays a pane's recent output when a client reconnects, which reproduces
  history for a normal shell but not for a program drawing on the alternate
  screen: those programs send only the parts of the screen that changed, so once
  the saved buffer wraps there is no complete frame left in it to replay. Claude
  Code's newer full-screen renderer is one of them, and it is the default for
  recent installs — panes could come back showing fragments of escape sequences
  as text, and stay that way until you typed something.

  Quil now notices which panes are on the alternate screen and asks those
  programs to repaint instead of replaying at them. A plugin's `ghost_buffer`
  setting is still what decides for everything else.
