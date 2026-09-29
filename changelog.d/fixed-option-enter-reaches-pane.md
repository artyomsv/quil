---
headline: Option+Enter inserts a newline in AI panes on macOS
---
- **`Option+Enter` now reaches the pane.** With "Use Option as Meta key" enabled,
  macOS Terminal.app sends `Option+Enter` as `ESC CR`, and it is the only chord there
  that inserts a newline in claude-code's prompt — Terminal.app sends the same bytes for
  `Shift+Enter` as for `Enter`. Quil forwarded `Alt` plus a printable key but dropped
  `Alt` plus `Enter`, so the key did nothing inside a pane. `Alt+Enter` and `Alt+Tab`
  now reach the pane as `ESC` followed by the key, and so does `Alt+Backspace` once it
  is unbound from `pane.go_back`.
