# Command Palette (`Ctrl+Shift+P`) — Design

Date: 2026-07-23
Status: Approved

## Goal

A single keystroke opens a modal, centered, keyboard-first fuzzy-find overlay
that lists every Quil action and every open tab/pane. Type a fragment of the
intent ("split", "restart", "backend"), the list filters live by fuzzy score,
Enter runs the highlighted entry, Esc closes. The palette is a **launcher, not
a re-implementation**: every command dispatches into the exact handler the
keybinding already calls — a third dispatcher over the same methods, alongside
the keybinding switch and the context menu.

The payoff is discoverability: Quil has ~40 actions behind keys today, reachable
only if you memorize the binding or dig through F1. The palette makes all of
them findable by typing, and its right-hand shortcut column teaches the binding
every time you use it.

## Decisions (locked)

1. **It is a `dialogScreen`, NOT a compositor overlay.** The palette is modal,
   centered, keyboard-capturing, and needs no mouse-anchored positioning — the
   exact shape of `dialogCommandHistory` / `dialogMemory`. Adding a
   `dialogCommandPalette` value gives free input routing (the `m.dialog !=
   dialogNone` gate at the top of `handleKey`) and free centered rendering
   (`renderDialog` + `lipgloss.Place`). The `overlayAt` compositor the context
   menu uses only earns its complexity for a surface that sits over live panes
   at a cursor position; the palette does neither. (This reverses an earlier
   hypothesis — the codebase evidence is decisive.)

2. **Keybinding default is a two-binding list: `ctrl+shift+p,alt+shift+p`.**
   `ctrl+shift+p` is the VS Code / Sublime muscle-memory trigger and the
   discoverable default. But legacy terminals without the Kitty keyboard
   protocol cannot distinguish `ctrl+shift+p` from `ctrl+p` (both emit 0x10) —
   there, our binding simply never matches (no false trigger; `ctrl+p` still
   reaches the shell as readline previous-history). `alt+shift+p` is the
   reliable fallback, consistent with the existing alt+shift layer
   (`command_history=alt+shift+i`, `rename_pane=…,alt+shift+r`,
   `redraw=alt+shift+l`, splits on `alt+shift+h/v`) and collides with nothing
   bound today. The exact `msg.String()` form of both is verified at
   implementation time with the debug key trace (`[logging] level="debug"`).

3. **Pane-scoped commands act on the ACTIVE pane** — identical to pressing the
   keybinding. The palette has no "target pane under cursor" concept (that is
   the context menu's job). Navigation commands (`Go to`, `Switch tab`) are the
   only ones that change which pane/tab is active.

4. **v1 command surface** (all dispatch to existing handlers):
   - **Navigation (dynamic):** `Go to: <tab> / <pane>` for every pane across
     every tab; `Switch to tab: <n>:<name>` for every tab.
   - **Layout / pane (active pane):** Split horizontal, Split vertical, Toggle
     focus mode, Toggle notes, Rename pane, Mute/Unmute pane, Toggle eager
     restore, Input history (gated), Open lazygit (gated), Restart pane…, Close
     pane….
   - **Tab:** New tab, Close tab…, Rename tab, Cycle tab color.
   - **Create:** **New pane…** (opens the standard create-pane dialog, = Ctrl+N).
   - **System:** Settings, Shortcuts, Plugins, Memory, About, View client log,
     View daemon log, View MCP logs, Force redraw, Stop daemon….

5. **Deferred to Phase 2 (explicitly OUT of v1):**
   - **Per-plugin `New: <plugin>` and per-instance `Create: <plugin> →
     <instance>`.** These require faithfully reproducing the create-pane
     dialog's multi-branch plugin-selection transition (form fields / saved
     instances / setup toggles / CWD prompt / dialog width). That coupling is
     not worth the risk of spawning panes with skipped required setup in a first
     PR. The single "New pane…" entry preserves the "I can create from here"
     discoverability without the coupling.
   - **Content search (`/` prefix)** across pane buffers — its own feature
     (alt-screen-vs-scrollback split, daemon-side search). The renderer reserves
     a mode-prefix concept so it can be added without a rewrite.
   - **`:` direct-command mode** — redundant with fuzzy in v1.
   - **MRU (most-recently-used) ordering**, session or persisted. v1 shows a
     fixed, curated registry order when the query is empty.

