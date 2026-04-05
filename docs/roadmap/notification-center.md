# Notification Center

| Field | Value |
|-------|-------|
| Priority | 4 |
| Effort | Medium |
| Impact | High |
| Status | Done |
| Depends on | M10 MCP Server (for AI event consumption). Phase 3 integrates with Process Health + Cross-Pane Events |

## Problem

Users run long-running processes in panes — AI assistants asking for confirmation, builds compiling, tests executing, webhooks waiting. Today the user must **manually poll** each pane to check if it needs attention. This forces a choice: either watch a pane (wasting time) or risk missing an important event (wasting the result).

This is the **context-switching tax** — the same class of problem Aethel solves for reboots, but for real-time multitasking within a session.

**Example workflow without notification center:**
1. Ask Claude Code to refactor auth module (pane 1)
2. Switch to terminal to work on something else (pane 2)
3. Periodically switch back to pane 1: "Is it done? Does it need confirmation?"
4. Repeat 5-10 times before Claude finishes
5. Miss the confirmation prompt, Claude sits idle for 10 minutes

**Example workflow with notification center:**
1. Ask Claude Code to refactor auth module (pane 1)
2. Switch to terminal to work on something else (pane 2)
3. Notification appears: "claude-code: Waiting for confirmation"
4. Press Enter → jump to pane 1, confirm
5. Press Alt+Backspace → jump back to pane 2, continue working

## Proposed Solution

Three components that ship incrementally:

### 1. Daemon: Event Emission

The daemon detects events and broadcasts them to connected TUI clients:

- **Process exit detection** — when a PTY process exits, emit an event with the exit code
- **Output pattern matching** — plugin TOML `[[notification_handlers]]` (parallel to existing `[[error_handlers]]`)
- **Event queue** — bounded (50 items), survives TUI disconnect/reconnect, replayed on attach

### 2. TUI: Notification Sidebar

A non-modal sidebar on the right edge (~30 columns) that coexists with normal pane rendering:

```
┌─ 1:AI + Code ─┬─ 2:Backend ─┬─ 3:Infra ────────┐
│                              │ ┌─ Notifications ─┐│
│  $ npm run dev               │ │                  ││
│  Server listening on :3000   │ │ ✖ claude-code  2m││
│                              │ │   Process failed ││
│                              │ │                  ││
│                              │ │ ⚠ backend     5m ││
│                              │ │   Needs input    ││
│                              │ │                  ││
│                              │ │ ℹ tests       8m ││
│                              │ │   All passed     ││
│                              │ │                  ││
│                              │ │ ↑↓ Nav  ⏎ Go     ││
│                              │ │ d Dismiss  D All ││
│                              │ └──────────────────┘│
├─ terminal · ~/project ───────┴─────────────────────┤
│ [3 events]  1:AI + Code | 2:Backend | 3:Infra      │
└─────────────────────────────────────────────────────┘
```

- **Auto-shows** when events arrive, toggleable via `Alt+N`
- **Not modal** — panes remain interactive; sidebar is just a visual panel
- **Focusable** — `Alt+N` or `Tab` moves focus to sidebar for navigation
- **Event items**: severity icon + pane name + title + relative timestamp
- **Status bar badge**: `[3 events]` when sidebar is hidden but events are pending

### 3. TUI: Pane History Stack ("Go Back")

A navigation history that enables the "work → jump → handle → return" loop:

- `Alt+Backspace` pops the history and navigates back (like browser back / `cd -`)
- History is pushed when navigating from a notification
- Bounded stack (20 entries), gracefully skips stale references (closed panes)
- **Works globally** — useful anytime the user jumps between panes, not just from notifications

## User Experience

### Keybindings

| Key | Context | Action |
|-----|---------|--------|
| `Alt+N` | Global | Toggle notification sidebar |
| `Alt+Backspace` | Global | Navigate back to previous pane |
| `Up/Down` | Sidebar focused | Navigate events |
| `Enter` | Sidebar focused | Go to linked pane (pushes current to history) |
| `d` | Sidebar focused | Dismiss selected event |
| `D` | Sidebar focused | Dismiss all events |
| `Esc` | Sidebar focused | Close/unfocus sidebar |

### Event Severity Icons

