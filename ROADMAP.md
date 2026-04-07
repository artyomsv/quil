# Quil Roadmap

Detailed progress tracker and future plans for Quil.

---

## Completed

### M1: Foundation
> Daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, daemon lifecycle.

All core infrastructure is in place. The client-daemon architecture works across Linux, macOS, and Windows. Shell integration auto-injects OSC 7 hooks for CWD tracking. Binary split tree enables arbitrarily nested pane layouts.

### M2: Persistence
> Workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions.

Workspace state (tabs, panes, layout, CWD) persists to `~/.quil/workspace.json` with atomic writes and `.bak` rollback. Ghost buffers capture PTY output to binary files. On daemon restart, shells respawn with saved CWD and ghost buffers replay instantly.

### M3: Resume Engine
> Regex scrapers, token extraction, AI session resume.

Session resume infrastructure is complete. The `preassign_id` strategy generates a UUID at pane creation, passes it via `--session-id`, and resumes with `--resume` after daemon restart. The `session_scrape` strategy extracts tokens from PTY output via regex for tools that don't support pre-assigned IDs. The `rerun` strategy re-executes the same command + args. Fallback to shell when resume args can't be resolved.

### M4: Plugin System
> Typed panes with TOML plugins, plugin registry, pane creation dialog.

The plugin system is fully operational. 4 built-in plugins ship with Quil:

| Plugin | Status | Persistence |
|--------|--------|-------------|
| **Terminal** | Production | `cwd_only` — restore working directory |
| **Claude Code** | Production | `preassign_id` — UUID-based session resume |
| **SSH** | POC | `rerun` — reconnect with same args |
| **Stripe** | POC | `rerun` — re-listen with same webhook URL |

Key capabilities:
- **TOML plugin format** — user-created plugins in `~/.quil/plugins/*.toml`
- **Plugin registry** with auto-detection (`exec.LookPath`)
- **Pane creation dialog** (`Ctrl+N`) — three-step: category, plugin, split direction
- **Error handlers** — regex patterns match PTY output and show help dialogs
- **Atomic pane replacement** — swap pane type in-place
- **Resuming/preparing spinner** — animated border indicator during pane startup
- **Window size persistence** — save/restore terminal dimensions across restarts

### M6: Pane Focus Mode
> Full-window focus for single pane (Ctrl+E toggle).

Ctrl+E toggles the active pane to fill the entire tab content area. Other panes keep running in the background, receiving PTY output. The layout tree stays intact — focus mode is a pure rendering toggle on `TabModel.focusMode`.

Key behaviors:
- **Ctrl+E** toggles focus on/off (configurable via `focus_pane` keybinding)
- Active pane resized to full tab dimensions; VT emulator + daemon PTY updated
- `[focus]` indicator in status bar
- Pane navigation (Tab/Shift+Tab) disabled in focus mode
- Split (Alt+H/V) and close (Ctrl+W) auto-exit focus mode
- Focus state is NOT persisted — restarting Quil returns to normal layout

### M8: Bubble Tea v2 Migration + Text Selection
> BT v2 + Lipgloss v2 migration, text selection, platform-native clipboard, editor enhancements.

Migrated from Bubble Tea v1.3.10 to v2.0.2 and Lipgloss v1.1.0 to v2.0.2. Added text selection, clipboard, editor selection/navigation, and beta disclaimer dialog.

Key changes:
- **Bubble Tea v2** — declarative View (`tea.View`), typed mouse events, `KeyPressMsg`
- **Lipgloss v2** — border-inclusive Width/Height semantics
- **Terminal text selection** — Shift+Arrow (char), Ctrl+Shift+Arrow (word), mouse click+drag
- **Editor text selection** — full selection/clipboard in TOML editor (Shift+Arrow, Ctrl+X/V/A, Enter to copy)
- **Editor navigation** — Ctrl+Arrow word jump, Ctrl+Alt+Arrow 3-word, Ctrl+Up/Down paragraph
- **Clipboard** — platform-native Read/Write: Win32 API, pbcopy/xclip
- **Bracketed paste** — Ctrl+V wraps content in `ESC[200~...ESC[201~`
- **Beta disclaimer** — startup dialog with random tips, "Don't show again" persists to config
- **Config persistence** — `config.Save()` for atomic config write-back
- **Go 1.25** — required by Lipgloss v2

