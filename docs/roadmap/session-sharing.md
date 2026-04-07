# Session Sharing for Pair Programming

| Field | Value |
|-------|-------|
| Priority | 10 |
| Effort | Large |
| Impact | Medium |
| Status | Proposed |
| Depends on | — |

## Problem

Remote pair programming with terminal tools is painful. Screen sharing is laggy and one-directional. tmux session sharing requires SSH access to the same machine. No existing tool combines terminal sharing with project context, typed panes, and AI session awareness.

## Proposed Solution

```bash
# Developer A (host)
quil serve --share --token abc123

# Developer B (remote)
quil attach --host dev-server:8080 --token abc123
```

Both developers see the same workspace. Read-only by default, collaborative mode optional. This is tmux session sharing but with project context, typed panes, and AI session awareness.

## User Experience

### Host Side

```bash
# Start sharing current workspace
$ quil serve --share --token mysecret
Sharing workspace on :8080
Token: mysecret
Waiting for connections...

# Or generate a random token
$ quil serve --share
Sharing workspace on :8080
Token: a7f3b2c1 (share this with your pair)

# With specific port
$ quil serve --share --port 9090 --token mysecret
```

Status bar shows: `[sharing: 1 viewer]`

### Remote Side

```bash
$ quil attach --host 192.168.1.50:8080 --token mysecret
Connected to workspace "my-saas-app" (read-only)
Press Ctrl+Q to disconnect
```

### Modes

| Mode | Behavior |
|------|----------|
| **Read-only** (default) | Remote sees all panes, can scroll, but cannot type or interact |
| **Collaborative** | Both can type, navigate tabs, split panes. Cursor shows who's where |
| **Follow** | Remote's view follows host's active pane/tab automatically |

```bash
# Enable collaborative mode
quil serve --share --token abc123 --mode collaborative
```

## Technical Approach

### 1. Network Protocol

Reuse the existing IPC protocol (length-prefixed JSON) over TCP instead of Unix sockets:

```
┌──────────┐     TCP + TLS      ┌──────────────┐
│  Remote   │ ◄──────────────► │  Host daemon  │
│  TUI      │  IPC messages     │  (quild)    │
└──────────┘  + auth layer      └──────────────┘
```

### 2. Authentication

- Token-based authentication (shared secret)
- TLS encryption for all traffic (self-signed cert auto-generated)
- Optional: OAuth/GitHub integration for team settings

### 3. State Synchronization

The host daemon already broadcasts state to connected TUI clients. Remote clients are just TUI clients connected over TCP instead of Unix socket:

- Same `MsgSync` workspace state messages
- Same `MsgPaneOutput` PTY output streaming
- Input messages (`MsgInput`) gated by mode (blocked in read-only)

### 4. Cursor Presence

In collaborative mode, show remote cursor position:
- Different cursor color per connected user
- "User B is viewing Tab 2, Pane: backend" indicator
- Active pane highlight shows who's focused where

### 5. Files

| File | Change |
|------|--------|
| `internal/daemon/share.go` | New — TCP listener, auth, remote client management |
| `internal/daemon/tls.go` | New — self-signed TLS cert generation |
| `internal/ipc/tcp.go` | New — TCP transport (reusing existing IPC protocol) |
| `cmd/quil/serve.go` | New — `serve --share` subcommand |
| `cmd/quil/attach.go` | New — `attach --host` subcommand |
| `internal/tui/model.go` | Presence indicators, mode restrictions |

## Success Criteria

- [ ] `quil serve --share` starts TCP listener with auth
- [ ] `quil attach --host` connects and shows remote workspace
- [ ] Read-only mode prevents remote input
- [ ] Collaborative mode allows both users to interact
- [ ] PTY output streams with acceptable latency (< 100ms LAN)
- [ ] TLS encryption on all traffic
- [ ] Status bar shows connection status and viewer count

## Open Questions

- NAT traversal: should Quil provide a relay server for WAN connections?
- Maximum number of concurrent viewers?
- Should shared sessions persist after host disconnects?
- Recording: save shared sessions as replayable recordings?
- Integration with VS Code Live Share or similar?
