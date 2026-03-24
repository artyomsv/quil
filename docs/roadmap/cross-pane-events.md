# Cross-Pane Context Awareness

| Field | Value |
|-------|-------|
| Priority | 9 |
| Effort | Large |
| Impact | High |
| Status | Proposed |
| Depends on | Smart Process Health (recommended) |

## Problem

Aethel panes are isolated — they can't react to events in other panes. A build failure in one pane doesn't notify the AI assistant in another. An SSH disconnect doesn't trigger reconnection. Test results don't update tab badges. The developer is still the message bus between their tools.

This creates an **integrated experience** that no collection of separate terminals can match.

## Proposed Solution

Let panes react to events in other panes:

| Event | Reaction |
|-------|----------|
| **Build fails** | AI pane gets a toast: "Build error detected. Send to Claude?" → one keypress sends the error context |
| **SSH disconnects** | Auto-reconnect with backoff |
| **Test passes** | Green flash on the tab, optional desktop notification |
| **Webhook received** | Counter badge on the tab: `Stripe (47)` |

## User Experience

### Toast Notifications

```
┌─ claude-code ────────────────────────────┐
│ claude > I've updated the auth           │
│ middleware to use JWT tokens...           │
│                                          │
│ ┌─────────────────────────────────────┐  │
│ │ ⚠ Build error in "backend" pane     │  │
│ │ Press Enter to send context to AI   │  │
│ └─────────────────────────────────────┘  │
└──────────────────────────────────────────┘
```

### Tab Badges

```
┌─ 1:AI + Code ─┬─ 2:Backend ─┬─ 3:Stripe (12) ─┐
```

### Event-Driven Plugin Rules

Plugins can subscribe to events via TOML:

```toml
[events]
on_exit_nonzero = "notify"           # show toast in other panes
on_output_match = "FAIL|ERROR"       # regex trigger
on_output_match_action = "badge"     # increment tab badge
on_disconnect = "reconnect"          # auto-reconnect behavior
```

## Technical Approach

### 1. Event Bus (Daemon-Side)

```go
type PaneEvent struct {
    SourcePane string
    Type       EventType  // Exit, OutputMatch, Disconnect, Stale
    Data       map[string]string
    Timestamp  time.Time
}

type EventBus struct {
    subscribers map[string][]EventHandler
    ch          chan PaneEvent
}
```

Events are emitted by:
- Process exit monitor (exit code != 0)
- PTY output regex scanner (configurable patterns)
- Connection state changes (SSH disconnect)
- Stale timer expiry

### 2. Event Routing

Events flow: `Pane PTY output → regex match → EventBus → subscriber panes → TUI notification`

Subscriptions can be:
- **Plugin-level**: defined in TOML (`[events]` section)
- **Pane-level**: configured per-pane at runtime
- **Global**: all panes receive certain events (build failures)

### 3. TUI Toast System

- Toast overlay rendered above pane content
- Auto-dismiss after 5s or on keypress
- Action key (Enter) sends event context to target pane
- Toast queue for multiple simultaneous events

### 4. Tab Badges

- Counter badge in tab header: `Stripe (47)`
- Color-coded: red for errors, green for success, grey for info
- Cleared on tab focus or manually

### 5. Files

| File | Change |
|------|--------|
| `internal/daemon/eventbus.go` | New — event bus, subscribers, routing |
| `internal/daemon/scanner.go` | New — PTY output regex scanner for events |
| `internal/daemon/session.go` | Emit events on process exit, disconnect |
| `internal/plugin/plugin.go` | Parse `[events]` section from TOML |
| `internal/tui/toast.go` | New — toast notification overlay |
| `internal/tui/model.go` | Handle event IPC messages, render toasts, tab badges |
| `internal/ipc/messages.go` | New message types: `MsgPaneEvent`, `MsgToast` |

## Success Criteria

- [ ] Build failure in one pane shows toast in AI pane
- [ ] Toast action key sends error context to AI pane
- [ ] Tab badges show event counts
- [ ] SSH panes auto-reconnect on disconnect
- [ ] Plugin TOML `[events]` section is respected
- [ ] Events are logged for debugging

## Open Questions

- Should events cross tab boundaries or only within same tab?
- Event history / replay for debugging?
- Should MCP server expose events as a tool? (`subscribe_to_events`)
- Rate limiting on high-frequency events (e.g., test runner output)?
- Security: can any pane send input to any other pane via events?