### Pre-Built Binaries & One-Line Install — [PRD](docs/roadmap/pre-built-binaries.md)
> GoReleaser cross-compilation, GitHub Releases, install script.

GoReleaser produces archives for 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64) with SHA256 checksums. Single `release.yml` workflow with two jobs: version bump + tag, then GoReleaser build + publish. Install script (`scripts/install.sh`) for Linux/macOS with checksum verification. Homebrew tap, Scoop, Winget deferred (need external repos).

Key capabilities:
- **GoReleaser config** — `.goreleaser.yml` (v2), two builds (quil + quild), `.tar.gz`/`.zip` archives
- **Automated releases** — conventional commit analysis → version bump → tag → build → publish
- **Install script** — POSIX shell, OS/arch detection, SHA256 verification, `QUIL_VERSION` pinning
- **Version injection** — consistent `-ldflags` across GoReleaser, dev.sh, dev.ps1, rebuild.ps1, Makefile
- **CI security** — actions pinned to SHA, per-job permissions, version format validation

### M10: MCP Server — [PRD](docs/roadmap/mcp-server.md)
> Make Quil the AI's eyes and hands via Model Context Protocol.

`quil mcp` subcommand bridges MCP JSON-RPC (stdio) to daemon IPC (socket). AI assistants can read pane output, send commands, take screenshots, navigate tabs, restart panes, and control the TUI. No other terminal multiplexer offers this.

Key capabilities:
- **13 MCP tools** — Phase A: `list_panes`, `read_pane_output`, `send_to_pane`, `get_pane_status`, `create_pane`. Phase B: `send_keys`, `restart_pane`, `screenshot_pane`, `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane`, `close_tui`
- **Official MCP SDK** — `modelcontextprotocol/go-sdk` v1.4+, typed tool handlers with struct-based input schemas
- **Request-response IPC** — backward-compatible `Message.ID` field for correlation; daemon responds to specific connection
- **VT-emulated screenshots** — `charmbracelet/x/vt` renders ring buffer into text grid showing actual screen state
- **Orange highlight** — pane border flashes orange during MCP interaction (configurable `[mcp] highlight_duration`)
- **Per-pane logging** — interaction metadata in `~/.quil/mcp-logs/`; two-layer redaction (AI markers + regex fallback)
- **TUI cooperation** — `set_active_pane` and `close_tui` use daemon broadcast → TUI handler pattern
- **Process exit tracking** — `Pane.ExitCode` and `WaitExit()` with `sync.Once` on PTY sessions (Unix + Windows)

### M12: Notification Center — [PRD](docs/roadmap/notification-center.md)
> Centralized event sidebar with pane navigation and history stack.

Replaces manual pane polling with push notifications. A non-modal sidebar shows pending events; select an event to jump to the linked pane; `Alt+Backspace` returns to where you were (browser-back pattern).

