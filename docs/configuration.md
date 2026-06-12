# Configuration

Quil reads `~/.quil/config.toml` (or `$QUIL_HOME/config.toml` when `QUIL_HOME` is set) at startup. Every section is optional — missing keys use the defaults shown below. Edit and save; some settings live-apply on next launch.

## Table of contents

- [File location](#file-location)
- [`[daemon]`](#daemon)
- [`[ghost_buffer]`](#ghost_buffer)
- [`[logging]`](#logging)
- [`[ui]`](#ui)
- [`[mcp]`](#mcp)
- [`[notification]`](#notification)
- [`[keybindings]`](#keybindings)
- [Per-plugin instances](#per-plugin-instances)
- [How edits get persisted](#how-edits-get-persisted)

## File location

| Variable | Resolves to | Notes |
|---|---|---|
| (default) | `~/.quil/config.toml` | Standard install |
| `$QUIL_HOME=/path/to/dir` | `$QUIL_HOME/config.toml` | Dev mode and `quil-dev` builds |

The file is created with `0600` permissions on first save and only your user can read it.

## Full default config

```toml
[daemon]
snapshot_interval = "30s"
auto_start = true

[ghost_buffer]
max_lines = 500
dimmed = true

[logging]
level = "info"            # debug, info, warn, error
max_size_mb = 5           # rotate quild.log / quil.log when file exceeds this size (MB)
max_files = 10            # number of timestamped rotation archives to keep

[ui]
tab_dock = "top"
theme = "default"
mouse_scroll_lines = 3
page_scroll_lines = 0           # 0 = half-page (dynamic) — terminal pane scrollback
log_viewer_page_lines = 40      # Alt+Up/Alt+Down jump in F1 → log viewer
show_disclaimer = true          # beta disclaimer on startup

[mcp]
highlight_duration = "10s"      # border flash duration when AI touches a pane

[notification]
sidebar_width = 30              # columns reserved for the notification sidebar
max_events = 200                # ring-buffer cap (per daemon, both sidebar and MCP)

[notification.hooks]
claude = "default"              # "default" | "verbose" | "off"
opencode = "default"            # same

[keybindings]
quit = "ctrl+q"
new_tab = "ctrl+t"
close_pane = "ctrl+w"
close_tab = "alt+w"
split_horizontal = "alt+shift+h"
split_vertical = "alt+shift+v"
pane_left = "alt+left"
pane_right = "alt+right"
pane_up = "alt+up"
pane_down = "alt+down"
next_pane = ""                  # unbound by default — use directional Alt+Arrow
prev_pane = ""
rename_tab = "f2"
rename_pane = "alt+f2,alt+shift+r"   # macOS users: alt+shift+r is the reliable form
cycle_tab_color = "alt+c"
scroll_page_up = "alt+pgup"
scroll_page_down = "alt+pgdown"
paste = "ctrl+v"
focus_pane = "ctrl+e"
notification_toggle = "alt+n"   # show / focus / hide the notification sidebar
notification_focus = "f3"       # jump focus to the sidebar (alt path when alt+n misbehaves)
mute_pane = "alt+m"             # toggle notification mute for the active pane
toggle_eager = "alt+shift+e"    # toggle eager restore; eager panes respawn on restart, others load lazily
go_back = "alt+backspace"       # pane history back (after jumping via sidebar Enter)
notes_toggle = "alt+e"          # toggle pane notes editor
toggle_lazygit = "alt+g"        # toggle lazygit overlay for the repo at the active pane's CWD
redraw = "alt+shift+l"          # force full screen repaint (clears rendering artifacts)
```

## `[daemon]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `snapshot_interval` | duration | `"30s"` | Periodic safety-net write of `workspace.json` + ghost buffers. Event-driven snapshots (pane create/destroy, etc.) still fire 500 ms after the trigger. |
| `auto_start` | bool | `true` | The TUI auto-starts `quild --background` when it can't find an existing daemon. Set `false` if you manage `quild` yourself (systemd, launchd, etc.) — the TUI will error instead of auto-spawning. |

## `[ghost_buffer]`

The "ghost buffer" is the rendered preview Quil shows immediately on reconnect, before the actual shell has caught up.

| Key | Type | Default | What it does |
|---|---|---|---|
| `max_lines` | int | `500` | Lines per pane retained in the on-disk ghost buffer (`~/.quil/buffers/<pane-id>.buf`). Larger = better restore fidelity, more disk. |
| `dimmed` | bool | `true` | While the pane is showing ghost (not yet receiving live output), render the border muted with a `restored` label. First live output clears the flag. |

**Lazy restore:** On daemon restart only the active tab's panes spawn immediately. Panes in other tabs are deferred — their workspace model and ghost buffer history are available at once, but the child process is not started until the tab is first opened. Mark a pane as "eager" (`Alt+Shift+E`, config key `toggle_eager`) to force it to spawn immediately regardless of which tab is active. Eager panes are marked with `●` on the tab label.

## `[logging]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `level` | string | `"info"` | One of `debug`, `info`, `warn`, `error`. `debug` traces clipboard pipeline, per-key handlers, image-paste decoding, MCP IPC. Apply-on-next-launch only. |
| `max_size_mb` | int | `5` | Per-file rotation threshold. When `quil.log` or `quild.log` would exceed this size the file is rotated to a timestamped archive (`stem-YYYYMMDD-HHMMSS.log`) and a fresh base file is opened. |
| `max_files` | int | `10` | How many timestamped rotation archives to keep per log file. Older archives are pruned by modification time. |

## `[ui]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `tab_dock` | string | `"top"` | Where the tab bar sits. (Currently only `top` is implemented.) |
| `theme` | string | `"default"` | Reserved for future theming. |
| `mouse_scroll_lines` | int | `3` | Lines per mouse-wheel notch in pane scrollback. |
| `page_scroll_lines` | int | `0` | Lines per `Alt+PgUp` / `Alt+PgDown`. `0` = half the pane height (dynamic). |
| `log_viewer_page_lines` | int | `40` | `Alt+Up` / `Alt+Down` jump distance in the F1 log viewer. |
| `show_disclaimer` | bool | `true` | Display the beta disclaimer on startup. The `Don't show again` button flips this to `false`. |

## `[mcp]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `highlight_duration` | duration | `"10s"` | When the AI interacts with a pane via MCP, its border flashes orange for this duration. `"0s"` disables. See [MCP visual indicator](mcp.md#visual-mcp-activity-indicator). |

## `[notification]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `sidebar_width` | int | `30` | Columns reserved for the notification sidebar when toggled (`Alt+N`). Reducing this gives more room to panes; values below ~25 truncate event titles and excerpts heavily. |
| `max_events` | int | `200` | Ring-buffer cap for the daemon's notification queue. The sidebar and MCP `get_notifications` both read from this queue. Each event is bounded to ≤ 4 KiB `Message` + ≤ 1 KiB per `Data` value (`_quil_truncated` flag set when truncated). |

### `[notification.hooks]`

Hook-driven notifications surface structured events from Claude Code and OpenCode (permission asks, retries, "reply ready", file edits, …) instead of guessing from the PTY byte stream. The daemon writes the resolved tier to the hook script's environment via `QUIL_HOOK_MODE` at pane spawn so the script can branch on it.

| Key | Type | Default | What it does |
|---|---|---|---|
| `claude` | string | `"default"` | Tier for Claude Code panes. `"default"` forwards SessionEnd, UserPromptSubmit, Notification, PermissionRequest, Stop, PreCompact, PostCompact, SubagentStart/Stop, TaskCreated/TaskCompleted. `"verbose"` additionally forwards PreToolUse/PostToolUse (one card per tool call — useful for debugging, noisy in normal use). `"off"` disables hook event forwarding entirely; Quil falls back to the legacy PTY-byte idle heuristic. |
| `opencode` | string | `"default"` | Tier for OpenCode panes. `"default"` forwards session.idle/error/compacted, session.status retry only, file.edited batched 1 s, permission.ask, experimental.session.compacting. `"verbose"` adds tool.execute.before/after. `"off"` disables hook event forwarding. |

The hook events flow through a JSONL spool (`~/.quil/events/<paneID>.jsonl`) that the daemon polls every 200 ms. Truncated on daemon start (no replay of stale events); deleted on pane destroy.

## `[keybindings]`

Every binding accepts a Bubble Tea key string. Common forms:

- Single key — `enter`, `tab`, `escape`, `space`, `f1` … `f12`
- Modified — `ctrl+a`, `alt+left`, `shift+tab`, `ctrl+shift+up`, `alt+shift+v`
- Multiple bindings — comma-separate them in the same field, e.g. `rename_pane = "alt+f2,alt+shift+r"`. Quil tries each binding for a match. Useful when a default binding is unreliable on a specific platform (macOS in particular intercepts most F-keys unless "Use F1, F2, etc. keys as standard function keys" is enabled).
- Empty string — explicitly unbind (e.g., `next_pane = ""`)

Multiple modifiers stack with `+` (no spaces). Mouse buttons are not bindable here — mouse events route through Bubble Tea's mouse subsystem.

### Bindable actions

| Key | Default | Purpose |
|---|---|---|
| `quit` | `ctrl+q` | Quit the TUI |
| `new_tab` | `ctrl+t` | Open a new tab |
| `close_tab` | `alt+w` | Close active tab (with confirm) |
| `close_pane` | `ctrl+w` | Close active pane (with confirm) |
| `split_horizontal` | `alt+shift+h` | Split side-by-side |
| `split_vertical` | `alt+shift+v` | Split top/bottom |
| `pane_left` / `right` / `up` / `down` | `alt+arrow` | Spatial pane navigation |
| `next_pane` / `prev_pane` | *(unbound)* | Linear pane cycling — bind to `tab` / `shift+tab` if preferred (you'll lose PTY tab-completion) |
| `rename_tab` | `f2` | Inline rename for the active tab |
| `rename_pane` | `alt+f2,alt+shift+r` | Inline rename for the active pane. The second binding is a macOS-friendly fallback since `f2` is often eaten by the OS and `option` is not always configured as Meta. |
| `cycle_tab_color` | `alt+c` | Cycle through 8 tab colours |
| `scroll_page_up` / `scroll_page_down` | `alt+pgup` / `alt+pgdown` | Pane scrollback |
| `paste` | `ctrl+v` | Paste from clipboard (text or image) |
| `focus_pane` | `ctrl+e` | Toggle focus mode |
| `notification_toggle` | `alt+n` | Cycle the notification sidebar: hidden → visible → visible+focused → hidden |
| `notification_focus` | `f3` | Jump focus to the sidebar (alt path when `alt+n` is intercepted by the terminal) |
| `mute_pane` | `alt+m` | Toggle notification mute on the active pane. Muted panes show `[muted]` on their border and never fire idle / bell / process-exit / hook events. Persisted in `workspace.json` so mute survives daemon restart. |
| `toggle_eager` | `alt+shift+e` | Toggle eager restore on the active pane. Eager panes respawn immediately on daemon restart; other panes load lazily (process started only when the tab is first opened). Tabs with an eager pane show `●` in the tab bar. Persisted in `workspace.json`. |
| `go_back` | `alt+backspace` | Pane history back — return to the pane you were on before the sidebar's `Enter` jump |
| `notes_toggle` | `alt+e` | Open / close the per-pane notes editor |
| `toggle_lazygit` | `alt+g` | Toggle lazygit overlay for the git repo resolved from the active pane's current directory. Only shown when the `lazygit` binary is installed. |
| `redraw` | `alt+shift+l` | Force a full screen repaint — clears rendering artifacts (scrambled or misplaced characters) without restarting the TUI |

## Per-plugin instances

Quil persists plugin "instance" presets (saved hostnames for SSH, named claude-code workdirs, etc.) in `~/.quil/instances.json`. This file is **not** edited by hand — use the `Ctrl+N` setup dialog. Hand-editing risks deserialization errors; back it up first.

## How edits get persisted

- **Edits via the F1 → Settings dialog** auto-save on TUI exit. The setter for each row flips `m.configChanged = true`; `main.go` writes the file atomically via temp + rename.
- **Edits to `~/.quil/config.toml` while the TUI is open** are picked up on next launch — there is intentionally no live reload (would require re-plumbing the file handle owned by `main.go`).
- **Atomic write** — Quil writes to `~/.quil/config.toml.tmp` then renames over the target. A crash mid-write leaves the previous config intact.