| Severity | Icon | Color | When |
|----------|------|-------|------|
| Error | `✖` | Red | Process exit code != 0 |
| Warning | `⚠` | Yellow | Output pattern: "waiting for confirmation/input" |
| Info | `ℹ` | Blue | Process exit code 0, build complete |

### Interaction with Other Features

- **Focus mode** (`Ctrl+E`): sidebar hidden in focus mode — events still accumulate, shown on exit
- **Dialogs**: sidebar hidden when a dialog is open (dialogs are modal)
- **Tab switching**: events are cross-tab — clicking an event may switch tabs

## Technical Approach

### 1. Process Exit Detection (Daemon)

Extend PTY `Session` interface with `Wait() (int, error)`:
- Unix (`session_unix.go`): `cmd.Wait()` → `ProcessState.ExitCode()`
- Windows (`session_windows.go`): `WaitForSingleObject` + `GetExitCodeProcess`

Wrap `streamPTYOutput` completion in `spawnPane`:
```go
go func() {
    d.streamPTYOutput(pane.ID, ptySession)
    d.handleProcessExit(pane.ID)  // emits PaneEvent
}()
```

### 2. IPC Messages

```go
// Daemon → Client
MsgPaneEvent = "pane_event"
type PaneEventPayload struct {
    ID        string            `json:"id"`
    PaneID    string            `json:"pane_id"`
    TabID     string            `json:"tab_id"`
    PaneName  string            `json:"pane_name"`
    Type      string            `json:"type"`      // "process_exit", "output_match"
    Title     string            `json:"title"`
    Message   string            `json:"message"`
    Severity  string            `json:"severity"`  // "info", "warning", "error"
    Timestamp int64             `json:"timestamp"`
    Data      map[string]string `json:"data"`
}

// Client → Daemon
MsgDismissEvent = "dismiss_event"
type DismissEventPayload struct {
    EventID string `json:"event_id"`  // empty = dismiss all
}
```

### 3. Daemon Event Queue

```go
// internal/daemon/event.go
type eventQueue struct {
    events []PaneEvent
    max    int        // 50
    mu     sync.Mutex
}
```

- `Push()` prepends (newest first), trims to max
- `Dismiss(eventID)` / `DismissAll()` for client requests
- Replayed on `handleAttach` after workspace state (like ghost buffers)
- `emitEvent()` pushes + broadcasts to all connected clients

### 4. Plugin Notification Handlers

```toml
# Parallel to existing [[error_handlers]]
[[notification_handlers]]
pattern = "(?i)waiting for (confirmation|input|approval)"
title = "Needs attention"
severity = "warning"

[[notification_handlers]]
pattern = "Build succeeded|All tests passed"
title = "Build complete"
severity = "info"
```

Follows exact same pattern as `ErrorHandler` — compiled regex, checked in `flushPaneOutput()`.

### 5. TUI Sidebar Rendering

`View()` integration — when sidebar is visible, pane area shrinks:

```go
tabContent := tab.View()  // rendered at (width - sidebarW)
if sidebarW > 0 {
    sidebar := m.notifications.View(sidebarW, tabH)
    tabContent = lipgloss.JoinHorizontal(lipgloss.Top, tabContent, sidebar)
}
```

`sidebarFocused` state routes keys to `handleNotificationKey()` instead of PTY.

### 6. Pane History Stack

```go
type PaneRef struct {
    TabIndex int
    PaneID   string
}
// Model.paneHistory []PaneRef — bounded stack (20)
```

`pushPaneHistory()` before notification navigation. `popPaneHistory()` on `Alt+Backspace`.

## Implementation Phases

### Phase 1: Foundation (standalone, no dependencies)
- Process exit detection (`Session.Wait()` on both platforms)
- `PaneEvent` struct + `eventQueue` in daemon
- `MsgPaneEvent` IPC message + TUI handler
- Basic `NotificationCenter` struct + sidebar rendering
- `Alt+N` toggle keybinding
- Pane history stack + `Alt+Backspace`
- Status bar badge

### Phase 2: Plugin Patterns
- `NotificationHandler` struct in plugin system
- `[[notification_handlers]]` TOML parsing
- `MatchNotification()` in `flushPaneOutput`
- Default patterns for claude-code plugin

### Phase 3: Integration (when related features ship)
- Consume events from cross-pane event bus
- Consume health state changes from process health system
- Tab bar event indicators
- Auto-show/hide behavior configuration