## Architecture

TUI-only change. No daemon, IPC schema, or persistence changes. One config
default is added (`command_palette` keybinding). Everything else lives in
`internal/tui/`.

### State

A cohesive `paletteState` value on `Model` (mirrors `ctxMenu`), zero value =
closed:

```go
type paletteState struct {
    open     bool            // set alongside m.dialog = dialogCommandPalette
    query    string          // rune-aware query buffer
    cursor   int             // index into filtered; clamped to [0, len-1]
    commands []paletteCommand // full registry, rebuilt on open
    filtered []paletteCommand // commands matching query, best score first
}
```

`m.dialog == dialogCommandPalette` is the single source of truth for "open"
(drives input routing + rendering); `paletteState.open` is a convenience mirror
kept in lockstep by the open/close methods so tests and helpers do not have to
reach through `m.dialog`.

### Command model — typed action + arg (no closures)

Following the context menu's typed-dispatch pattern (not closures — closures
capturing a value-receiver `Model` are awkward and untestable here):

```go
type paletteAction int
const (
    palActNone paletteAction = iota
    palActGoToPane      // arg = paneID
    palActSwitchTab     // arg = tabID
    palActSplitH, palActSplitV
    palActFocus, palActNotes, palActRenamePane
    palActMute, palActEager, palActHistory, palActLazygit
    palActRestartPane, palActClosePane
    palActNewTab, palActCloseTab, palActRenameTab, palActCycleTabColor
    palActNewPane
    palActSettings, palActShortcuts, palActPlugins, palActMemory, palActAbout
    palActClientLog, palActDaemonLog, palActMCPLog
    palActRedraw, palActStopDaemon
)

type paletteCommand struct {
    action   paletteAction
    label    string   // display text ("Split horizontal", "Go to: 2:Build / backend")
    detail   string   // right-aligned muted hint (shortcut via kbDisplay, or pane type)
    keywords []string // extra fuzzy targets ("horizontal","hsplit")
    enabled  bool     // greyed + not executable when false (history/lazygit gates)
    arg      string   // navigation target id; empty for static commands
}
```

A single `arg` covers every v1 dynamic command (navigation only takes one id).
No two-parameter command exists in v1 (per-instance create is Phase 2).

### Files

| File | Change |
|------|--------|
| `internal/tui/palette.go` | **New** — `paletteState`, `paletteAction`, `paletteCommand`, `buildPaletteCommands`, fuzzy scorer, filter, `renderCommandPalette`, open/close/key/dispatch methods |
| `internal/tui/palette_test.go` | **New** — fuzzy scorer + build + render pure-function tests |
| `internal/tui/palette_integration_test.go` | **New** — open/filter/navigate/execute via `Update` |
| `internal/tui/model.go` | `dialogCommandPalette` iota value; `palette paletteState` field; `command_palette` case in `handleKey`; extract the few inline handlers listed below |
| `internal/tui/dialog.go` | `dialogCommandPalette` cases in `handleDialogKey` and `renderDialog`; Shortcuts-help row |
| `internal/config/config.go` | `CommandPalette` field + `"ctrl+shift+p,alt+shift+p"` default |
| `internal/config/config_test.go` | default-value assertion |

## Components

### 1. Command registry — `buildPaletteCommands(m) []paletteCommand`

Rebuilt on every open (dynamic entries reflect current tabs/panes/gates).
Assembly order defines the empty-query display order (curated: navigation first,
then pane, tab, create, system):

- **Navigation:** iterate `m.tabs` → `tab.Leaves()`; one `Go to` per pane
  (`label = "Go to: <n>:<tabName> / <paneDisplayName>"`, `detail = pane.Type`,
  `arg = pane.ID`). One `Switch to tab` per tab. The currently-active pane is
  still listed (executing it is a harmless no-op focus) for a complete map.
