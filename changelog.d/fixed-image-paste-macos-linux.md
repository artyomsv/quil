---
headline: Screenshot paste now works on macOS and Linux
---
- **Pasting a screenshot works on macOS and Linux, not only on Windows.** Quil
  takes `Ctrl+V` before the app in the pane sees it, and on macOS and Linux it
  could not read an image from the clipboard. So with an image on the
  clipboard, `Ctrl+V` did nothing, and Claude Code never got the chance to
  paste the image itself (#261).

  Quil now reads the image on macOS (through `osascript`, which ships with the
  OS) and on Linux (through `xclip`). It saves the PNG under `~/.quil/paste/`
  and types its path into the pane, as it already did on Windows.

  When the clipboard holds nothing Quil can paste, `Ctrl+V` is now passed to
  the app in the pane instead of being dropped, so the app can run its own
  paste.
