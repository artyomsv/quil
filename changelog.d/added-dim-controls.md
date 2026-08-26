---
headline: Turn the unfocused dim on or off without editing a file
---
- **The unfocused-window dim is now switchable and tunable from inside Quil.** Until
  now the only way to change it was to hand-edit `unfocused_dim` in `config.toml` and
  relaunch. There are two new front doors, and both take effect on the next repaint:

  - **`F1` → Settings** gained two rows. **Unfocused dim** switches the effect on and
    off, and **Unfocused dim level** takes a number from `0.01` to `0.9` — higher
    fades further toward your terminal's background.
  - **The command palette** (`Alt+Shift+P`, search for "dim") offers the same switch
    plus three ready-made levels: subtle (`0.30`), normal (`0.60`) and strong
    (`0.85`). The level currently in effect is marked.

  Switching the dim off **keeps** your level, so turning it back on returns you to the
  setting you chose rather than the default.

- **New `[ui]` key: `unfocused_dim_enabled`** (default `true`), the off switch that
  sits beside the existing `unfocused_dim` level. Existing configurations need no
  changes — a config file written before this key existed keeps dimming exactly as it
  did, and one that switched the dim off with `unfocused_dim = 0` stays off.
