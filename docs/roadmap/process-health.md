# Smart Process Health & Auto-Restart

| Field | Value |
|-------|-------|
| Priority | 7 |
| Effort | Medium |
| Impact | High |
| Status | Proposed |
| Depends on | — |

## Problem

Quil currently restores sessions after reboot, but processes can crash or hang during normal operation too. Developers babysit long-running processes (build watchers, webhook listeners, SSH tunnels) — manually checking if they're still alive, restarting them when they die. This is the gap between "terminal organizer" and "workflow orchestrator."

## Proposed Solution

Elevate what's already partially in `error_handlers` to a first-class health monitoring system:

| Feature | Behavior |
|---------|----------|
| **Process health indicator** | Green/yellow/red dot on pane border based on process state |
| **Auto-restart with backoff** | Crashed panes restart after 1s → 2s → 4s → ... → max 60s |
| **Stale detection** | "No output for 5 minutes" warning for webhook/watcher panes |
| **Desktop notifications** | OS-native notification when a watched pane errors |
| **Cross-pane event bus** | "Build failed" event → available to other plugins |

Plugin authors would declare health rules in TOML:

```toml
[health]
auto_restart = true
max_restarts = 5
restart_backoff = "exponential"    # linear | exponential | fixed
stale_timeout = "5m"
notify_on_error = true
```

**Why this matters:** Moves Quil from "terminal organizer" to "workflow orchestrator." Developers stop babysitting processes.

## User Experience

### Visual Indicators

```
┌─ terminal (backend) ● ──────────────────┐   ← green dot = healthy
│ $ npm run dev                            │
│ Server listening on :3000                │
└──────────────────────────────────────────┘

┌─ terminal (tests) ○ ────────────────────┐   ← yellow dot = stale
│ $ npm test -- --watch                    │
│ (no output for 5 minutes)               │
└──────────────────────────────────────────┘

┌─ stripe ✖ ──────────────────────────────┐   ← red dot = crashed
│ Process exited with code 1              │
│ Restarting in 4s... (attempt 3/5)       │
└──────────────────────────────────────────┘
```

### Notifications

- Desktop notification: "Stripe webhook listener crashed (exit code 1). Auto-restarting..."
- Tab badge: red dot on tab header when any pane has errors
- Status bar: "2 healthy, 1 restarting" summary

## Technical Approach

### 1. Health State Machine

```
         spawn
    ┌────────────────┐
    │                │
    ▼                │
 STARTING ──────► HEALTHY ──────► STALE
    │                │               │
    │                ▼               │
    │            CRASHED ◄───────────┘
    │                │
    │                ▼
    │          RESTARTING ──► (back to STARTING)
    │                │
    │                ▼
    └──────────► DEAD (max restarts exceeded)
```

### 2. Daemon Changes

- `Pane.HealthState` field tracking current state
- Background goroutine per pane monitoring process exit
- Stale timer resets on every PTY output byte
- Restart logic with configurable backoff
- Notification dispatch (OS-native via `beeep` or similar Go library)

### 3. TUI Changes

- Health dot rendered in pane top border (colored Unicode circle)
- Tab badge when pane has error state
- Status bar health summary
- Restart countdown visible in crashed panes

### 4. Plugin TOML Extension

```toml
[health]
auto_restart = true           # restart on crash
max_restarts = 5              # give up after N restarts
restart_backoff = "exponential" # 1s, 2s, 4s, 8s...
max_backoff = "60s"           # cap backoff at 60s
stale_timeout = "5m"          # warn if no output for 5m
notify_on_error = true        # OS desktop notification
watched = true                # include in health summary
```

### 5. Files

| File | Change |
|------|--------|
| `internal/daemon/health.go` | New — health state machine, restart logic, stale detection |
| `internal/daemon/notify.go` | New — OS notification dispatch |
| `internal/daemon/session.go` | Integrate health monitoring into pane lifecycle |
| `internal/plugin/plugin.go` | Parse `[health]` section from TOML |
| `internal/tui/pane.go` | Render health indicators |
| `internal/tui/model.go` | Status bar health summary |

## Success Criteria

- [ ] Pane border shows green/yellow/red health indicator
- [ ] Crashed panes auto-restart with exponential backoff
- [ ] "No output for 5m" stale detection works
- [ ] Desktop notification fires on pane crash
- [ ] Plugin TOML `[health]` section is respected
- [ ] `max_restarts` prevents infinite restart loops
- [ ] Tab shows error badge when pane is unhealthy

## Open Questions

- Should health state persist across daemon restarts?
- How to handle processes that exit cleanly with code 0 — restart or not?
- Cross-pane event bus: separate feature or bundled here?
- OS notification library choice: `beeep`, `notify-send`, Win32 toast?
