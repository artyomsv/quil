# Keybindings

Quil's full keymap. Every binding is configurable via `~/.quil/config.toml` under `[keybindings]` — see [Configuration](configuration.md#keybindings) for the override syntax.

## Table of contents

- [Quick reference](#quick-reference)
- [Tabs](#tabs)
- [Panes](#panes)
- [Pane navigation](#pane-navigation)
- [Notes editor](#notes-editor)
- [Notification sidebar](#notification-sidebar)
- [Clipboard](#clipboard)
- [Text selection](#text-selection)
- [Scrolling](#scrolling)
- [Dialogs (F1 menus)](#dialogs-f1-menus)
- [Keys that pass through to the PTY](#keys-that-pass-through-to-the-pty)

---

## Quick reference

The five keys you'll use most:

| Key | Action |
|---|---|
| `F1` | About menu → Settings, Plugins, Memory, log viewers |
| `Ctrl+N` | New typed pane (Claude Code, OpenCode, terminal, …) |
| `Ctrl+T` | New tab |
| `Ctrl+W` | Close active pane |
| `Ctrl+Q` | Quit |

## Tabs

| Key | Action |
|---|---|
| `Ctrl+T` | New tab |
| `Alt+W` | Close active tab |
| `F2` | Rename active tab |
| `Alt+C` | Cycle tab colour (8 colours) |
| `Alt+1` … `Alt+9` | Switch directly to tab 1–9 |
| Mouse click on tab | Switch to that tab |
| Mouse drag a tab | Reorder — intermediate tabs slide one slot at a time, dragged tab follows the cursor |

The active tab is prefixed with `* ` in the tab bar so it's visible even when [tab colors](configuration.md#keybindings) override the bold weight.

## Panes

| Key | Action |
|---|---|
| `Ctrl+N` | New typed pane (plugin picker dialog) |
| `Ctrl+W` | Close active pane (with confirm) |
| `Alt+Shift+H` | Split side-by-side |
| `Alt+Shift+V` | Split top/bottom |
| `Alt+F2` / `Alt+Shift+R` | Rename active pane. `Alt+Shift+R` is a macOS-friendly fallback since `F2` is often eaten by the OS and `Option` is not always passed through as Meta. |
| `Ctrl+E` | Toggle focus mode (active pane full-screen) |
| `Alt+G` | Toggle lazygit overlay (git repo from active pane's directory) |
| `Alt+Shift+L` | Force a full screen redraw — clears rendering artifacts (scrambled/misplaced characters) without restarting. Mnemonic: `Ctrl+L` redraws a shell. |

## Pane navigation

| Key | Action |
|---|---|
| `Alt+Left` / `Right` / `Up` / `Down` | Focus the closest neighbour in that direction (spatial, tmux-style) |
| `Alt+Backspace` | Jump back through pane visit history (browser back) |

Linear pane cycling (`Tab` / `Shift+Tab`) is **not** bound by default — see [Keys that pass through](#keys-that-pass-through-to-the-pty).

You can bind `next_pane` / `prev_pane` in `config.toml` if you prefer linear cycling alongside the spatial keys.

## Notes editor

| Key | Action |
|---|---|
| `Alt+E` | Toggle pane notes (split ~60/40 with the bound pane) |
| `Ctrl+S` | Save notes immediately (in addition to 30 s autosave) |
| `Tab` / `Shift+Tab` | Cycle keyboard focus between editor and bound pane |
| `Esc` | Clear selection (first press) / exit notes mode (second press) |

## Notification sidebar

| Key | Action |
|---|---|
| `Alt+N` | Cycle sidebar visibility: hidden → visible+unfocused → visible+focused → hidden |
| `F3` | Focus the notification sidebar (when visible) |
| `Alt+M` | Mute / unmute notifications for the active pane. Muted panes show `[muted]` on the border and never fire process-exit, bell, OSC 133, or idle events. Useful for `npm test --watch` and other chatty processes. |
| `Alt+Shift+E` | Toggle eager restore on the active pane. Eager panes respawn immediately on daemon restart instead of loading lazily on tab open; marked with `●` on the tab. |

## Clipboard

| Key | Action |
|---|---|
| `Ctrl+V` | Paste from clipboard (text or image) |
| `Ctrl+Alt+V` | Paste alias — useful when Windows Terminal eats `Ctrl+V` |
| `F8` | Paste alias — **recommended on Windows** because Windows Terminal never delivers `Ctrl+V` to the TUI |

If the clipboard has no text but contains an image, Quil decodes the DIB, saves a PNG under `~/.quil/paste/`, and types the absolute path into the active pane. See [Image paste](features.md#image-paste-from-clipboard).

## Text selection

| Key | Action |
|---|---|
| `Shift+Arrow` | Extend selection by character |
| `Ctrl+Shift+Arrow` | Extend selection by word |
| `Ctrl+Alt+Shift+Arrow` | Extend selection by 3 words |
| `Shift+Home` / `Shift+End` | Extend to line start / end |
| `Ctrl+A` (in editors) | Select all |
| `Enter` | Copy selection to clipboard |
| Mouse click + drag | Visual selection (terminals + editors) |

## Scrolling

| Key | Action |
|---|---|
| `Alt+PgUp` / `Alt+PgDown` | Scroll the pane scrollback by `[ui] page_scroll_lines` (0 = half-page) |
| Mouse wheel | Scroll by `[ui] mouse_scroll_lines` (default 3) |
| Click on scrollbar | Jump the scrollbar thumb to that Y position (rightmost content column of the pane) |
| Click + drag on scrollbar | Continuous scroll — drag follows cursor Y, even off-pane |
| `Alt+Up` / `Alt+Down` *(in log viewer)* | Jump cursor by `[ui] log_viewer_page_lines` (default 40) |

## Dialogs (F1 menus)

| Key | Action |
|---|---|
| `F1` | Open About menu |
| `↑` / `↓` (or `k` / `j`) | Move cursor |
| `Enter` | Activate / open child |
| `Esc` | Back / close |
| `y` | Confirm shutdown on **Stop daemon** confirm (deliberately not `Enter`) |
| `n` / `Esc` | Cancel confirm |

## Keys that pass through to the PTY

These are deliberately unbound at the TUI level so they reach the running pane process:

- **`Tab` / `Shift+Tab`** — shell tab-completion, Claude Code mode-cycling, opencode picker navigation
- **Most printable characters** — type into the shell/REPL

Plugins can declare additional pass-through keys via `raw_keys = [...]` in their TOML — see the [plugin reference](plugin-reference.md#raw-keys).

If you'd rather have `Tab` cycle panes, bind it in `config.toml`:

```toml
[keybindings]
next_pane = "tab"
prev_pane = "shift+tab"
```

…but you'll lose the PTY tab-completion you usually want.

## Customizing keybindings

Every binding listed above corresponds to a key in `~/.quil/config.toml` under `[keybindings]`. See [Configuration](configuration.md#keybindings) for the full list of overridable bindings.
