---
headline: Frames cost a third less and big workspaces use far less memory
---
- **Drawing a frame is about a third cheaper.** Quil was re-measuring the width of
  every line it had just drawn, in order to line blocks up that were already the
  same width — roughly half the work in assembling a screen. Measured across six
  runs: **34–35% faster** on a normal repaint and **up to 56% faster** when only
  the chrome changes, with about a third fewer allocations.

- **Default scrollback depth now scales to the size of your workspace.** Every pane
  keeps its own terminal emulator whether or not it is visible, so depth multiplies
  by pane count — 41 panes at the old fixed default measured 847 MB of client
  memory, climbing toward 1.1 GB. The default now spends a budget across the
  workspace instead of a fixed depth per pane.

  A workspace of ten panes or fewer is completely unchanged. Setting
  `[ui] scrollback_lines` yourself still wins and is never adjusted, and when a
  depth is chosen for you it is written to the log rather than applied silently.

- **Scrollback depth is now editable in F1 → Settings** instead of only in
  `config.toml`. It applies on next launch, like every other Settings row.

- **F1 → Processes lists quil's own processes**, separating out any MCP bridges
  whose parent has gone. Those can be stopped from the dialog; anything that is not
  quil's own is shown but never offered for termination.