## Files to Change

| File | Action | Purpose |
|------|--------|---------|
| `internal/daemon/event.go` | **New** | PaneEvent, eventQueue, emitEvent |
| `internal/tui/notification.go` | **New** | NotificationCenter, View(), handleNotificationKey() |
| `internal/ipc/protocol.go` | Modify | MsgPaneEvent, MsgDismissEvent types + payloads |
| `internal/daemon/daemon.go` | Modify | Process exit detection, event emission, replay on attach |
| `internal/tui/model.go` | Modify | Sidebar in View(), pane history, keybindings, status bar badge |
| `internal/tui/styles.go` | Modify | Notification sidebar styles |
| `internal/config/config.go` | Modify | Keybindings (notification_toggle, go_back) + UI config |
| `internal/plugin/plugin.go` | Modify | NotificationHandler struct |
| `internal/plugin/scraper.go` | Modify | MatchNotification function |
| `internal/pty/session.go` | Modify | Add Wait/ExitCode to Session interface |
| `internal/pty/session_unix.go` | Modify | Implement Wait |
| `internal/pty/session_windows.go` | Modify | Implement Wait |
| `internal/plugin/defaults/claude-code.toml` | Modify | Add notification patterns |

## Success Criteria

- [ ] Process exit in any pane creates a notification
- [ ] `Alt+N` toggles the notification sidebar
- [ ] Selecting an event navigates to the linked pane (cross-tab if needed)
- [ ] `Alt+Backspace` returns to the previous pane
- [ ] Events survive TUI disconnect/reconnect (daemon queue)
- [ ] Plugin `[[notification_handlers]]` regex triggers notifications on output match
- [ ] Status bar shows event count when sidebar is hidden
- [ ] Sidebar is non-modal — panes remain interactive
- [ ] Focus mode hides sidebar; events accumulate
- [ ] MCP `watch_notifications` blocks until event fires for specified panes
- [ ] MCP `get_notifications` returns pending notifications without blocking
- [ ] AI can wait for process exit or output pattern match without polling

## MCP Integration: AI as Event Consumer

The Notification Center is not just a TUI feature — it's the **central event hub** that both the TUI sidebar and MCP-connected AI tools consume from. This eliminates the need for AI to poll panes.

### The Problem with Polling

Without notifications, AI must poll panes to check if work is done:
```
AI: send_keys(build_pane, ["npm test", "enter"])
AI: sleep(10)                               ← blind wait
AI: screenshot_pane(build_pane)             ← too early? too late?
AI: sleep(10)                               ← more polling
AI: screenshot_pane(build_pane)             ← finally done
```

### The Solution: `watch_notifications` MCP Tool

A blocking MCP tool that waits for events from the Notification Center:

```
AI: send_keys(build_pane, ["npm test", "enter"])
AI: result = watch_notifications(
        pane_ids=["build_pane"],
        timeout=120
    )
    → blocks until: process exits, output matches a notification pattern, or timeout
AI: result = {pane_id: "build_pane", event: "process_exit", exit_code: 1, title: "Tests failed"}
AI: screenshot_pane(build_pane)  ← exact moment, no waste
```

### Architecture

```
┌──────────────────────────────────────────┐
│         Daemon Event System              │
│                                          │
│  flushPaneOutput() ──► output watchers   │
│  streamPTYOutput() ──► exit watchers     │
│  plugin scrapers   ──► state watchers    │
│                                          │
│      ┌────────────────────────┐          │
│      │  Notification Center   │          │
│      │  (per-pane event queue)│          │
│      └──────┬─────────┬───────┘          │
│             │         │                  │
│   ┌─────────┘         └──────────┐       │
│   ▼                              ▼       │
│ TUI Sidebar (M12)          MCP Bridge    │
│ - visual notifications     - watch_notifications tool
│ - toast popups             - per-pane filtering
│ - Alt+N toggle             - blocking until event fires
│ - badge counts             - returns event details
└──────────────────────────────────────────┘
```

### MCP Tools for Notification Center

| Tool | Description |
|------|-------------|
| `watch_notifications` | Block until an event fires for specified pane IDs. Returns event details. |
| `get_notifications` | Non-blocking: return all pending notifications (for initial state check). |
| `subscribe_pane` | Register interest in a pane — daemon prioritizes event delivery for subscribed panes. |

