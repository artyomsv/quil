---
headline: Horizontal wheel no longer scrolls terminal apps down
---
- **A horizontal mouse-wheel notch over a pane running vim, htop, lazygit,
  claude-code or another mouse-tracking app no longer scrolls it down.** A
  trackpad or shift-scroll notch (`MouseWheelLeft`/`MouseWheelRight`) was
  read as "not up" and forwarded to the app as wheel-down, so a horizontal
  swipe scrolled the wrong direction instead of doing nothing.