Key capabilities:
- **Daemon event queue** — bounded, mutex-protected, survives TUI disconnects, replays on attach. Watcher pub/sub for MCP blocking tool
- **Event sources** — process exit detection, OSC 133 command completion with exit code, bell character detection (30s cooldown), smart idle analysis
- **Smart idle analysis** — when a pane goes idle (5s no output), last 4KB stripped of ANSI and matched against plugin `[[idle_handlers]]` patterns. SSH `[Y/n]` → "Waiting for confirmation", Claude Code prompt → "Waiting for input", password prompts detected
- **TUI sidebar** — toggled via Alt+N (visibility), F3 (focus+navigate). Severity-colored pane names (red/orange/blue). Status bar `[N events]` badge. 10s timestamp refresh
- **Pane history stack** — `Alt+Backspace` navigates back through previously visited panes
- **OSC 133 shell hooks** — bash, zsh, PowerShell emit command start/end markers. Zsh captures `$?` immediately via `local ec=$?`. PowerShell uses `[char]0x1b` for 5.1 compat
- **Plugin `[[idle_handlers]]`** — TOML section for context-aware patterns, parallel to `[[error_handlers]]`. Default patterns for terminal, claude-code, and ssh plugins
- **Plugin `path` field** — explicit binary location bypasses PATH lookup. 3-tier detection: path → LookPath → searchBinary fallback (fixes Explorer-launched apps on Windows)
- **MCP tools** — `get_notifications` (non-blocking) and `watch_notifications` (blocking up to 5 min, replaces polling). `requestWithTimeout` with `time.NewTimer` + `defer timer.Stop()`

### M7: Pane Notes — [PRD](docs/roadmap/pane-notes.md)
> Side-by-side note-taking linked to individual panes.

A plain-text notes editor that opens next to the active pane on `Alt+E`. Notes are stored one file per pane and survive pane destruction, so the context you captured while debugging in a pane is still there next week when the pane is long gone.

Key capabilities:
- **Alt+E toggle** — opens the notes editor alongside the active pane (pane left ~60%, editor right ~40%). Mutually exclusive with focus mode
- **Read-only pane while editing** — all keys route to the editor so there is never ambiguity about where input lands. Exit notes to interact with the pane
- **Three independent save safety nets** — 30-second debounce auto-save, explicit `Ctrl+S`, and unconditional save on exit (`Alt+E`, `Esc`, close/split, TUI quit)
- **Per-pane storage** — `~/.quil/notes/<pane-id>.md`, atomic temp+rename writes, survives daemon restart (pane IDs are stable via `workspace.json`)
- **Notes outlive the pane** — closing or destroying a pane does not delete its notes file; browser ships in Phase 2
- **TextEditor reuse** — the existing rune-aware editor (selection, clipboard, multi-line paste, word jumps) gained a `Highlight` field so it can render plain text with no TOML colouring

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
- Observability commands — `quil status`, session metrics, log level control
- Encrypted token storage — OS keyring integration for sensitive scraped values
- Tab dock positions (top/bottom/left/right)
- OS service integration (`quil service install` — systemd/launchd/Task Scheduler)

---

## Planned — Core Features

### M9: Project Workspace Files — [PRD](docs/roadmap/workspace-files.md)

> `.quil.toml` checked into repo — the "docker-compose.yml for dev environments."

Define workspace blueprints committed to git: tabs, panes, plugins, CWDs, commands. `cd my-project && quil` materializes the entire dev environment. Every team member gets the exact same setup. **Network effect within teams.**

### M11: Command Palette — [PRD](docs/roadmap/command-palette.md)

> `Ctrl+Shift+P` fuzzy-find overlay for everything.

Search panes, execute commands, switch tabs, create panes, open saved instances — all from a single keyboard shortcut. Fuzzy string matching makes every feature instantly discoverable.

---

## Planned — Growth & Adoption

### The "Holy Shit" Demo — [PRD](docs/roadmap/demo-gif.md)

> 30-second GIF: 5 panes → reboot → `quil` → everything snaps back.

The entire pitch in one visual. Goes on README, Hacker News, r/programming, Twitter/X. Adoption for developer tools is driven by a single viral moment. **Priority 2** — prerequisite for marketing.

### Community Plugin Registry — [PRD](docs/roadmap/community-plugins.md)

> `quil plugin install aider` — community TOML plugins via GitHub.

GitHub repo as registry, `quil plugin install/search/update` CLI. High-value plugins: Aider, lazygit, k9s, Docker Compose, ngrok, pgcli. Every plugin makes Quil useful to a new audience.

### tmux Migration Path — [PRD](docs/roadmap/tmux-migration.md)

> Import keybindings and session layouts from tmux.

