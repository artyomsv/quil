---
headline: Closing a tab's last pane now closes the tab
---
- **`Ctrl+W` on a tab's last pane closes the tab.** Quil used to put a fresh
  shell in the tab instead, so the tab never went away and `Ctrl+W` looked
  broken. Only `Alt+W` closed it (#262).

  Now the tab closes with its last pane. The confirm dialog says so before you
  press `Enter`. If it was the project's only tab, you get one new `Shell`
  tab, as after `Alt+W`, so the window is never empty.

  The MCP `destroy_pane` tool does the same: an agent that closes a tab's last
  pane closes the tab.
