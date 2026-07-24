# Command Palette (`Alt+Shift+P`)

| Field | Value |
|-------|-------|
| Priority | 4 |
| Effort | Medium |
| Impact | High |
| Status | Implemented (v1) — see `docs/superpowers/specs/2026-07-23-command-palette-design.md` |
| Depends on | — |

## Problem

Quil has many features behind keybindings, but discoverability is poor. Users need to memorize shortcuts or open the help dialog. Power users expect a command palette (VS Code, Sublime Text, Raycast) — a single entry point for everything.

## Proposed Solution

A fuzzy-find overlay triggered by `Alt+Shift+P` (the default; `Ctrl+Shift+P` is intercepted by many terminals' own palette) that provides instant access to:

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
- `command_palette` keybinding (configurable; default `alt+shift+p`)
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

- [x] `Alt+Shift+P` opens the command palette (`Ctrl+Shift+P` opt-in; terminals often intercept it)
- [x] Typing filters commands with fuzzy matching
- [x] Enter executes the selected command
- [x] Existing keybinding actions are available as commands (dispatch into the same handlers)
- [x] Current tabs/panes appear as "Go to" / "Switch to tab" commands
- [ ] Saved plugin instances appear as "Create" commands — **deferred to Phase 2** (single "New pane…" ships in v1; per-plugin/instance quick-create needs the multi-step setup flow)
- [x] Response feels instant (< 16ms per keystroke)

## Shipped since v1

- **Content search** — runs alongside the command filter on every non-empty query (no `/` prefix, no separate mode): literal, case-insensitive matching against every pane's buffered scrollback across all tabs (including background and muted panes). Matching panes render in a **Found in panes** section below the filtered commands — one entry per pane (match count + most-recent-match preview), sorted by match count — as `palActGoToPane` rows so Enter/navigation reuse the command machinery. Only panes with a loaded output buffer are scanned — `searchPanes` skips a nil `OutputBuf` and never spawns a dormant pane — so lazily-restored panes become searchable once opened (their persisted ghost snapshot is searchable after restore; non-ghost-buffer plugins have nothing to search until spawned). New IPC pair `pane_search_req`/`pane_search_resp`, daemon-side scan in `internal/daemon/search.go`, TUI side in `internal/tui/palette_search.go`. See `docs/keybindings.md` and `docs/features.md`.

## Open Questions — resolved for v1

- `":"` prefix for direct command mode — **deferred to Phase 2** (redundant with fuzzy in v1).
- MRU (most recently used) persistence — **deferred to Phase 2** (v1 shows a curated fixed order when the query is empty).
- Search terminal output (VS Code "Go to Symbol") — **shipped**, see "Shipped since v1" above. Jumping to a specific matching line (not just the pane) and an MCP search tool remain deferred to Phase 2.
