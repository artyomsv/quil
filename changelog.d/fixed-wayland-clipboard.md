---
headline: Copy and paste use the Wayland clipboard on Linux
---
- **Copy and paste work with the native clipboard in Linux Wayland sessions.** Quil
  only knew `xclip` / `xsel`, which reach a Wayland clipboard through the
  compositor's XWayland bridge. That bridge is not dependable: on KDE Plasma, `Ctrl+V`
  in Quil pasted nothing, or an older clipboard, after copying in a native Wayland
  app, and a compositor without XWayland left Quil with no clipboard at all.

  When `WAYLAND_DISPLAY` is set and wl-clipboard is installed, Quil now uses
  `wl-copy` / `wl-paste` for copy, paste and image paste. If wl-clipboard fails, the
  copy or paste is retried with `xclip` / `xsel`. If there is nothing to retry
  with, a timed-out copy or paste makes the next ones fail at once for 30 seconds
  instead of each waiting again. X11 sessions are unchanged, and a Wayland session
  without wl-clipboard still uses `xclip` / `xsel`.

- **Pasting more than 10 MB of text no longer hangs the paste on macOS and Linux.**
  Quil stopped reading at its 10 MB limit but then waited for a clipboard helper
  that was still trying to write the rest, so the paste never arrived. The helper is
  now stopped at the limit and the paste is refused. Every copy and paste helper is
  also given 5 seconds to finish instead of being waited on forever, and a paste
  helper that is stopped takes its own child processes with it.
