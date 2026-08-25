---
headline: Quil dims itself when the window loses focus
---
- **The whole frame fades while the terminal window is unfocused.** Typing a command
  into a window that only *looked* focused is now visible before the first keystroke
  lands: when the terminal window loses OS focus, every colour on screen — panes,
  tab bar, sidebar, borders, status bar — blends toward the terminal's own
  background, and snaps back the moment focus returns. Nothing else changes; the
  text, the layout and the cell widths are identical, only the colours move.

  Quil asks the terminal for its real default foreground and background (OSC 10/11)
  and fades toward those, so the effect matches your theme rather than assuming a
  black background. Tune it with `unfocused_dim` under `[ui]` in `config.toml` —
  `0.45` by default, `0` to switch it off entirely.

  Terminals that do not implement focus reporting (DEC 1004) never dim, since they
  never report losing focus in the first place.