### IPC Extension for MCP

```go
// MCP bridge → Daemon
MsgWatchNotificationsReq = "watch_notifications_req"
type WatchNotificationsReqPayload struct {
    PaneIDs []string      `json:"pane_ids"`
    Timeout time.Duration `json:"timeout"`
}

// Daemon → MCP bridge (when event fires or timeout)
MsgWatchNotificationsResp = "watch_notifications_resp"
type WatchNotificationsRespPayload struct {
    Event   *PaneEventPayload `json:"event,omitempty"` // nil on timeout
    Timeout bool              `json:"timeout"`
}
```

The daemon registers a one-shot watcher for the specified panes. When any event fires for those panes, it responds to the waiting MCP bridge with the event details.

### Example: AI Debugging Workflow

```
User: "The build pane seems stuck, check it"

AI: panes = list_panes()
    → finds build_pane = "pane-abc123"

AI: status = get_pane_status(build_pane)
    → running: true, type: "terminal"

AI: screenshot = screenshot_pane(build_pane)
    → sees: "npm test" running, output streaming

AI: result = watch_notifications(pane_ids=[build_pane], timeout=120)
    → blocks... daemon monitors pane events...
    → 45 seconds later: process exits with code 1
    → returns: {event: "process_exit", exit_code: 1, title: "Process failed"}

AI: screenshot = screenshot_pane(build_pane)
    → sees: "3 tests failed" output

AI: [reads test output, fixes the code, reruns tests]
```

### Implementation Notes

- `watch_notifications` uses the same `eventQueue` that the TUI sidebar consumes
- The MCP bridge timeout for `watch_notifications` must be longer than the default 10s (up to 5 minutes)
- Multiple `watch_notifications` calls can be active simultaneously (different panes)
- When the MCP bridge disconnects, all its watchers are automatically cleaned up

## Implementation Outcome

Implemented in v0.12.0. Key differences from the original PRD:

**Added beyond PRD:**
- **OSC 133 shell hooks** — bash, zsh, PowerShell emit command start/end markers for precise command completion detection with exit code. Zsh uses `local ec=$?` to capture exit code before other precmd functions clobber it. PowerShell uses `[char]0x1b` for 5.1 compat
- **Smart idle analysis** — moved from per-chunk regex matching (too noisy with arrow keys, command history) to idle-time analysis. `idleChecker()` goroutine ticks 1s, reads last 4KB at idle (5s no output), strips ANSI, matches against `[[idle_handlers]]`. 30s cooldown per pane
- **Plugin `[[idle_handlers]]`** — new TOML section (parallel to `[[error_handlers]]`) for context-aware idle notifications. Default patterns for terminal (confirmation, password, error), claude-code (waiting for input), ssh (confirmation, password)
- **Plugin `path` field** — explicit binary location bypasses PATH lookup. 3-tier detection: path → LookPath → searchBinary fallback (fixes Explorer-launched apps on Windows where PATH differs)
- **F3 keybinding** — focuses notification sidebar directly (in addition to Alt+N toggle)
- **Bell detection** — daemon detects BEL character (`\x07`) in PTY output with OSC-stripping regex and 30s cooldown per pane
- **Sidebar 3-state toggle** — Alt+N cycles: hidden → visible+unfocused → visible+focused → hidden (prevents keyboard input stealing)
- **`requestWithTimeout`** — MCP bridge helper with `time.NewTimer` + `defer timer.Stop()` for 5-minute waits

**Changed from PRD:**
- `[[notification_handlers]]` renamed/replaced by `[[idle_handlers]]` — patterns run at idle time, not on every output chunk
- `subscribe_pane` MCP tool was not implemented (not needed — `watch_notifications` accepts `pane_ids` filter)
- No auto-show behavior — user requested manual-only toggle
- `sync.Once` on daemon shutdown to prevent double-close panic

**Open Questions (resolved):**
- Bell detection: yes, daemon detects BEL with cooldown
- Sidebar width: fixed at 30 columns (hardcoded in `NewNotificationCenter`)
- Event expiry: no auto-expire; bounded queue (50) drops oldest
- Go-back scope: only notification navigation (not all pane switches)
- Dismissed events: disappear permanently (no history view)