- **Pane actions:** static entries; `detail` = `kbDisplay(kb.X)` so the shortcut
  shows and teaches. Mute label reflects `activePane.Muted`; eager reflects
  `activePane.Eager`. `Input history` `enabled` iff the active pane's plugin has
  `Command.RecordHistory`; `Open lazygit` `enabled` iff the lazygit plugin is
  `Available` — same gates the context menu uses.
- **Tab actions, Create, System:** static entries with `kbDisplay` details where
  a binding exists.

Keywords give fuzzy aliases (e.g. Split horizontal → `["hsplit","horizontal"]`,
Settings → `["config","preferences"]`).

### 2. Fuzzy matching — pure functions

```go
// fuzzyScore reports whether query is a case-insensitive subsequence of target
// and, if so, a score where higher is better. Rewards: consecutive runs, match
// at target start, match right after a separator (space/:/-/./_), and earlier
// overall position. Empty query → (0, true) (everything passes).
func fuzzyScore(query, target string) (score int, matched bool)

// commandScore returns the best fuzzyScore of query against the command's label
// and each keyword; matched iff any target matches.
func commandScore(query string, c paletteCommand) (score int, matched bool)
```

`filterPalette(query, commands)` keeps matched commands, sorts by score
descending with a **stable** sort so equal scores preserve registry order
(navigation-before-system grouping survives). Empty query returns all commands
in registry order. Target ≤16 ms/keystroke is trivial at this list size (linear
scan of ~40 static + N panes).

### 3. Rendering — `renderCommandPalette(m) string`

Returns box CONTENT; `renderDialog` wraps it in `dialogBorder` and centers via
`lipgloss.Place` (inherited, no new placement code). Layout:

```
> split hor▏
                              (blank)
  Split horizontal            alt+shift+h
  Split vertical              alt+shift+v
  Close pane…                 ctrl+w
                              (blank)
  ↑↓ Navigate   ⏎ Run   Esc Close
```

- Width: `clamp(longest(label+gap+detail)+padding, 50, m.width-4)`.
- Query row: `"> " + query + caret`, caret `▏` (matches `dialogEditStyle` idiom).
- Up to `paletteVisibleRows` (12) result rows; if `len(filtered) >` that, a
  window scrolls to keep `cursor` visible with a `… (+k more)` affordance.
- Each row: label left, detail right-aligned in a muted style; cursor row
  reverse-video; disabled rows greyed (reuse the ctx-menu disabled style).
- Empty `filtered` → a single greyed `No matching commands` row; Enter is inert.

### 4. Open / close — `internal/tui/palette.go`

```go
func (m *Model) openCommandPalette()          // build, filter(""), cursor 0, m.dialog = dialogCommandPalette
func (m *Model) closeCommandPalette()          // m.dialog = dialogNone; zero paletteState
```

