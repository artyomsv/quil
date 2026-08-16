# tmux comparison

How Quil's `tmux` keymap preset lines up against tmux's own default prefix table.

Quil is a pane and tab manager, not a tmux clone. Most of the gaps below are tmux concepts Quil has no equivalent for, rather than work left undone — the notes under each section say which is which.

## Table of contents

- [Switching between the two keymaps](#switching-between-the-two-keymaps)
- [Coverage at a glance](#coverage-at-a-glance)
- [Windows → tabs](#windows--tabs)
- [Panes](#panes)
- [Copy mode and buffers](#copy-mode-and-buffers)
- [Sessions and client → projects](#sessions-and-client--projects)
- [Everything else](#everything-else)
- [What Quil adds](#what-quil-adds)
- [What is missing and probably shouldn't be](#what-is-missing-and-probably-shouldnt-be)

---

## Switching between the two keymaps

Edit `~/.quil/bindings.toml` (or `$QUIL_HOME/bindings.toml`) and change one line:

```toml
preset = "tmux"      # tmux-style prefix keymap
```
```toml
preset = "default"   # Quil's own single-chord keymap
```

**Then restart Quil.** `bindings.toml` is read once at startup — there is no in-app preset switcher and no hot reload, so a change made while Quil is running has no effect until the next launch. `F1` → Shortcuts always shows the keymap that is *currently* live, which is the quickest way to confirm a switch took.

Two things worth knowing before you flip it:

- **Presets replace, they do not add.** Selecting `tmux` gives up `Ctrl+T` and `Ctrl+W` — those keys belong to the tmux layout now. Any action the preset does not name keeps its usual Quil chord.
- **You do not have to choose wholesale.** `[bindings]` overrides sit on top of whichever preset is selected, so you can run the tmux keymap and keep a handful of Quil chords:

  ```toml
  preset = "tmux"

  [bindings]
  "tab.new" = "${prefix} c, ctrl+t"   # both
  ```

To change the prefix itself, one more line — the preset supplies `ctrl+b`, and this overrides it:

```toml
preset = "tmux"
prefix = "ctrl+a"
```

**Check `F1` → Shortcuts after changing `prefix`.** A prefix that collides with an inherited default degrades quietly: `prefix = "ctrl+w"` makes every preset sequence shadow the default close-pane binding. That is coherent — the longer sequence wins — but the only place it is visible is F1's warning rows.

## Coverage at a glance

| | Count |
|---|---|
| Same key, same meaning | 27 |
| Close analogue on a different key | 12 |
| No Quil equivalent | 23 |

The tmux column throughout is the default prefix table for **tmux 3.x as shipped**. If you have a `.tmux.conf`, your bindings differ and this document says nothing about them.

## Windows → tabs

| tmux | tmux command | Quil | Status |
|---|---|---|---|
| `c` | new-window | `tab.new` | exact |
| `,` | rename-window | `tab.rename` | exact |
| `&` | kill-window | `tab.close` | exact |
| `n` | next-window | `tab.next` | exact |
| `p` | previous-window | `tab.prev` | exact |
| `1`–`9` | select-window 1–9 | `tab.switch_1`–`9` | exact |
| `0` | select-window 0 | *deliberately unbound* | by design |
| `l` | last-window | — | absent |
| `w` | choose-window | Command palette (`Alt+Shift+P`) | analogue |
| `f` | find-window | Command palette (`Alt+Shift+P`) | analogue |
| `.` | move-window | Drag a tab in the tab bar | mouse only |
| `'` | select-window by index prompt | — | absent |
| `M-n` / `M-p` | next / previous window with alert | Attention queue (`Alt+Shift+A`) | analogue |
| `i` | display-message | — | absent |

**Why `prefix 0` is unbound.** tmux windows count from 0; Quil tabs count from 1 and render a 1-based prefix in the tab bar. Mapping `prefix 0` to tab 1 would double-bind that tab and leave tab 9 unreachable for anyone counting from zero, so the preset binds `1`–`9` and stops.

## Panes

| tmux | tmux command | Quil | Status |
|---|---|---|---|
| `%` | split-window -h | `pane.split_h` | exact |
| `"` | split-window -v | `pane.split_v` | exact |
| `x` | kill-pane | `pane.close` | exact |
| `z` | resize-pane -Z (zoom) | `pane.focus_toggle` | exact |
| `o` | select-pane (next) | `pane.next` | exact |
| `←` `↑` `↓` `→` | select-pane -L/-U/-D/-R | `pane.left/up/down/right` | exact |
| `;` | last-pane | Pane history back (`Alt+Backspace`) | analogue |
| `C-←` / `M-←` | resize-pane by 1 / 5 cells | Drag the split border | mouse only |
| `q` | display-panes | — | absent |
| `{` / `}` | swap-pane -U / -D | — | absent |
| `!` | break-pane | — | absent |
| `Space` | next-layout | — | absent |
| `M-1`–`M-5` | select-layout (preset layouts) | — | absent |
| `E` | select-layout -E (spread) | — | absent |
| `C-o` / `M-o` | rotate-window | — | absent |
| `m` / `M` | mark / unmark pane | — | absent |

**The layout cluster is a concept gap, not a to-do.** tmux treats a window's panes as an arrangeable set with named layouts you cycle through. Quil uses a binary split tree that you shape directly, so there is nothing to cycle *to* — `Space`, `M-1`–`M-5`, `E`, `{`/`}` and the rotate keys have no meaning in that model.

## Copy mode and buffers

| tmux | tmux command | Quil | Status |
|---|---|---|---|
| `[` | copy-mode | `pane.scroll_page_up` | approximate |
| `PgUp` | copy-mode -u | Scroll up (`Alt+PgUp`) | analogue |
| `]` | paste-buffer | Paste (`Ctrl+V` / `F8`) | analogue |
| `=` | choose-buffer | — | absent |
| `-` | delete-buffer | — | absent |
| `#` | list-buffers | — | absent |

**`[` is an approximation and the preset does not pretend otherwise.** It scrolls one page up. tmux's copy-mode is a modal editor with selection, search and its own key table; Quil has scrollback plus `Shift`+Arrow selection, which covers similar ground by a different route.

**The buffer keys have nothing to point at.** tmux keeps a named buffer stack; Quil uses the system clipboard, so there is one buffer and nothing to choose from, list, or delete.

## Sessions and client → projects

| tmux | tmux command | Quil | Status |
|---|---|---|---|
| `d` | detach-client | `app.quit` — the daemon keeps running | exact |
| `C-b` | send-prefix | Press the prefix twice | exact |
| `s` | choose-session | Project picker (`Alt+P`) | analogue |
| `(` / `)` | switch-client -p / -n | Previous / next project (`Alt+Shift+←/→`) | analogue |
| `L` | switch-client -l | Bounce project (`Alt+O`) | analogue |
| `$` | rename-session | Project form only | no key |
| `D` | choose-client | — | absent |

**Quitting Quil is tmux's detach.** The daemon keeps every pane running; relaunching reattaches. That is why `prefix d` maps to `app.quit` rather than to something destructive.

**The doubled prefix matters more than it looks.** Quil panes routinely `ssh` into hosts running real tmux. Pressing the prefix twice sends one literal prefix through to the pane, which is the only thing that keeps that inner tmux reachable.

## Everything else

| tmux | tmux command | Quil | Status |
|---|---|---|---|
| `?` | list-keys | `system.shortcuts` | exact |
| `r` | refresh-client | Redraw (`Alt+Shift+L`) | analogue |
| `:` | command-prompt | — | absent |
| `t` | clock-mode | — | absent |
| `~` | show-messages | — | absent |

**There is no command prompt because there are no commands.** tmux's `:` prompts for a command in tmux's own command language. Quil has no such language; the command palette (`Alt+Shift+P`) covers the discovery half of what `:` is used for.

## What Quil adds

These have no tmux equivalent and keep their own chords under either preset.

| Key | Action | Notes |
|---|---|---|
| `Alt+Shift+P` | Command palette | Fuzzy-find any action, pane or tab |
| `Alt+Shift+A` | Attention queue | Jump to the agent blocked longest, across every project |
| `Alt+E` | Pane notes | A scratch editor bound to one pane |
| `Alt+G` / `Alt+D` | lazygit / hunk overlay | Git UI over the repo at the pane's directory |
| `Alt+R` | Restart pane | Respawns the process; AI panes resume their session |
| `Alt+Shift+I` | Pane input history | Every prompt submitted to an AI pane |
| `Alt+M` | Mute pane notifications | Per-pane, for the notification sidebar |
| `Ctrl+N` | New typed pane | Pick a plugin: Claude Code, OpenCode, terminal… |

## What is missing and probably shouldn't be

Two rows above are genuine gaps rather than concept mismatches, and they are the ones most likely to catch tmux muscle memory:

- **`l` — last-window.** Quil has no "bounce to the previous tab" at all. `Alt+O` does exactly this for *projects*, so the concept exists one level up.
- **`;` — last-pane.** `Alt+Backspace` is close, but it is a history stack rather than a two-way toggle, so repeated presses walk backwards instead of flipping between two panes.

Neither is bound by the preset because neither action exists yet. If you want them, they need adding to the registry first — see [Keybindings](keybindings.md#customizing-keybindings).
