---
headline: The mouse wheel keeps scrolling panes after quil updates itself
---
- **After an in-place update, the mouse wheel scrolled the terminal instead of the pane** (macOS Terminal.app, #268). On macOS and Linux the new quil now replaces the old process instead of running beside it as a child, so one quil owns the terminal, exactly as on a fresh launch. Windows keeps the old relaunch, because it has no way to replace a running process.
