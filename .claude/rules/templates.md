---
description: Workspace template configuration, creation, MCP adapters, layouts, and settings invariants
paths:
  - "internal/config/templates*"
  - "internal/daemon/template*.go"
  - "internal/daemon/mcp_spawn*.go"
  - "internal/daemon/worktree_add.go"
  - "internal/ipc/template.go"
  - "internal/tui/template*.go"
  - "cmd/quil/mcp*"
---

## Workspace templates

`config.Templates` describes a tab with 1–8 ordered panes, frozen arguments, optional prompts, and a layout hint. `$QUIL_HOME/templates.toml` defaults to the embedded file when absent. Invalid files return a named error. `WriteTemplatesSource` validates before atomic replacement and preserves the editor's comments and prompt formatting. F1 → Settings → Templates uses the existing TOML editor, saves locally and reloads only the local daemon. The destination daemon loads its own file on every create; remote catalog synchronization is not implemented.

The daemon validates plugins, toggle groups, permissions and directories before allocation. The first branch pane forwards its relative directory as `WorktreeSpec.Subdir`, validated inside the actual checkout. Both sides of containment must resolve; no missing-path fallback. Keep `WorktreePath` at the checkout root, never its subdirectory. Wrapped directory errors use `errors.As` and roll back the checkout and provisional template tab.

No branch: one completed state frame. Branch: preparing, existing swap, completed — no frame per pane. The response is immediate and may contain only a placeholder ID. The TUI waits until every claimed pane exists and `TemplateMain` names a final pane before building the initial layout. Never save a provisional tree through either layout-send path. Once built or restored, user layout changes win over the spent hint.

`mcp_spawn.go` owns ordinary per-spawn MCP adapters and model argument translation. `Pane.QuilMCP` is the sole opt-in. Claude uses `--strict-mcp-config`; Codex uses a bounded effective-server probe, disables inherited servers, and registers a collision-free bridge name. Whole-table overrides still merge. OpenCode may retain other servers. These are guard rails, not a shell security boundary. Spawn never re-reads a template to change frozen pane arguments.

The template dialog routes all text/paste by `templateTextTarget`; selector rows swallow paste. Character input comes from `msg.Text`. The directory row is the shared `cwdBrowse*` browser, reset through `resetDirBrowseState` on open, Esc and a successful reply, and committed as `m.cwdBrowseDir` — never a typed field, so it is absent from `templateTextTarget`. Submission is blocked while `m.browse.pending`, or a create lands on the daemon default rather than the directory on screen. Enter creates from every row except the task editor (newline) and the directory row (descend); Ctrl+S creates from all four. Browsing and creation are stamped with the destination pinned on open. Only the requesting client's correlated response arms focus. Prompts are queued in listed order after all panes exist; substitution is single-pass. Codex output is not a reliable answer channel: request a file in prompts that need results.

The ordinary MCP server exposes 35 tools. `create_from_template` requires daemon 1.73.0; older project/tab/task tools retain 1.72.0. See `docs/workspace-templates.md` and `docs/mcp.md`.
