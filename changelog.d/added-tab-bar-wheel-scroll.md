---
headline: Scroll the tab bar with the mouse wheel
---
- **The mouse wheel over the tab bar now scrolls the strip instead of switching
  tabs.** When there are more tabs than fit, wheeling over row 0 moves the
  visible window one tab at a time without changing which tab is active and
  without reaching the pane underneath.

  The overflow indicator is now two-sided (`«N` on the left, `N»` on the
  right) so it always says how many tabs are hidden on each side of the
  current view, not just a single combined count. Clicking a tab while
  scrolled keeps the current scroll position instead of re-centering; any
  other way of switching tabs (keyboard, the palette, an MCP tool) returns
  the bar to auto-centering on whichever tab becomes active.
