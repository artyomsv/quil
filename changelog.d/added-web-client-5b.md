---
headline: Edit the workspace from the browser: split, close, dialog, keys
---
- **Browser client: editing.** `quil web` can now create, split, replace, close, rename, mute, restart and move panes, create, rename, color and close tabs, and drag split borders. The create-pane dialog has every option of the terminal dialog: type, saved instances, folder, kube context, toggles, worktree, sandbox and resume. Large pastes wait for the pane instead of being dropped.
- **Browser client: notifications, overlays and keys.** The browser has the notification list, the lazygit and hunk overlays (`Alt+G`, `Alt+D`) and your keymap, including the tmux preset. Keys a browser keeps for itself get a fallback chord, shown in the `F1` key list.
- **F1 → Settings → Keys** switches the key preset and prefix without a restart. Your own overrides in `bindings.toml` are kept; comments in that file are not.
- The daemon now keeps every saved layout in step with the panes that exist, so a closed pane can no longer come back from an old layout write.
