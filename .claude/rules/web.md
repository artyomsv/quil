---
description: The browser client (ADR-35) — the gateway's allow-list, flow control, login code and key, the shared vector files, the embedded page. Load when touching any of them.
paths:
  - internal/webgw/**
  - cmd/quil/web*.go
  - web/**
---

# Browser client (`quil web`)

User guide: `docs/web.md`. Design: ADR-35 in `docs/architecture.md`. The gateway is a thin proxy: one daemon connection per browser tab, and **no daemon change**. If a task seems to need one, stop and ask.

## Invariants

- **The allow-list is the page's whole power** (`forwardable` in `internal/webgw/allow.go`). In the default mode each tab is a full-rights local client. Add a type only together with the UI that uses it, and never a token, shutdown, kill, create or destroy type without a deliberate decision. `pane_input`, `resize_panes` and `client_geometry` are sent id-less whatever the page set; the gateway overwrites the page's hello process fields with its own.
- **Never pause the daemon reader** (`bridge.run`). A paused reader lets the daemon's 64-slot must-deliver queue for that connection fill, and the daemon closes it. Slowness is handled by the replay buffer, the 2 MiB live-unacknowledged limit and 4002, never by back-pressure on the read.
- **Every close except 4001 sends a flushed `detach`** before the daemon connection closes (a queued frame is discarded by a close), so a held size-master slot is released at once. 4001 keeps the daemon connection for the resync lease and the page re-attaches on it.
- **Follower and unpaintable tabs never resize.** The page sends `resize_panes` only as a confirmed master with a window of at least 40 by 10 cells; a read-only tab sends nothing at all (`web/src/lib/sizing.ts`).
- **Client ids are leased** (`internal/webgw/lease.go`): honoured only if this gateway minted the id for the same login session and no live tab holds it. The id is per browser tab (sessionStorage), never per origin.
- **The login code is never in a URL, argv or a log.** Neither is the key. The key is checked in `web_open` BEFORE any daemon dial, with close 1008 "login required"; the cookie alone opens nothing, because cookies are not isolated by port. A wrong code never invalidates the right one. A wrong code holds its login slot through the delay on purpose (that caps guessing); do not "fix" the lock-out by releasing the slot early.
- **Host is loopback only, Origin is an exact match, the CSP is exactly the one in `internal/webgw/checks.go`.** Loosening one needs a reason in the PR.
- **Close codes are a contract** with `web/src/lib/protocol.ts` and `banner.ts`: 4001 resync, 4002 too slow, 4003 daemon unavailable, 4004 token refused, 4005 version mismatch, 4006 closed by an agent, 1001 going away, 1008 refused / login required, and 1008 "replaced by a newer connection" (`closeReplaced` in `server.go`, `CLOSE_REPLACED_REASON` in `protocol.ts`; a test keeps them equal), the one 1008 the page retries with back-off. The page sanitizes every close reason, since it may relay the daemon's words.
- **Pane output is binary frames** (`internal/webgw/frame.go` and `web/src/lib/frame.ts`). Whoever receives a frame and does not write it to a terminal must still report the bytes as processed, or the gateway's unacknowledged count never drains.
- **The page's terminals answer no queries** (`web/src/lib/queries.ts`), as the TUI answers none: a terminal parses replayed history too, so a reply would be fresh input to the pane.
- **Every terminal grid change is queued in the pane's write chain** (`TerminalStore.resize`), for hidden panes too, from the daemon's sizes as each state or `pane_sizes` arrives. A resize applied out of order lets output parse at the wrong size, which no later resize repairs.
- **The shared vector files are the contract for both languages**: `internal/webgw/testdata/frame_vectors.json` (frames) and `internal/layouttree/testdata/layout_vectors.json` (layout). Change a vector and both test suites together.
- **The page is embedded** from `internal/webgw/dist`. A checkout with no build serves a notice (`HasUI` is false). `dev.sh cross`, `dev.sh image` and the Dockerfile build without the page; `docs/web.md` says so.

## Working here

No local builds, tests or linters: CI runs `go test`, `go vet` and the web job on the pull request. Format Go with gofmt only. Edit TypeScript and Svelte with 2-space indentation and `interface` for object shapes.
