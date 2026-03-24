# tmux Migration Path

| Field | Value |
|-------|-------|
| Priority | 8 |
| Effort | Small |
| Impact | Medium |
| Status | Proposed |
| Depends on | — |

## Problem

tmux has millions of users with years of muscle memory. Switching to Aethel means re-learning keybindings and manually recreating session layouts. The switching cost is high enough that many users won't bother trying Aethel, even if they find the feature set compelling.

Making switching painless is the fastest acquisition channel.

## Proposed Solution

Two import commands that lower the barrier to adoption:

```bash
aethel import-keybindings tmux    # reads ~/.tmux.conf, maps to config.toml
aethel import-session             # snapshot running tmux session → Aethel workspace
```

## User Experience

### Keybinding Import

```bash
$ aethel import-keybindings tmux
Reading ~/.tmux.conf...
Mapped 12 keybindings:
  prefix + "    → split_horizontal (Alt+H)
  prefix + %    → split_vertical (Alt+V)
  prefix + c    → new_tab (Ctrl+T)
  prefix + x    → close_pane (Ctrl+W)
  prefix + z    → focus_pane (Ctrl+E)
  prefix + n    → next_tab
  prefix + p    → prev_tab
  ...
Written to ~/.aethel/config.toml
Note: Aethel doesn't use a prefix key — bindings are mapped to direct shortcuts.
```

### Session Import

```bash
$ aethel import-session
Detected tmux session: "dev" (3 windows, 7 panes)
Importing:
  Window 1: "code" → Tab 1 (2 panes, vertical split)
  Window 2: "servers" → Tab 2 (3 panes, mixed splits)
  Window 3: "logs" → Tab 3 (2 panes, horizontal split)
Workspace created. Run 'aethel' to open.
Note: Running processes are not migrated — only layout and directories.
```

## Technical Approach

### 1. Keybinding Parser

Parse `~/.tmux.conf` for `bind-key` / `bind` directives:

```
bind-key -T prefix '"' split-window -v
bind-key -T prefix '%' split-window -h
bind-key -T prefix 'c' new-window
bind-key -T prefix 'z' resize-pane -Z
```

Map tmux actions to Aethel config keys:

| tmux Action | Aethel Keybinding |
|-------------|-------------------|
| `split-window -v` | `split_horizontal` |
| `split-window -h` | `split_vertical` |
| `new-window` | `new_tab` |
| `kill-pane` | `close_pane` |
| `resize-pane -Z` | `focus_pane` |
| `next-window` | Next tab |
| `previous-window` | Previous tab |
| `select-pane -t +1` | `next_pane` |

Note: Aethel doesn't use a prefix key — tmux `prefix + x` becomes a direct shortcut.

### 2. Session Snapshot

Use `tmux list-windows` and `tmux list-panes` to capture:
- Window names → tab names
- Pane layout (tmux layout string) → split tree
- Pane CWDs → pane working directories
- Pane dimensions → split ratios

```bash
tmux list-windows -t dev -F "#{window_index}:#{window_name}:#{window_layout}"
tmux list-panes -t dev -F "#{pane_index}:#{pane_current_path}:#{pane_width}:#{pane_height}"
```

Output as workspace JSON or `.aethel.toml` (if workspace files feature is implemented).

### 3. Files

| File | Change |
|------|--------|
| `cmd/aethel/import.go` | New — `import-keybindings` and `import-session` subcommands |
| `internal/tmux/parser.go` | New — tmux.conf parser and keybinding mapper |
| `internal/tmux/session.go` | New — tmux session snapshot via CLI commands |

## Success Criteria

- [ ] `aethel import-keybindings tmux` reads `.tmux.conf` and updates `config.toml`
- [ ] Mapped keybindings work correctly in Aethel
- [ ] `aethel import-session` captures running tmux layout
- [ ] Imported session preserves window names, pane CWDs, split structure
- [ ] Clear output showing what was mapped/imported
- [ ] Unmappable bindings are listed with explanations

## Open Questions

- Support for tmux plugins (tpm) keybindings?
- Handle custom prefix keys beyond `Ctrl+B`?
- Should session import also capture environment variables?
- Support importing from screen as well?
