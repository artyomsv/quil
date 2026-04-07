# Command Palette (`Ctrl+Shift+P`)

| Field | Value |
|-------|-------|
| Priority | 4 |
| Effort | Medium |
| Impact | High |
| Status | Proposed |
| Depends on | — |

## Problem

Quil has many features behind keybindings, but discoverability is poor. Users need to memorize shortcuts or open the help dialog. Power users expect a command palette (VS Code, Sublime Text, Raycast) — a single entry point for everything.

## Proposed Solution

A fuzzy-find overlay triggered by `Ctrl+Shift+P` that provides instant access to:

- **Search panes** by name, plugin type, CWD
- **Execute commands**: split, close, rename, resize
- **Switch tabs/panes** instantly
- **Create new pane** from plugin
- **Open saved instances** (SSH connections, etc.)

Cheap to implement (fuzzy string matching + existing dialog system) but makes the tool feel **polished and modern**. The single UI feature that makes power users love a tool.

## User Experience

```
┌─ Command Palette ──────────────────────────────┐
│ > split hor_                                    │
│                                                 │
│   Split Horizontal          Alt+H               │
│   Split Vertical            Alt+V               │
│   Close Pane                Ctrl+W               │
│   Close Tab                 Alt+W                │
│                                                 │
│ ↑↓ Navigate  Enter Select  Esc Close            │
└─────────────────────────────────────────────────┘
```

### Command Categories

| Category | Examples |
|----------|---------|
| Navigation | `Go to: 1:Shell`, `Go to: 2:Build`, `Focus pane: backend` |
| Layout | `Split horizontal`, `Split vertical`, `Toggle focus mode` |
| Pane | `Close pane`, `Rename pane`, `New terminal`, `New claude-code` |
| Tab | `New tab`, `Close tab`, `Rename tab`, `Cycle tab color` |
| Plugin | `Create: SSH → production-server`, `Create: Stripe → webhooks` |
| System | `Settings`, `Shortcuts`, `Plugins`, `About` |

### Fuzzy Matching

- Type fragments: `clau` matches "New claude-code pane"
- Type shortcuts: `alt h` matches "Split horizontal (Alt+H)"
- Type pane names: `back` matches "Go to: backend"
- Most recently used commands float to top

## Technical Approach

### 1. Command Registry

```go
type PaletteCommand struct {
    Label    string   // Display text
    Detail   string   // Secondary text (shortcut, description)
    Category string   // Grouping
    Keywords []string // Additional search terms
    Action   func()   // What to execute
}
```

Commands are registered at startup from:
- Static commands (split, close, rename, etc.)
- Dynamic commands (current tabs/panes, saved instances)

### 2. Fuzzy Matching

Implement simple fuzzy substring scoring:
- Exact prefix match: highest score
- Consecutive character match: high score
- Non-consecutive match: lower score
- Case-insensitive

Or use a lightweight Go fuzzy library.

### 3. UI Integration

- New `dialogPalette` state in dialog system
- Full-width overlay with input field + scrollable results
- `Ctrl+Shift+P` keybinding (configurable)
- Results update on every keystroke
- Enter executes selected command, Esc closes

### 4. Files

| File | Change |
|------|--------|
| `internal/tui/palette.go` | New — command registry, fuzzy matching, rendering |
| `internal/tui/model.go` | Add `dialogPalette` state, keybinding handler |
| `internal/tui/dialog.go` | Add palette to dialog switch |
| `internal/config/config.go` | Add `command_palette` keybinding |

## Success Criteria

- [ ] `Ctrl+Shift+P` opens command palette overlay
- [ ] Typing filters commands with fuzzy matching
- [ ] Enter executes the selected command
- [ ] All existing keybinding actions are available as commands
- [ ] Current tabs/panes appear as "Go to" commands
- [ ] Saved plugin instances appear as "Create" commands
- [ ] Response feels instant (< 16ms per keystroke)

## Open Questions

- Should the palette support ":" prefix for direct command mode (`:split h`)?
- MRU (most recently used) persistence across sessions?
- Should it also search terminal output (like VS Code's "Go to Symbol")?