`quil import-keybindings tmux` reads `~/.tmux.conf`, maps to `config.toml`. `quil import-session` snapshots a running tmux session into an Quil workspace. tmux has millions of users — making switching painless is the fastest acquisition channel.

---

## Planned — Advanced Features

### Smart Process Health & Auto-Restart — [PRD](docs/roadmap/process-health.md)

> Green/yellow/red health indicators, auto-restart with backoff, stale detection.

Elevate `error_handlers` to a first-class health monitoring system. Auto-restart crashed panes with exponential backoff, detect stale processes, fire desktop notifications. Plugin TOML `[health]` section for configuration. Moves Quil from "terminal organizer" to "workflow orchestrator."

### Cross-Pane Context Awareness — [PRD](docs/roadmap/cross-pane-events.md)

> Build fails → AI pane gets a toast → one keypress sends context.

Event bus connecting panes: build errors notify AI assistants, SSH auto-reconnects, test passes flash green, webhook counters badge tabs. Creates an **integrated experience** that no collection of separate terminals can match.

### Session Sharing — [PRD](docs/roadmap/session-sharing.md)

> `quil serve --share` / `quil attach --host` for pair programming.

Remote workspace viewing and collaboration over TCP+TLS. Read-only by default, collaborative mode optional. tmux session sharing but with project context, typed panes, and AI session awareness.

---

## Priority Matrix

| Priority | Feature | Effort | Impact | Category |
|----------|---------|--------|--------|----------|
| ~~1~~ | ~~Pre-built binaries + one-line install~~ | ~~Small~~ | ~~Critical~~ | ~~Done~~ |
| 2 | "Holy Shit" demo GIF/video | Small | Critical | Growth |
| 3 | Project workspace files (`.quil.toml`) | Medium | Very High | Core |
| 4 | Command palette (`Ctrl+Shift+P`) | Medium | High | Core |
| ~~5~~ | ~~MCP server for AI integration~~ | ~~Medium~~ | ~~Very High~~ | ~~Done~~ |
| 5 | Notification center (sidebar + pane history) | Medium | High | Core |
| 6 | Community plugin registry + 10 plugins | Medium | High | Growth |
| 7 | Smart health monitoring + auto-restart | Medium | High | Advanced |
| 8 | tmux keybinding import | Small | Medium | Growth |
| 9 | Cross-pane context / event bus | Large | High | Advanced |
| 10 | Session sharing | Large | Medium | Advanced |

## Strategic Notes

### The Developer Pain (Layered)

| Layer | Pain | Who Feels It |
|-------|------|-------------|
| 1. Context destruction | Reboot = 10-15 min of manual reconstruction | Every multi-terminal developer |
| 2. AI session loss | Losing a Claude conversation means losing reasoning context worth hours | AI-native developers (growing fast) |
| 3. Project fragmentation | 5 terminals + 3 tools + 2 SSH = no single "project view" | Team leads, senior engineers |
| 4. Onboarding friction | "How do I run this?" → README with 8 terminal commands | New team members, OSS contributors |
| 5. Cross-tool blindness | AI assistant can't see the build error in the next pane | Everyone using AI coding tools |

Quil currently solves layers 1-3 well. **Layers 4-5 are where the breakout potential lives.**

Items 1-2 (install + demo) cost almost nothing and are **prerequisites for everything else**. Items 3 (workspace files) and 5 (MCP) are the **strategic differentiators** — workspace files create team adoption and MCP creates the "AI-native" moat that no other multiplexer can claim.

### Feature Synergies

The **notification center** (M12), **process health** (advanced), and **cross-pane events** (advanced) form a layered system:

| Layer | Feature | Role |
|-------|---------|------|
| UI | Notification Center (M12) | Sidebar, pane navigation, history stack |
| Monitoring | Process Health | Health states, auto-restart, stale detection |
| Orchestration | Cross-Pane Events | Event bus, pane-to-pane context passing |

M12 ships first as a standalone feature (process exit + output patterns). The other two extend it when they ship — health states and cross-pane events feed into the notification center's event queue.
