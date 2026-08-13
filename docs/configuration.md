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
- [`[overlay]`](#overlay)
- [`[update]`](#update)
- [`[[destinations]]`](#destinations)
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
sidebar_open = false            # project sidebar starts collapsed
sidebar_width = 22              # project sidebar width — reserves layout width; NOT [notification]'s

[mcp]
highlight_duration = "10s"      # border flash duration when AI touches a pane

[notification]
sidebar_width = 30              # notification OVERLAY width (draws over panes) — not [ui]'s
max_events = 200                # ring-buffer cap (per daemon, both sidebar and MCP)

[notification.hooks]
claude = "default"              # "default" | "verbose" | "off"
opencode = "default"            # same

[notification.desktop]
enabled = true                  # OS toasts; Windows only, needs `quil notify setup`
blocked = true                  # toast when a pane parks waiting on you
done = true                     # toast when a turn finishes while you are away
cooldown = "30s"                # per pane, shared by both kinds
require_blur = true             # only toast while the terminal is unfocused

[overlay]
idle_timeout_minutes = 5        # destroy a hidden lazygit overlay after this long; 0 disables
max_live = 5                    # cap live overlays across all tabs; 0 disables

[update]
check = true                    # Daily check for new releases
auto = true                     # Download and stage in background

# Extra daemons to attach beside the local one. Optional and omitted by
# default; one table per host. See [[destinations]] below.
# [[destinations]]
# name = "gpu box"
# dest = "gpu01"

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
restart_pane = "alt+r"          # kill + respawn the active pane's process (AI sessions resume)
toggle_eager = "alt+shift+e"    # toggle eager restore; eager panes respawn on restart, others load lazily
go_back = "alt+backspace"       # pane history back (after jumping via sidebar Enter)
notes_toggle = "alt+e"          # toggle pane notes editor
toggle_lazygit = "alt+g"        # toggle lazygit overlay for the repo at the active pane's CWD
toggle_wrap = "alt+shift+w"     # AI-pane preview: switch left-edge crop (default) <-> soft-wrap
redraw = "alt+shift+l"          # force full screen repaint (clears rendering artifacts)
new_project = "alt+shift+n"
project_picker = "alt+p"        # fuzzy-find and switch project
project_toggle = "alt+o"        # bounce between the two most recent projects
attention_queue = "alt+shift+a" # oldest pane waiting on you, across every project
sidebar_toggle = "alt+shift+s"  # collapse/expand the PROJECT sidebar (not the notification one)
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
| `sidebar_open` | bool | `false` | Whether the **project** sidebar starts expanded. Closed by default so existing installs keep their pane geometry unchanged. `Alt+Shift+S` flips it, and the setting persists. |
| `sidebar_width` | int | `22` | Width of the **project** sidebar. Unlike the notification sidebar this reserves real layout width — panes are narrower by exactly this many columns while it is open, and toggling it resizes every pane's PTY. Clamped against the terminal width, so an oversized value cannot push the pane area off screen. Editable without touching this file: **F1 → Settings → Sidebar width** (the one Settings row that applies immediately rather than on next launch), or **drag the sidebar's right edge** with the mouse. Both persist here. A drag will not take the strip below 12 columns — collapsing it entirely is what `Alt+Shift+S` is for, and a strip dragged to nothing would leave no edge to grab it back by. |

> **Two different `sidebar_width` keys.** This one (`[ui]`, default `22`) is the
> project sidebar on the **left**, which reserves layout width. The one in
> [`[notification]`](#notification) (default `30`) is the notification overlay on
> the **right**, which draws over the pane area and resizes nothing. They are
> independent settings in different sections.

## `[mcp]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `highlight_duration` | duration | `"10s"` | When the AI interacts with a pane via MCP, its border flashes orange for this duration. `"0s"` disables. See [MCP visual indicator](mcp.md#visual-mcp-activity-indicator). |

## `[notification]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `sidebar_width` | int | `30` | Width of the notification sidebar overlay (`Alt+N`). Distinct from [`[ui] sidebar_width`](#ui), which sizes the project sidebar on the left. The sidebar draws over the right edge of the pane area — panes keep their size (no PTY resize) and the covered columns reappear when it closes. Values below ~25 truncate event titles and excerpts heavily. |
| `max_events` | int | `200` | Ring-buffer cap for the daemon's notification queue. The sidebar and MCP `get_notifications` both read from this queue. Each event is bounded to ≤ 4 KiB `Message` + ≤ 1 KiB per `Data` value (`_quil_truncated` flag set when truncated). |

### `[notification.hooks]`

Hook-driven notifications surface structured events from Claude Code and OpenCode (permission asks, retries, "reply ready", file edits, …) instead of guessing from the PTY byte stream. The daemon writes the resolved tier to the hook script's environment via `QUIL_HOOK_MODE` at pane spawn so the script can branch on it.

| Key | Type | Default | What it does |
|---|---|---|---|
| `claude` | string | `"default"` | Tier for Claude Code panes. `"default"` forwards SessionEnd, UserPromptSubmit, Notification, PermissionRequest, Stop, PreCompact, PostCompact, SubagentStart/Stop, TaskCreated/TaskCompleted. `"verbose"` additionally forwards PreToolUse/PostToolUse (one card per tool call — useful for debugging, noisy in normal use). `"off"` disables hook event forwarding entirely; Quil falls back to the legacy PTY-byte idle heuristic. |
| `opencode` | string | `"default"` | Tier for OpenCode panes. `"default"` forwards session.idle/error/compacted, session.status retry only, file.edited batched 1 s, permission.ask, experimental.session.compacting. `"verbose"` adds tool.execute.before/after. `"off"` disables hook event forwarding. |

The hook events flow through a JSONL spool (`~/.quil/events/<paneID>.jsonl`) that the daemon polls every 200 ms. Truncated on daemon start (no replay of stale events); deleted on pane destroy.

### `[notification.desktop]`

Operating-system toasts raised from the same attention states the project sidebar marks. **Windows only** — macOS and Linux have no transport that supports click-to-route, so nothing is faked there and the Settings row reads `unsupported`.

| Key | Type | Default | What it does |
|---|---|---|---|
| `enabled` | bool | `true` | Master switch. Defaults **on**, and that does not make registration implicit: a toast still needs the Start Menu shortcut and `quil://` handler that `quil notify setup` writes. The flag says "I want these"; setup is the gate. |
| `blocked` | bool | `true` | Toast when a pane parks waiting on you — the ▲ state in the project sidebar. |
| `done` | bool | `true` | Toast when a turn finishes while you were away — the ✓ state. Never fires for the pane you are looking at. |
| `cooldown` | duration | `"30s"` | Rate limit, **per pane and shared by both kinds**: a pane that parks and then finishes five seconds later produces one toast, not two. A malformed or non-positive value falls back to 30 s rather than disabling the limit. |
| `require_blur` | bool | `true` | Suppress toasts while the terminal has focus. Set `false` if your terminal does not implement focus reporting (DEC 1004) — without it Quil never learns the window lost focus and the gate suppresses everything. |

Also editable at **F1 → Settings** ("Desktop notifications"), which applies immediately — no restart. That row reports registration *state* rather than the flag, so it reads `on (run notify setup)` on a machine where the flag is on but nothing is registered.

**Setup is explicit and reversible.** `quil notify setup` writes exactly two things, both user-scope with no admin rights, and prints them:

- `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Quil.lnk` — carries the AppUserModelID Windows requires before it will display a toast from an unpackaged executable, and supplies the toast's name and icon.
- `HKCU\Software\Classes\quil` — the `quil://` protocol handler that makes a toast clickable.

`quil notify setup --remove` deletes both. `quil notify status` reports what is registered and what the config says; `quil notify test` sends one self-labelled canary toast.

Dev builds use a separate namespace throughout (`quil-dev://`, AUMID `artyomsv.quil.dev`, `Quil (dev).lnk`) so a dev instance can never overwrite a production registration — these artifacts live outside `QUIL_HOME` and are the one thing dev isolation does not get for free.

> **Windows may not honour a freshly registered app immediately.** Windows indexes the new Start Menu shortcut on its own schedule; until it does, toasts are silently dropped and `quil notify test` reports `0x80070490`. Signing out and back in forces it. Nothing in Quil can accelerate this.

**Clicking a toast can only move your cursor.** The `quil://` handler parses the URI, validates the pane id, and writes it to a per-PID named pipe that the running TUI reads — there is deliberately no path from a registered URI to spawning a pane, sending input, or running a command, because a registered scheme is invokable by any local process. Inline toast action buttons are refused for that reason rather than merely deferred.

## `[overlay]`

Bounds the Alt+G lazygit overlay pane. `Alt+G` again (or switching away and back) only *hides* it — the process keeps running; only quitting lazygit itself (`q`) or one of these two limits reclaims it.

| Key | Type | Default | What it does |
|---|---|---|---|
| `idle_timeout_minutes` | int | `5` | Destroy an overlay that has been hidden for at least this long. `0` disables idle eviction — a hidden overlay then runs until lazygit quits on its own or its tab is destroyed. Clamped to at most `525600` (one year); a negative value reads as `0`. |
| `max_live` | int | `5` | Cap on overlays live across all tabs at once. Opening one past the cap evicts the least recently **shown** overlay (not the oldest one created) to make room. `0` disables the cap. |

Both keys are also editable at **F1 → Settings**, which pushes the change to the running daemon immediately — no restart needed.

"Hidden" means no attached client has the overlay on screen. With two clients on one daemon, an overlay one of them is displaying is not reclaimed because the other switched tabs.

The daemon seeds both values from its **own** config at startup, so a daemon with no client attached still reclaims overlays on its own terms. A connected client then pushes its values to every daemon it is attached to, which overrides that daemon's `[overlay]` section for as long as the client is attached — so against a remote daemon it is the client's settings that govern, and if several clients disagree the last one to push wins.

## `[update]`

Automatic update checking and staging.

| Key | Default | Description |
|-----|---------|-------------|
| `check` | `true` | Daily check for new releases (one unauthenticated GET to `api.github.com`). Set `false` to disable the daily background check. F1 → About → "Check for updates" / "Update now" still contacts GitHub on demand regardless of this setting. |
| `auto` | `true` | Download and stage new releases in the background once a check finds one. The update applies at the next `quil` launch after a single `[Y/n]` confirmation. Set `false` for notify-only (the daily check still runs; nothing downloads until you trigger it from About). |

A pending update shows as `↑ v<version>` in the status bar (`ready` once
staged), in F1 → About, and once per version as a startup dialog. Dev and
debug builds never self-update (the pipeline is compiled out via a build-time
flag, since a self-update would strip the dev/debug ldflags baked into those
binaries) — see `./scripts/dev.sh build`. Installs in non-writable locations
(package managers) also never self-update; those show the release page URL
instead.

## `[[destinations]]`

Extra daemons to attach at launch, alongside the local one. Each destination's
projects appear in the same sidebar as the local ones, and each keeps its own
tabs, panes and agent state.

```toml
[[destinations]]
name = "gpu box"
dest = "gpu01"

[[destinations]]
name = "prod"
dest = "prod-jump"        # an ~/.ssh/config Host alias works too
```

| Key | Default | Description |
|-----|---------|-------------|
| `dest` | — | Required. Passed to `ssh` **verbatim**, so an `~/.ssh/config` `Host` alias keeps its `HostName`, `Port`, `User` and `ProxyJump`. This is also the routing key Quil uses internally, so it must be unique. |
| `name` | the `dest` | Label shown in launch warnings and in the reconnect banner. Useful when `dest` is a bare IP. |

Each host needs `quil` installed and reachable over ssh — run
`quil remote setup <dest>` once per host; it records the absolute path under
`[remote.hosts.<dest>]` so attaching works even when the non-interactive `PATH`
cannot see the install directory.

Notes:

- Hosts are dialled **in parallel and non-interactively**, so a host that is
  switched off delays startup by one connect timeout at most and never blocks on
  an ssh prompt. Accept a new host's key with one manual `ssh <dest>` (or
  `quil remote setup <dest>`) before adding it here.
- A host that is unreachable at launch is reported on stderr and skipped —
  the client still starts with everything else. Relaunch once the host is back.
- A host whose daemon version does not match this client is skipped with an
  explanation in `quil.log`; run `quil remote setup <dest>` to upgrade it.
- `quil --remote <host>` **ignores this list**. That mode means "drive that one
  machine", so it attaches to that host alone and to no local daemon.

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
| `quick_actions` | `alt+a` | Open the pane context menu for the active pane (same menu as right-click) |
| `command_history` | `alt+shift+i` | Open the active pane's input-history modal (submitted prompts; only meaningful for panes whose plugin records history, e.g. claude-code) |
| `focus_pane` | `ctrl+e` | Toggle focus mode |
| `notification_toggle` | `alt+n` | Cycle the notification sidebar: hidden → visible → visible+focused → hidden |
| `notification_focus` | `f3` | Jump focus to the sidebar (alt path when `alt+n` is intercepted by the terminal) |
| `mute_pane` | `alt+m` | Toggle notification mute on the active pane. Muted panes show `[muted]` on their border and never fire idle / bell / process-exit / hook events. Persisted in `workspace.json` so mute survives daemon restart. |
| `restart_pane` | `alt+r` | Restart the active pane's process in place (confirm dialog). Kills the child and respawns it with the plugin's resume strategy — AI panes resume their recorded session. Recovery for a process that stopped reading input. |
| `toggle_eager` | `alt+shift+e` | Toggle eager restore on the active pane. Eager panes respawn immediately on daemon restart; other panes load lazily (process started only when the tab is first opened). Tabs with an eager pane show `●` in the tab bar. Persisted in `workspace.json`. |
| `go_back` | `alt+backspace` | Pane history back — return to the pane you were on before the sidebar's `Enter` jump |
| `notes_toggle` | `alt+e` | Open / close the per-pane notes editor |
| `toggle_lazygit` | `alt+g` | Toggle lazygit overlay for the git repo resolved from the active pane's current directory. Only shown when the `lazygit` binary is installed. |
| `toggle_wrap` | `alt+shift+w` | Switch the active AI pane's preview between left-edge crop (default) and soft-wrap. Only meaningful for `wide_canvas` panes rendered smaller than the window; per-pane, not persisted. |
| `redraw` | `alt+shift+l` | Force a full screen repaint — clears rendering artifacts (scrambled or misplaced characters) without restarting the TUI |
| `new_project` | `alt+shift+n` | Open the create-project dialog |
| `project_picker` | `alt+p` | Fuzzy-find and switch project |
| `project_toggle` | `alt+o` | Bounce between the two most recent projects |
| `attention_queue` | `alt+shift+a` | Jump to the oldest pane waiting on you, across every project |
| `sidebar_toggle` | `alt+shift+s` | Collapse / expand the **project** sidebar. Not the notification overlay — that is `notification_toggle`. This one reserves real layout width, so toggling it resizes every pane's PTY. |

## Per-plugin instances

Quil persists plugin "instance" presets (saved hostnames for SSH, named claude-code workdirs, etc.) in `~/.quil/instances.json`. This file is **not** edited by hand — use the `Ctrl+N` setup dialog. Hand-editing risks deserialization errors; back it up first.

## Recent working directories

When a pane plugin asks for a working directory (`prompts_cwd`, e.g. Claude Code), the setup dialog offers your **last 5 used folders** as a one-keystroke quick pick, so switching between projects doesn't mean re-navigating the directory tree each time. Select a recent folder and press Enter to open there, or choose **Browse…** to open the full directory browser. Folders that no longer exist are skipped automatically. The list is stored in `~/.quil/recent-cwds.json` (managed by Quil — no hand-editing) and survives daemon/TUI restart. Git-repo discovery (`discover = "git"`, e.g. lazygit) takes priority over the recent list when it finds repos near the active pane.

## Remote project cache

Quil records the last project list seen on each remote destination in `~/.quil/remote-projects-<host>.json` (plus a bare `remote-projects.json` for the local daemon), so a host that is unreachable at launch can still be shown by name in the sidebar instead of vanishing. This file is managed by Quil — no hand-editing. A stale copy is harmless: it self-heals on the next successful broadcast from that host, and is removed automatically when the host is disconnected.

## How edits get persisted

- **Edits via the F1 → Settings dialog** auto-save on TUI exit. The setter for each row flips `m.configChanged = true`; `main.go` writes the file atomically via temp + rename.
- **Edits to `~/.quil/config.toml` while the TUI is open** are picked up on next launch — there is intentionally no live reload (would require re-plumbing the file handle owned by `main.go`).
- **Atomic write** — Quil writes to `~/.quil/config.toml.tmp` then renames over the target. A crash mid-write leaves the previous config intact.
