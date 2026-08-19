---
headline: Claude panes no longer clear themselves on reconnect
---
- **A Claude Code pane no longer clears its own conversation.** Quil asks such a
  pane to repaint by sending it `Ctrl+L`, and Claude Code v2.1.126+ runs
  `/clear` when it receives two of those within two seconds. A daemon restart, a
  client reattach, or two quick resizes delivered exactly that pair — wiping the
  conversation in every AI pane at once, with nobody at the keyboard.

  Quil now keeps any two deliveries of a plugin's `redraw_key` at least three
  seconds apart, and coalesces the ones it holds back into a single later
  repaint.
