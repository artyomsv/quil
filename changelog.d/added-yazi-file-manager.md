---
headline: yazi file manager as a pane and an Alt+Shift+F overlay
---
- **yazi, a terminal file manager, is now a built-in tool.** It has two entry
  points, like lazygit and hunk. **Ctrl+N → Tools → Yazi** opens it as an
  ordinary pane, with the plain folder browser as the directory step.
  **`Alt+Shift+F`** toggles a full-tab yazi on the active pane's own directory —
  any folder, no git repository needed. A hidden yazi keeps running, so showing
  it again in the same folder keeps your place, tabs and selection; after the
  pane has moved to another folder, a new yazi opens there. It shares the tab's
  one overlay slot with lazygit (`Alt+G`) and hunk (`Alt+D`), so the keys swap
  tools. Offered only when the `yazi` binary is installed, and shown greyed with
  a link to the project otherwise. Also in the pane menu (**Open yazi**) and the
  command palette. New action `pane.toggle_yazi`, default `alt+shift+f` (not
  `alt+f`, which is readline forward-word), plus `alt+F` for macOS Terminal.app;
  rebind it in `bindings.toml`. A folder that no longer exists is refused with a
  message instead of opening yazi somewhere else, and the old overlay stays. Image
  previews do not work inside Quil, because its panes do not answer terminal
  queries; yazi falls back to text previews. The browser client does not have
  the overlay yet.
