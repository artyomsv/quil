---
headline: Edit panes, tabs and layout from the browser
---
- **The browser client can now change the workspace.** Pane and tab editing in `quil web` is being added in this release; see `docs/web.md`.
- **Keys page in F1 → Settings:** pick the key preset (default or tmux) and the tmux prefix, and the new keys work at once. Your own overrides in `bindings.toml` are kept; comments in that file are not.
- `quil web` accepts the 5b editing, dialog and notification requests; a saved instance is expanded from the gateway machine's own files, never from the page.
- The browser can split, replace, close, rename, mute, restart and move panes, rename, color and close tabs, and drag split borders. Large pastes wait for the pane instead of being dropped.
- The browser has the full create-pane dialog: type, saved instances (create, edit, delete), folder, kube context, toggles, worktree, sandbox and resume. Open it from a pane menu (New pane…, Replace…) or the tab bar `+`.
