# Aethel Roadmap

Detailed progress tracker and future plans for Aethel.

## Completed

### M1: Foundation
> Daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, daemon lifecycle.

All core infrastructure is in place. The client-daemon architecture works across Linux, macOS, and Windows. Shell integration auto-injects OSC 7 hooks for CWD tracking. Binary split tree enables arbitrarily nested pane layouts.

### M2: Persistence
> Workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions.

Workspace state (tabs, panes, layout, CWD) persists to `~/.aethel/workspace.json` with atomic writes and `.bak` rollback. Ghost buffers capture PTY output to binary files. On daemon restart, shells respawn with saved CWD and ghost buffers replay instantly.

### M3: Resume Engine
> Regex scrapers, token extraction, AI session resume.

Session resume infrastructure is complete. The `preassign_id` strategy generates a UUID at pane creation, passes it via `--session-id`, and resumes with `--resume` after daemon restart. The `session_scrape` strategy extracts tokens from PTY output via regex for tools that don't support pre-assigned IDs. The `rerun` strategy re-executes the same command + args. Fallback to shell when resume args can't be resolved.

### M4: Plugin System
> Typed panes with TOML plugins, plugin registry, pane creation dialog.

The plugin system is fully operational. 4 built-in plugins ship with Aethel:

| Plugin | Status | Persistence |
|--------|--------|-------------|
| **Terminal** | Production | `cwd_only` — restore working directory |
| **Claude Code** | Production | `preassign_id` — UUID-based session resume |
| **SSH** | POC | `rerun` — reconnect with same args |
| **Stripe** | POC | `rerun` — re-listen with same webhook URL |

Key capabilities:
- **TOML plugin format** — user-created plugins in `~/.aethel/plugins/*.toml`
- **Plugin registry** with auto-detection (`exec.LookPath`)
- **Pane creation dialog** (`Ctrl+N`) — three-step: category, plugin, split direction
- **Error handlers** — regex patterns match PTY output and show help dialogs
- **Atomic pane replacement** — swap pane type in-place
- **Resuming/preparing spinner** — animated border indicator during pane startup
- **Window size persistence** — save/restore terminal dimensions across restarts

---

## In Progress

### M5: Polish
> Production-quality UX, plugin refinements, observability, encrypted tokens.

**Completed:**
- Default TOML plugins — claude-code, ssh, stripe shipped as embedded editable TOML files
- Plugin instance management — saved SSH connections, Stripe webhooks persisted to `instances.json`
- Plugin management UI — F1 → Plugins with view, reload, restore defaults, in-app TOML editor
- In-app TOML editor — full-screen editor with syntax highlighting and validation
- Pane creation dialog extended — 4-step flow: category → plugin → instance/form → split direction
- Centralized snapshot queue — event-driven with 500ms debounce, replaces scattered calls
- Per-plugin ghost buffer toggle — `ghost_buffer` bool controls PTY output persistence
- GhostSnap restore — clean ghost buffer replay after daemon restart
- Diagnostic logging — trace-level logging across daemon, TUI, and IPC
- Plugin configuration reference — comprehensive docs for custom plugin creation

**Remaining:**
- JSON transformer (`Ctrl+J`) — format and highlight JSON in terminal output
- Observability commands — `aethel status`, session metrics, log level control
- Encrypted token storage — OS keyring integration for sensitive scraped values
- Tab dock positions (top/bottom/left/right)
- OS service integration (`aethel service install` — systemd/launchd/Task Scheduler)

---

## Planned

### M6: Pane Focus Mode

Switch the active pane to full-window mode, hiding all other panes. Gives the user maximum screen space for focused work in a single pane.

**Behavior:**
- Keybinding (e.g., `Alt+F` or `F11`) toggles focus mode on/off
- In focus mode: active pane fills the entire tab content area (no splits visible)
- Other panes remain alive in the background — they continue running, receiving PTY output
- Status bar shows a focus indicator (e.g., `[focus]`)
- Exiting focus mode restores the previous split layout exactly
- Focus state is NOT persisted — exiting Aethel always returns to normal layout

### M7: Pane Notes

Side-by-side note-taking mode linked to individual panes. When enabled, the window splits into the active pane (left) and a text editor (right) where the user can write notes about their work.

**Behavior:**
- Keybinding (e.g., `Alt+N`) toggles notes mode on/off for the active pane
- In notes mode: all other panes are hidden; the screen shows active pane (left) + text editor (right)
- Notes are stored as plain text files in `~/.aethel/notes/<pane-id>.txt`
- Notes are linked to the pane — after hard restart, opening notes for the same pane shows previous notes
- The text editor supports basic editing: type, backspace, enter, arrow keys, scroll
- Notes persist independently from workspace state — they survive pane destruction and can be browsed later
- Exiting notes mode restores the previous layout

### M8: Bubble Tea v2 Migration

Migrate from Bubble Tea v1 to v2 when it becomes stable.

**Motivation:**
- v2 has improved key handling (shift/ctrl modifiers distinguishable)
- Better performance and rendering
- Built-in spinner and other components
- Breaking API changes — requires updating all TUI code

**Approach:**
- Wait for v2 stable release
- Update import paths and adapt to new API
- Leverage new key modifier support to enable `Ctrl+Shift+W` and similar bindings
- Replace custom spinner with built-in component