Open is bound in `handleKey`'s main switch: `case kbMatches(key,
kb.CommandPalette): m.openCommandPalette(); return m, tea.ClearScreen`. Placed
alongside the other dialog-openers (near `kb.Redraw`). Not added to
`notesKeyExempt` — the palette is unavailable in notes mode (its actions
restructure the layout under the editor), matching `quick_actions`'s no-op there.

### 5. Input routing — `handleCommandPaletteKey(msg) (tea.Model, tea.Cmd)`

Registered in `handleDialogKey`'s switch. Branches on `msg.String()`:

- `esc` → `closeCommandPalette()`.
- `enter` → execute `filtered[cursor]` if enabled, else no-op.
- `up` / `ctrl+p` → cursor− (clamp 0). `down` / `ctrl+n` → cursor+ (clamp len−1).
- `backspace` → trim last rune of query, re-filter, clamp cursor.
- printable (`msg.Text != ""`, appended rune-safe; `space` → `" "`) → append,
  re-filter, reset cursor to 0.
- everything else → swallowed (dialog is input-capturing, as all dialogs are).

Re-filter recomputes `filtered` and clamps `cursor` into range.

### 6. Dispatch — `executePaletteCommand(cmd) (tea.Model, tea.Cmd)`

Close the palette first (`m.dialog = dialogNone` + zero state) so handlers that
open their own dialog (close/restart confirm, create pane, settings) land
cleanly. Then `switch cmd.action`:

- **Navigation:** `palActGoToPane` → `findPaneAndTab(arg)`; set `m.activeTab`,
  clear old active pane's `.Active`, `tab.ActivePane = arg`, target `.Active =
  true`, return `switchTab(idx)` cmd (daemon stays authoritative — mirrors the
  ctx-menu focus + `setActivePaneMsg` logic). `palActSwitchTab` → find idx by
  tabID, `switchTab(idx)`.
- **Reuse existing methods** for the rest — the map of action → handler:

  | action | handler |
  |---|---|
  | SplitH/SplitV | `m.splitPane(SplitHorizontal/Vertical)` (cmd) |
  | Focus | `m.toggleFocusForActiveTab()` |
  | Notes | `m.toggleNotesMode()` |
  | RenamePane | `m.beginPaneRename()` |
  | Mute | `m.toggleActivePaneMute()` (cmd) |
  | Eager | `m.toggleActivePaneEager()` (cmd) |
  | History | `m.openHistoryForActivePane()` |
  | Lazygit | `m.handleToggleLazygit()` (cmd) |
  | RestartPane | `m.openRestartPaneConfirm()` |
  | ClosePane | `m.openClosePaneConfirm()` |
  | NewTab | `m.createTab()` (cmd) |
  | CycleTabColor | `m.cycleTabColor()` (cmd) |
  | CloseTab | `m.openCloseTabConfirm()` *(extract)* |
  | RenameTab | `m.beginTabRename()` *(extract)* |
  | NewPane | `m.openCreatePaneDialog()` *(extract)* |
  | Memory | `m.openMemoryDialog()` + `m.refreshMemory()` cmd |
  | Redraw | `m.redrawCmd()` *(extract)* |
  | StopDaemon | `m.openShutdownConfirm()` *(extract)* |
  | Settings/Shortcuts/Plugins/About | `m.dialog = dialogX` (trivial, inline — no logic to share) |
  | ClientLog/DaemonLog | `m.openLogViewer(label, path)` |
  | MCPLog | `m.openMCPLogsViewer()` |

  Handlers returning bare `tea.Cmd` are wrapped `return m, m.x()`; handlers
  returning `(tea.Model, tea.Cmd)` are returned directly.

### 7. Handler extraction (pure refactor)

The inline case bodies the palette reuses are extracted verbatim into named
`Model` methods in place, and the keybinding cases become one-line delegations
(zero behavior change — same technique as the context-menu PR). New methods:
`openCloseTabConfirm`, `beginTabRename`, `openCreatePaneDialog`, `redrawCmd`,
`openShutdownConfirm`. Trivial `m.dialog = dialogX` openers (settings, shortcuts,
plugins, about) are NOT extracted — there is no logic to duplicate.

## Error handling

- **Empty query** → all commands shown (registry order), cursor 0.
- **No match** → one greyed "No matching commands" row; Enter inert.
- **Disabled command executed** (unreachable — cursor lands only on filtered
  rows, which may include a disabled entry) → `executePaletteCommand` no-ops on
  `!cmd.enabled` as a backstop.
- **`Go to` target vanished** between open and execute (daemon reconciliation) →
  `findPaneAndTab` returns nil; dispatch no-ops without deref.
- **Window resize while open** → the palette is size-agnostic (centered, rebuilt
  width on render); no close needed, unlike the anchored context menu. `View()`
  re-centers on the new size automatically.
- **Cursor bounds** — every query mutation re-clamps `cursor` into
  `[0, len(filtered)-1]` (or 0 when empty).

## Testing

Pure functions (table-driven, `t.Parallel()`, white-box `package tui`):

- `fuzzyScore` — subsequence match/no-match; empty query passes; consecutive >
  scattered; prefix > mid; separator-boundary bonus; case-insensitivity.
- `commandScore` — best-of label+keywords; keyword-only match; no-match.
- `filterPalette` — empty query returns all in order; ranking order; stable ties
  preserve registry order; cursor-safe on shrink.
- `buildPaletteCommands` — count/labels/gates against `newSplitDragTestModel`
  (history + lazygit disabled without registry; navigation entries per pane;
  mute/eager label reflects active pane state).
- `renderCommandPalette` — every rendered line is exactly the box width;
  cursor-row reverse; disabled-row grey; empty-result row; scroll window keeps
  cursor visible.

Integration (drive `Update` / handlers on `newSplitDragTestModel`):

- `command_palette` key opens `dialogCommandPalette`; Esc closes.
- Typing filters (`msg.Text`); backspace restores; cursor resets on query
  change; up/down + ctrl+n/ctrl+p move and clamp.
- Enter on `Go to: p2` sets `activeTab`/`ActivePane` to the target (cross-tab)
  and issues a switch-tab cmd.
- Enter on `Close pane…` closes the palette and arms the pane close confirm for
  the active pane.
- Enter on `Split horizontal` returns the split cmd; on `Mute` toggles the
  active pane.
- Disabled `Input history` (no registry) is inert on Enter.
- Extraction refactor is behavior-neutral: full `./internal/tui/` suite stays
  green with no test changes.
- `config_test.go`: `CommandPalette` default == `"ctrl+shift+p,alt+shift+p"`.

## Verification corrections (applied after subagent review)

A codebase-verification pass confirmed the architecture and every reused handler
signature, and corrected these points — all folded into the implementation plan:

1. **Notes-mode guard is explicit, not implicit.** `quick_actions` is a no-op in
   notes mode because `openQuickActionsMenu` has an `if m.notesMode { return }`
   guard — NOT because of `notesKeyExempt` (which only governs the
   editor-focused path; with the pane focused in notes mode the key reaches the
   main switch). `openCommandPalette` therefore carries its own
   `if m.notesMode { return m, nil }` guard.
2. **`Stop daemon` dropped from v1.** It is not in `handleKey` — it lives in
   `handleAboutKey` (F1 → About → row 8) with an Esc-return-to-About coupling.
   Wiring it into the palette cleanly is disproportionate risk for a rare
   destructive action still reachable via F1. Deferred to Phase 2.
3. **Memory dispatch is two statements:** `openMemoryDialog()` returns a bare
   `Model` (not a cmd), so `m = m.openMemoryDialog(); return m, m.refreshMemory()`.
4. **Trivial dialog openers reset the cursor:** Settings/Shortcuts/Plugins/About
   set `m.dialogCursor = 0` alongside `m.dialog = dialogX` (those dialogs render
   their selection from `dialogCursor`; a stale value would land on a wrong row).
5. **Box width is set in `renderDialog`'s case** (a `paletteWidth` constant,
   clamped to `m.width-2` as `renderDialog` already does), not inside
   `renderCommandPalette`; the content renderer uses the same clamped width for
   right-aligning the detail column.
6. **`paletteState.open` mirror dropped** — `m.dialog == dialogCommandPalette` is
   the sole open/closed authority (mirrors how `ctxMenu` needs no such flag).
7. **Cross-tab focus ordering is load-bearing:** clear the previously-active
   tab's active pane `.Active` flag BEFORE switching `m.activeTab`, then set the
   target tab's `ActivePane` + target pane `.Active`, then `switchTab(idx)`.
8. **Caret glyph** is `│` (U+2502) via `dialogEditStyle`, matching the existing
   input idiom; **`redrawCmd`** is extracted as `forceRedraw() (tea.Model,
   tea.Cmd)` because it mutates `m.tabs` (render-cache invalidation) before
   returning the batch — it cannot be a bare `func() tea.Cmd`.

## Out of scope (v1)

- Per-plugin / per-instance quick-create (Phase 2 — see Decision 5).
- Stop daemon command (see correction 2; use F1 → About).
- Cross-pane content search `/` (Phase 2, separate feature).
- `:` direct-command mode.
- MRU ordering (session or persisted).
- Mouse interaction inside the palette (keyboard-first; click-to-run is a cheap
  Phase 2 add via `renderDialog` hit-testing if wanted).
- Palette availability inside notes mode.
- Fuzzy-searching terminal output / symbols.
