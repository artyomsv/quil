---
name: project_subscribe_broadcast_wiring_gap
description: fix/performance branch's MsgSubscribe/wantsFrame opt-out (internal/ipc, internal/daemon, cmd/quil/mcp.go) — predicate is unit-tested but nothing exercises Broadcast skipping a real conn or the daemon dispatch arm for MsgSubscribe
metadata:
  type: project
---

Round-1 QA of branch `fix/performance` (2026-08-18) found the same shape of gap
as [[project_ipc_write_window_wiring_gap]] and [[project_notify_wiring_gap]],
in a new feature: the pane-output opt-out.

`internal/ipc/server.go` adds `Conn.noPaneOutput` (atomic bool), `wantsFrame`,
`SetPaneOutputWanted`, and a skip check inside `Server.Broadcast`.
`internal/daemon/daemon.go` adds `handleSubscribe` and a `case ipc.MsgSubscribe`
dispatch arm. `cmd/quil/mcp.go` sends the opt-out message once at bridge
startup via `bridge.sendRaw`.

**What's tested:** `internal/ipc/subscribe_test.go` is thorough on the
*predicate* — `wantsPaneOutput`/`wantsFrame`/`setPaneOutputWanted` — built
directly on a bare `newConn(local)` from `net.Pipe`, no `Server` involved.

**What's NOT tested, and is easy to add given existing patterns:**
- No test calls `srv.Broadcast(...)` against a real `*Server` with an
  opted-out conn and asserts the frame never arrives. The package already has
  this exact harness shape in `broadcast_resilience_test.go` / `lossy_test.go`
  / `transport_test.go` (`srv.Broadcast(msg)` against a live server + real
  conns) — reusing it for the subscribe case is a small addition, not new
  infrastructure.
- No test drives `d.handleMessage(conn, subscribeMsg)` on a live daemon to
  confirm `MsgSubscribe` is wired into the dispatch switch. The daemon package
  already tests dispatch wiring this way (`d.handleMessage(nil, msg)` in
  `project_test.go`; a real conn via `d.server = ipc.NewServer(...)` in
  `overlay_attached_test.go`) — `handleSubscribe` needs a real `*ipc.Conn`
  (calls `conn.SetPaneOutputWanted`), so the `nil`-conn shortcut those tests
  use doesn't apply; it needs the `overlay_attached_test.go`-style real-conn
  setup instead.
- No test in `cmd/quil` (`mcp_tools_test.go` is the only mcp test file)
  confirms the bridge actually calls `sendRaw` with a `MsgSubscribe` payload
  at startup.

None of these are currently pinned by a mutation-style check — I did not run
mutation testing this round, just confirmed by grep that no test references
`MsgSubscribe`, `handleSubscribe`, `SetPaneOutputWanted`, `wantsFrame`, or
`SubscribePayload` outside the predicate test file.

**How to apply:** when reviewing this feature again (or the next
`case ipc.Msg*` dispatch arm anywhere in `daemon.go`), check whether a test
drives the *dispatch switch*, not just the handler function or the predicate
it configures. See [[project_test_tooling]] for the mutation-testing recipe if
verifying whether the suite would actually catch the arm being deleted.

## Resolved (2026-08-18, same branch, before merge)

All three gaps are now pinned, each verified by the mutation the note asks for:

- `internal/ipc/subscribe_broadcast_test.go` —
  `TestBroadcast_SkipsPaneOutputForOptedOutConnOnly`: real `Server`, two real
  clients. Mutation `if false && !c.wantsFrame(...)` → fails.
- `internal/daemon/subscribe_wiring_test.go` —
  `TestHandleSubscribe_DispatchArmStopsPaneOutputForThatClientOnly`: real socket
  via the `overlayServerDaemon` harness, exactly as this note predicted was
  needed. Mutation deleting the `case ipc.MsgSubscribe:` arm → fails.
- `cmd/quil/mcp_subscribe_test.go` —
  `TestMCPBridge_DeclinePaneOutputSendsSubscribeOptOut`. The send was extracted
  from `runMCP` into `mcpBridge.declinePaneOutput` to be reachable at all;
  `runMCP` builds an MCP server over stdio and blocks.

Each also asserts the opt-out is PER-CONNECTION — a filter that silenced every
client would satisfy the primary assertion on its own.

The generalisable lesson stands and is why this note is kept rather than
deleted: the predicate tests here were thorough and still proved nothing about
reachability. Test the dispatch switch, not the handler.
