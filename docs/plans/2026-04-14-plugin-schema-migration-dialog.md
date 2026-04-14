# Plugin Schema Migration Dialog

## Problem

When Quil ships new plugin features (e.g., `prompts_cwd`, `[[command.toggles]]`, updated `resume_args`), existing users never receive them. `EnsureDefaultPlugins` creates plugin TOMLs on first run but never updates them. Users with stale configs silently miss new functionality.

A `schema_version` field was added to embedded defaults, and `EnsureDefaultPlugins` can detect stale files. But silently overwriting loses user customizations. A migration dialog lets the user merge their edits with the new defaults.

## Design

### Trigger

On TUI startup, after daemon attach and workspace load, the TUI checks for stale plugins. If any are found, a blocking migration dialog opens before the user can interact with panes.

Detection: `EnsureDefaultPlugins` is modified to return stale plugin data instead of overwriting. A stale plugin is one where the user file's `schema_version` < the embedded default's `schema_version`.

### Dialog Layout

Full-screen split view (reuses existing `TextEditor` + TOML highlighting):

```
┌─ Plugin Migration ──────────────────────────────────────┐
│ [claude-code]  [ssh]          (tabs if multiple plugins) │
│                                                          │
│  Your config (editable)      │  New default (read-only)  │
│ ───────────────────────────  │  ──────────────────────── │
│  [plugin]                    │  [plugin]                 │
│  name = "claude-code"        │  name = "claude-code"     │
│  ...                         │  schema_version = 2       │
│                              │  ...                      │
│  [command]                   │  [command]                │
│  cmd = "claude"              │  cmd = "claude"           │
│                              │  prompts_cwd = true       │
│                              │  [[command.toggles]]      │
│                              │  ...                      │
│                                                          │
│ ──────────────────────────────────────────────────────── │
│  Enter save  A accept default  Tab switch focus          │
│  Ctrl+Left/Right switch plugin   Esc blocked             │
└──────────────────────────────────────────────────────────┘
```

- **Left pane**: User's current TOML (editable TextEditor, TOML syntax highlighting)
- **Right pane**: Embedded default TOML (read-only TextEditor, TOML highlighting)
- **Tab bar**: One tab per stale plugin. Ctrl+Left/Right or number keys to switch.
- **Footer**: Keybinding hints

### User Actions

| Key | Action |
|-----|--------|
| Enter | Validate left-pane TOML, save to disk, advance to next plugin (or close) |
| A | Replace left pane with the full default content (accept new default) |
| Tab | Toggle keyboard focus between left and right panes |
| Ctrl+Left/Right | Switch between plugin tabs (when multiple) |
| Esc | No-op (blocked — migration must be resolved) |
| Ctrl+Q | Quit the application entirely |

### Blocking Behavior

The dialog blocks all pane interaction until every stale plugin is resolved. Esc is a no-op. The only exits are:

1. Save merged config for each plugin (Enter)
2. Accept the new default for each plugin (A then Enter)
3. Quit the application (Ctrl+Q)

This guarantees all plugins are at the current schema version before the user can work.

### TOML Validation

On Enter, the left-pane content is parsed as TOML. If parsing fails, an error message appears below the editor (same pattern as the existing TOML editor dialog). The user must fix the syntax before saving.

Additionally, `schema_version` in the saved content must be >= the embedded default's version. If the user removes or lowers it, show an error.

### Data Flow

1. `EnsureDefaultPlugins(dir)` → returns `[]StalePlugin` (name, user bytes, default bytes) for plugins needing migration. Files that are current or newly created are handled as before.
2. `cmd/quild/main.go` calls `EnsureDefaultPlugins` on daemon startup — daemon sees the stale list but doesn't need to act (it loads whatever TOML is on disk).
3. `cmd/quil/main.go` calls `EnsureDefaultPlugins` during TUI init. If stale plugins are returned, they're passed to `tui.NewModel` which sets `m.dialog = dialogPluginMigration`.
4. The migration dialog renders after the disclaimer dialog (if shown) and after workspace state loads.
5. On save/accept, the resolved TOML is written to disk. The plugin registry is reloaded so the TUI picks up the new config immediately.

### Reused Components

| Component | Location | Usage |
|-----------|----------|-------|
| `TextEditor` | `internal/tui/editor.go` | Both panes (left editable, right read-only) |
| TOML highlighting | `TextEditor.Highlight = "toml"` | Syntax coloring in both panes |
| Full-screen dialog | `dialogTOMLEditor` pattern in `dialog.go` | Bypass dialog box, render full screen |
| `parseSchemaVersion` | `internal/plugin/defaults.go` | Validate saved content has correct version |
| Dialog footer pattern | Existing dialog rendering | Keybinding hints |

### Files to Modify

| File | Change |
|------|--------|
| `internal/plugin/defaults.go` | Change `EnsureDefaultPlugins` to return `[]StalePlugin` |
| `internal/plugin/plugin.go` | Add `StalePlugin` struct |
| `cmd/quil/main.go` | Pass stale plugins to TUI model |
| `cmd/quild/main.go` | Handle new return value (daemon ignores stale list) |
| `internal/tui/model.go` | Add `dialogPluginMigration` iota, model fields, Update handler |
| `internal/tui/dialog.go` | Add render + key handler for migration dialog |

### Edge Cases

- **No stale plugins**: Dialog never appears. Normal startup.
- **User deletes schema_version from their edit**: Validation rejects — error shown.
- **User pastes the default into the left pane**: Valid — same as pressing A.
- **Multiple plugins stale**: Tab bar appears. Each must be resolved independently.
- **Plugin file deleted between detection and save**: Re-create from user's edited content (same as fresh write).
