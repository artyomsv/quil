---
headline: Tab drag no longer jitters; tabs and projects move by keyboard
---
- **Dragging a tab no longer jumps around.** A tab now moves only once the pointer
  passes the middle of the tab it is over. Before, a narrow tab dragged over a wide
  one swapped back and forth on every mouse move, and a wide tab dragged over narrow
  ones flew across several slots at once.
- **Tabs move from the keyboard.** `Alt+Shift+PgUp` / `Alt+Shift+PgDn` slide the
  active tab one slot left or right (`tab.move_left` / `tab.move_right`, also in the
  command palette and F1 → Shortcuts).
- **The project sidebar reorders too.** Drag a tab name up or down the PANES list to
  reorder tabs, and drag a project row to reorder projects — the same midpoint rule,
  measured in rows. Clicking a tab name in the sidebar now switches to that tab.
  `Alt+Shift+Up` / `Alt+Shift+Down` move the active project (`project.move_up` /
  `project.move_down`). Each daemon saves the order of its own projects; when several
  hosts are connected, how their rows interleave is kept for the running session only.
