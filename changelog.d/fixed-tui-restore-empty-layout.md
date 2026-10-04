---
headline: The TUI no longer crashes on a tab whose saved layout lost all its panes
---
- **The TUI no longer crashes when it opens a tab whose saved layout names none of the tab's panes.** This happened when a tab's last pane ended while no TUI was attached (for example, an agent closed it): the daemon put a fresh pane in the tab but kept the old layout, and the next TUI to attach crashed. The TUI now rebuilds the layout from the tab's panes.
