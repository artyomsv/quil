---
headline: Keybindings can be multi-key sequences, with a tmux preset
---
- **A keybinding can now be several keys pressed in sequence.** `Ctrl+B` then `c`, in
  the tmux style, rather than one chord per action. The status bar shows the keys typed
  so far while a sequence is pending, `Esc` cancels one, and a sequence that matches
  nothing says so instead of silently doing nothing.

  Pressing the leading key twice sends it through to the program in the pane — so a pane
  running `ssh` into a host running tmux still reaches that tmux's prefix.

- **Keymaps are now selectable presets, in `~/.quil/bindings.toml`.** Set
  `preset = "tmux"` for a tmux-compatible layout: `prefix c` opens a tab, `prefix %` and
  `prefix "` split a pane, `prefix z` zooms, `prefix 1`–`9` switch tabs, `prefix d`
  detaches. Presets **replace** rather than add, so selecting `tmux` gives up `Ctrl+T`
  and `Ctrl+W`; any action the preset does not mention keeps its usual key.

  Change the prefix with one line — `prefix = "ctrl+a"` — and override any individual
  binding under `[bindings]`.

- **Your existing keybindings migrate automatically on first launch.** Only settings that
  differ from the shipped defaults are carried across, so a keymap you never customized
  stays free to follow future default changes rather than being frozen as it is today.
  `config.toml` keeps its `[keybindings]` table for now.

- **Tab switching, next/previous tab, and the shortcuts list are rebindable.** `Alt+1`–`9`
  used to be fixed keys that no setting could reach. They are ordinary actions now, and
  two new ones — next tab and previous tab — ship unbound for a keymap to claim.
