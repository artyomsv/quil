# Keybindings

Quil's full keymap. Every binding is configurable in `~/.quil/bindings.toml`, either by selecting a preset or by overriding individual actions — see [Customizing keybindings](#customizing-keybindings) below and [Configuration](configuration.md#bindingstoml) for the file reference.

## Table of contents

- [Quick reference](#quick-reference)
- [Projects](#projects)
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
| `Alt+Shift+P` | Command palette — fuzzy-find any action or jump to any pane/tab |
| `F1` | About menu → Settings, Plugins, Processes, log viewers |
| `Ctrl+N` | New typed pane (Claude Code, OpenCode, Codex, terminal, …) |
| `Ctrl+T` | New tab — asks which pane it opens with (`Esc` cancels, creating nothing) |
| `Ctrl+W` | Close active pane |
| `Ctrl+Q` | Quit |

## Projects

A project owns a set of tabs. Switching project switches the whole tab bar, and
each project remembers the tab you left it on. Every tab belongs to exactly one
project, so `Ctrl+T` files the new tab into whichever project you are on, and
the pane it opens with starts in that project's root directory.

| Key | Action |
|---|---|
| `Alt+Shift+N` | New project |
| `Alt+Shift+X` | Destroy the active project (confirms first) |
| `Alt+P` | Project picker — fuzzy-find by name |
| `Alt+O` | Bounce between the two most recent projects |
| `Alt+Shift+Right` | Next project (wraps) |
| `Alt+Shift+Left` | Previous project (wraps) |
| `Alt+Shift+A` | Attention queue — jump to the oldest pane waiting on you, across every project |
| `Alt+Shift+Up` / `Alt+Shift+Down` | Move the active project one slot up / down in the sidebar |
| `Alt+Shift+S` | Collapse / expand the project sidebar |
| Mouse click a sidebar row | Switch to that project, switch to that tab, or raise that pane |
| Mouse drag a project row | Reorder — the row moves once the pointer passes the middle of its neighbour (a remote project is two rows tall). Each daemon remembers the order of its own projects; when several hosts are connected, how their rows interleave is kept only for the running session |
| Click + drag the sidebar's right edge | Resize the sidebar (12-column minimum; a rule previews the new edge, panes resize on release, width persists to `[ui] sidebar_width`) |

The sidebar is the reserved left column listing projects and the active
project's panes. Each project row carries a badge counting its agents: `⠹N`
while N panes are working — the same spinner the tab bar and the pane border
run, so one glance says "still going" — and `▲N` when N are blocked waiting on
you. Those counts
keep updating for projects in the **background**, which is what makes
`Alt+Shift+A` useful — it crosses the project boundary to reach whichever pane
has been waiting longest.

Each pane row carries the same vocabulary for one pane: `⠹` working, spinning
(with `⋯N`
when N subagents are still running), `▲` blocked on you (with the tool it is
waiting on, when the hook reported one), `✓` finished and not yet looked at,
`○` idle.

A project row can also carry a link marker, which is about the **connection**
rather than the agents: `⟳` while that daemon is reconnecting, `⚡` when its
reconnect is parked and it will not retry until you resume it (`r`). Only a
remote project can show either.

Unlike the notification sidebar (`Alt+N`), which draws *over* the pane area,
this one reserves real layout width — so toggling it resizes every pane's PTY.
Its width and whether it starts open are
[`[ui] sidebar_width` / `sidebar_open`](configuration.md#ui).

`Alt+P` and `Alt+O` are plain Alt-letter keys because no AI tool binds them; the
rest take the Alt+Shift layer for the same reason the split keys do. The group
deliberately avoids `Alt+W`, `Alt+A` and `Alt+Shift+P` (close tab, quick
actions, command palette).

## Tabs

| Key | Action |
|---|---|
| `Ctrl+T` | New tab — asks which pane it opens with (`Esc` cancels, creating nothing) |
| `Alt+W` | Close active tab |
| `F2` | Rename active tab |
| `Alt+C` | Cycle tab colour (8 colours) |
| `Alt+1` … `Alt+9` | Switch directly to tab 1–9 |
| `Alt+Shift+PgUp` / `Alt+Shift+PgDn` | Move the active tab one slot left / right |
| Mouse click on tab | Switch to that tab |
| Mouse drag a tab | Reorder — the tab moves once the pointer passes the middle of a neighbour, so a narrow tab dragged over a wide one never flips back and forth; intermediate tabs slide one slot at a time |
| Mouse click / drag a tab name in the project sidebar | Switch to that tab / reorder it — drag the name up or down past the middle of another tab's group of rows |

The active tab is prefixed with `* ` in the tab bar so it's visible even when [tab colors](configuration.md#keybindings) override the bold weight.

## Panes

| Key | Action |
|---|---|
| `Ctrl+N` | New typed pane (plugin picker dialog) |
| `Ctrl+W` | Close active pane (with confirm). If Quil created a worktree for the pane, the confirm offers to delete it too — `space` arms the row, and it is off every time the dialog opens. The branch is kept; uncommitted work in the worktree is not. |
| `Alt+R` | Restart active pane's process in place (with confirm). The process is killed and respawned with the plugin's resume strategy, so AI panes (Claude Code, OpenCode, Codex) resume their recorded session. Use this when a pane shows the "Pane not accepting input" warning. |
| `Alt+Shift+H` | Split side-by-side |
| `Alt+Shift+V` | Split top/bottom |
| `Alt+F2` / `Alt+Shift+R` | Rename active pane. `Alt+Shift+R` is a macOS-friendly fallback since `F2` is often eaten by the OS and `Option` is not always passed through as Meta. |
| `Ctrl+E` | Toggle focus mode (active pane full-screen) |
| `Alt+Shift+W` | Toggle the active AI pane's preview between left-edge crop (default) and soft-wrap. Only affects `wide_canvas` panes rendered smaller than the window. |
| `Alt+G` | Toggle lazygit overlay (git repo from active pane's directory) |
| `Alt+D` | Toggle hunk overlay — diff review for the same repo. Shares the tab's single overlay slot with lazygit, so pressing it while lazygit is on screen swaps tools. Mnemonic: **d**iff. Not `Alt+H`, because plain `Alt+H` is deliberately left unbound so it reaches the running program (see the passthrough note below) and because vim-style layouts rebind it to pane-left — set `toggle_hunk = "alt+h"` if you prefer it there. |
| `Alt+Shift+L` | Force a full screen redraw — clears rendering artifacts (scrambled/misplaced characters) without restarting. Mnemonic: `Ctrl+L` redraws a shell. |
| `Alt+Shift+I` | Open the active pane's input history — one row per prompt you submitted, newest first. `↑/↓` navigate, `PgUp/PgDn/Home/End` jump, `Enter` opens the full text in a soft-wrapped read-only viewer (drag or `Ctrl+A` to select, right-click or `Enter` to copy), `Esc` closes. Only AI panes whose plugin sets `record_history` (Claude Code) capture history; other pane types show an empty state. |
| `Alt+A` | Open the pane context menu for the active pane (`quick_actions`). Same menu as right-click — see [Mouse: pane context menu](#mouse-pane-context-menu) below. |

### Mouse: pane context menu

Right-click on a pane — with a text selection active, it copies the selection to the clipboard (unchanged); with no selection, it opens the pane context menu for the pane under the cursor. `↑`/`↓` (or `k`/`j`) navigate, `Enter` or a left-click executes the highlighted item, `Esc` or a click outside the menu closes it, and right-clicking another pane re-targets the menu to it. `Ctrl+Q` still quits while the menu is open.

Right-clicking a **pane row in the project sidebar** opens the same menu. It focuses that pane first — switching tabs if the pane lives on another one, exactly as a left-click on the row does — so every action in the menu applies to the pane you clicked.

## Pane navigation

| Key | Action |
|---|---|
| `Alt+Left` / `Right` / `Up` / `Down` | Focus the closest neighbour in that direction (spatial, tmux-style) |
| `Alt+Backspace` | Jump back through pane visit history (browser back) |

Linear pane cycling (`Tab` / `Shift+Tab`) is **not** bound by default — see [Keys that pass through](#keys-that-pass-through-to-the-pty).

You can bind `next_pane` / `prev_pane` in `config.toml` if you prefer linear cycling alongside the spatial keys.

### Word-jump inside a pane (macOS)

On Windows/Linux, `Ctrl+Left` / `Ctrl+Right` jump by word — Quil forwards them to the pane's
child (claude-code, shell). macOS **Terminal.app** emits no distinct sequence for `Ctrl+Arrow`,
but with **Use Option as Meta key** enabled (Settings → Profiles → Keyboard), `Option`-combos
arrive as Meta keys. Quil forwards any `Alt+<key>` to the pane as `ESC+<key>` (the standard Meta
encoding), so the readline word-jump keys work out of the box with **no configuration**:

- `Option+B` → backward one word
- `Option+F` → forward one word

(These map to `ESC-b` / `ESC-f`, which claude-code and shell readline bind to word navigation.)
Terminal.app has no distinct combo for a multi-word "fast jump" (`Option+Shift+Arrow` collapses
to `Option+Arrow` and `Cmd` is reserved by macOS), so that remains available only on
Kitty-protocol terminals (Ghostty, WezTerm, iTerm2).

Note: `Alt+A` (`Option+A` under Option-as-Meta) is bound to `quick_actions` — opening the pane
context menu — so `ESC-a` (emacs `M-a`, backward-sentence) no longer reaches the PTY. This is
deliberate and consistent with the other single-letter Alt-layer bindings Quil already
intercepts (`Alt+G`, `Alt+M`, `Alt+N`, `Alt+E`, …); rebind `quick_actions` in `config.toml` if
you rely on `M-a` in a pane's readline.

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
| Click + drag a split border | Resize the adjacent panes (10×4 minimum; PTY resize applied on release) |

## Scrolling

| Key | Action |
|---|---|
| `Alt+PgUp` / `Alt+PgDown` | Scroll the pane scrollback by `[ui] page_scroll_lines` (0 = half-page) |
| Mouse wheel | Scroll by `[ui] mouse_scroll_lines` (default 3) |
| Click on scrollbar | Jump the scrollbar thumb to that Y position (rightmost content column of the pane) |
| Click + drag on scrollbar | Continuous scroll — drag follows cursor Y, even off-pane |
| `Alt+Up` / `Alt+Down` *(in log viewer)* | Jump cursor by `[ui] log_viewer_page_lines` (default 40) |

## Command palette

| Key | Action |
|---|---|
| `Alt+Shift+P` | Open the command palette (`command_palette`) |
| Type | Fuzzy-filter the list (matches labels + keywords) |
| `↑` / `↓` (or `Ctrl+P` / `Ctrl+N`) | Move the selection (section headers are skipped) |
| `Enter` | Run the highlighted command |
| `Esc` | Close |

The palette is a modal launcher for **every** action plus jump-to-tab and
jump-to-pane. Entries are grouped under dim section headers — **Go to pane**,
**Tabs**, **Projects**, **Pane**, **System**, **Appearance** — navigation first;
headers disappear once you start typing. Each row shows its shortcut, so the
palette teaches the bindings as you go, and it dispatches into the same handlers
the keybindings use.

As you type, the palette also runs a **content search** across every pane's
scrollback (literal, case-insensitive) — matching panes appear in a **Found in
panes** section below the filtered commands, each with a match count and a preview
line. Arrow to one and press Enter to jump to that pane; Enter on a command still
runs the command. There is no separate mode or prefix — commands and pane matches
share one list, narrowing together as you type. Search reads each pane's loaded
output buffer and never wakes a dormant pane; with lazy pane restore, a pane you
haven't opened yet this session may not appear until you visit it.

The default is `Alt+Shift+P` because `Ctrl+Shift+P` (the VS Code key) is grabbed
by many terminals' own command palette — Windows Terminal, VS Code's integrated
terminal — before Quil sees it. If your terminal leaves it free, add it back:
`command_palette = "ctrl+shift+p,alt+shift+p"`. Unavailable while the notes
editor is open.

## Dialogs (F1 menus)

| Key | Action |
|---|---|
| `F1` | Open About menu |
| `↑` / `↓` (or `k` / `j`) | Move cursor |
| `Enter` | Activate / open child |
| `Esc` | Back / close |
| `PgUp` / `PgDn`, `Home` / `End`, `g` / `G` | Scroll the **Shortcuts** list |
| `y` | Confirm shutdown on **Stop daemon** confirm (deliberately not `Enter`) |
| `n` / `Esc` | Cancel confirm |

**F1 → Shortcuts** lists every binding, including the project keys, and scrolls: the footer shows your position (`12-30 of 62`) so a clipped list is never mistaken for the whole one.

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

Bindings live in `~/.quil/bindings.toml`, keyed by **action ID** rather than by config field name:

```toml
preset = "default"

[bindings]
"pane.rename" = "alt+shift+r"
"project.picker" = ""            # explicitly unbound
```

F1 → Shortcuts lists every action ID alongside its current key.

If you are upgrading, your existing `[keybindings]` table in `config.toml` is migrated to this file on first launch. Only settings that differ from the shipped defaults are carried across — a binding you never changed stays free to follow future default changes instead of being frozen at today's value. The old table is left in `config.toml` for one release as a fallback, but it is no longer read.

### Windows paste

`pane.paste` ships as three alternatives — `ctrl+v, ctrl+alt+v, f8`. Windows Terminal captures `Ctrl+V` for its own paste and never delivers it to Quil, and `Ctrl+Alt+V` is ambiguous with AltGr on European layouts. **If you rebind `pane.paste`, keep an `f8` alternative** or Windows users lose the only reliable trigger.

### How a binding is read

Quil resolves keys to **actions**, not to raw config strings. That has a few consequences worth knowing:

- **Modifier names are case-insensitive and order-insensitive.** `Ctrl+Shift+A`, `shift+ctrl+a`, and `ctrl+shift+a` are the same chord. Quil renders it back to you as `ctrl+shift+a`.
- **A single-character key keeps its case.** `alt+m` and `alt+M` are *different* chords. This matters on macOS — see [Word-jump inside a pane](#word-jump-inside-a-pane-macos) below.
- **Named keys are case-insensitive and have aliases.** `escape` = `esc`, `pageup` = `pgup`, `pagedown` = `pgdown` = `pgdn`, `return` = `enter`. `meta` and `hyper` both mean `super` (Cmd on macOS, Win on Windows).
- **Control characters are rejected.** A binding containing an escape sequence or other control character fails to parse, and the action falls back to its shipped default. Only that one binding is affected.
- **F1 shows what Quil actually parsed**, in canonical form. If you wrote `Ctrl+V` and F1 shows `ctrl+v`, that is the same key — Quil is echoing its normalized spelling, not changing your binding.

### When a binding doesn't work

Open **F1 → Shortcuts**. If Quil could not honour a binding, a warning row appears at the top of the list marked with `!`, naming the key, which action won, and what will not fire. Five cases produce one:

| Warning | Meaning |
|---|---|
| `duplicate binding` | Two actions claim the same key. The one listed first in F1 wins. |
| `cross-tier shadowing` | Two actions claim the same key, and one of them is resolved earlier in the dispatch order, so the other can never fire. |
| `collides with a built-in key` | The key is also handled by Quil outside the binding system — `F1`, `Ctrl+N`, the `F8` / `Ctrl+Alt+V` paste aliases, or the text-selection chords (`Shift+Arrow`, `Ctrl+Shift+Left/Right`, `Ctrl+Alt+Shift+Left/Right`). The message names which one wins. |
| `unreadable binding` | The spec did not parse. That action fell back to its default; every other binding is unaffected. |
| `unreachable binding` | One binding is the opening of another — binding both `ctrl+b` and `ctrl+b c` means the first can never fire, because pressing it always waits for a second key. The message names which one survives. |
| `unusable prefix` | A binding uses `${prefix}` but `prefix` is unset, or is not exactly one chord. Those bindings are dropped rather than half-expanded. |

These also go to the client log, so a binding that breaks the F1 dialog itself is still diagnosable.

## Key sequences

A binding can be several keys pressed in order, separated by spaces:

```toml
# ~/.quil/bindings.toml
[bindings]
"tab.new" = "ctrl+b c"
```

Press `Ctrl+B`, then `c`. While Quil is waiting for the second key the status bar shows `ctrl+b…`; `Esc` cancels, and a combination bound to nothing says so rather than doing nothing quietly.

Three things worth knowing:

- **Pressing the opening key twice sends it through to the pane.** With `Ctrl+B` as a sequence opener, `Ctrl+B` `Ctrl+B` delivers one literal `Ctrl+B` to whatever is running. This is what keeps a tmux *inside* a Quil pane reachable — the same escape tmux itself uses.
- **A sequence takes priority over a plugin's `raw_keys`.** If a pane's tool claims `x` and you bind `ctrl+b x`, the sequence wins. Single-chord bindings are unaffected.
- **Sequences are inert wherever something else owns the keyboard** — dialogs, the command palette, an inline rename, the context menu, a full-screen overlay, the notification sidebar, while text is selected, and while the notes editor has focus.

To bind the comma key, write `comma` — a literal `,` separates alternatives:

```toml
"tab.rename" = "ctrl+b comma"
```

## Keymap presets

Set a whole keymap in one line:

```toml
# ~/.quil/bindings.toml
preset = "tmux"
```

**Restart Quil for it to take effect.** `bindings.toml` is read once at startup — there is no in-app preset switcher and no hot reload, so editing the file while Quil is running does nothing until the next launch. `F1` → Shortcuts always lists the keymap that is *currently* live, which is how to confirm a switch actually took.

Switching back is the same line: `preset = "default"`.

**Presets replace, they do not add.** Selecting `tmux` means `Ctrl+T` no longer opens a tab and `Ctrl+W` no longer closes a pane — those keys belong to the tmux layout now. Any action the preset does not mention keeps its usual key: `Alt+Shift+P` still opens the command palette, `Alt+G` still toggles lazygit.

Want one of them back? Override it:

```toml
preset = "tmux"

[bindings]
"tab.new" = "ctrl+b c, ctrl+t"   # both
```

### The tmux preset

For how this lines up against tmux's own default prefix table — including what has no Quil equivalent and why — see [tmux comparison](tmux-comparison.md).

| tmux | Quil action |
|---|---|
| `prefix c` | New tab |
| `prefix ,` | Rename tab |
| `prefix &` | Close tab |
| `prefix n` / `prefix p` | Next / previous tab |
| `prefix 1`–`9` | Switch to tab 1–9 |
| `prefix %` | Split side by side |
| `prefix "` | Split top/bottom |
| `prefix x` | Close pane |
| `prefix z` | Toggle focus mode (tmux zoom) |
| `prefix o` | Next pane |
| `prefix ←↑↓→` | Focus pane in that direction |
| `prefix [` | Scroll page up |
| `prefix d` | Quit — the daemon keeps running, so this is tmux's detach |
| `prefix ?` | Show this shortcut list |

`prefix 0` is deliberately unbound: tmux windows count from 0 and Quil tabs count from 1, so mapping it to tab 1 would double-bind that tab and hide tab 9.

### Changing the prefix

```toml
preset = "tmux"
prefix = "ctrl+a"
```

One line rebinds every `${prefix}` binding in the preset.

**Check F1 after changing it.** A prefix that collides with an inherited default degrades quietly: `prefix = "ctrl+w"` makes every preset sequence shadow the default `Ctrl+W` binding for closing a pane. That is coherent — the longer sequence wins — but the only place it is visible is F1's warning rows.

An unmodified letter as the prefix (`prefix = "a"`) is allowed but warns: it is swallowed globally, before any pane sees it.

### Sequence timeout

Off by default, matching tmux — a pending prefix waits indefinitely. To make it expire:

```toml
sequence_timeout = "500ms"
```
