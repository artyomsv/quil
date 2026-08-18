---
headline: Per-pane scrollback depth is now configurable
---
- **`[ui] scrollback_lines` sets how much scrollback each pane keeps.** The depth was fixed
  at 10 000 lines, and every pane holds its own terminal emulator whether or not you are
  looking at it — so the figure multiplies by pane count. A 37-pane workspace measured
  1.13 GB of client memory.

  The default is unchanged, so no existing install loses history. Lower it if you run many
  panes on a memory-tight machine; an unset or nonsensical value falls back to the default
  rather than leaving panes with no history.
