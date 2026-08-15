# Browser UI — Alternative Front-End

| Field | Value |
|-------|-------|
| Priority | — |
| Effort | Large |
| Impact | High |
| Status | **Investigated — viable, not scheduled** |
| Investigated | 2026-08-15 |
| Depends on | IPC frame encoding (recommended, not blocking) |

## Question

Can the current architecture support a browser-based UI alongside (or instead of) the
Bubble Tea TUI, and what are the options?

## Verdict

**Yes, and the architecture is unusually well suited to it** — largely by accident of
decisions made for other reasons. Three blockers exist, none of them fundamental. The
recommended shape is a `quil web` bridge serving xterm.js, built in stages.

## Why the existing architecture already supports this

Six facts, each verified against the code:

1. **The daemon streams raw PTY bytes; VT emulation is client-side.**
   `PaneOutputPayload.Data []byte` carries unmodified terminal output. The
   `charmbracelet/x/vt` emulator is instantiated per-pane in `internal/tui/pane.go` —
   in the *client*. The daemon touches it in exactly one place, `screenshot_pane`.
   **xterm.js consumes precisely this byte stream**, so a browser client needs no change
   to the daemon's streaming model at all. This is the single largest enabler.

2. **`quil mcp` is a working proof of the second-client pattern.** It is a process that
   dials the daemon socket via `ipc.NewClient`, translates a foreign protocol
   (JSON-RPC/MCP), and coexists with a running TUI. A `quil web` subcommand is the same
   shape with a different protocol on the far side.

3. **The IPC client is already transport-agnostic.**
   `ipc.NewClientWithDialer(ctx, DialFunc)` exists and is proven in production by the ssh
   `stdioConn` in `internal/transport`.

4. **State and replay on attach are solved.** `MsgWorkspaceState` delivers the full
   project/tab/pane tree; ghost buffers deliver scrollback. A new client type inherits
   both with no new daemon code.

5. **Request/response correlation exists.** `Message.ID` and `respondTo` already support
   a client asking targeted questions, which is how every MCP tool works.

6. **The layout tree is already language-neutral.** `internal/tui/layout.go`'s binary
   split tree is serialized to JSON and persisted in `Tab.Layout`, so a browser client
   reads the same structure the TUI does rather than inventing its own.

## Blockers

| # | Blocker | Detail | Cost to resolve |
|---|---|---|---|
| 1 | **Resize contention** | `handleResizePane` is last-writer-wins, and the TUI re-sends every pane's size on *every* workspace broadcast (see `Pane.appliedCols` in `internal/daemon/session.go`). A second client at different geometry fights it continuously — each flip re-applies the PTY size and fires `repaintAfterResize`. | Protocol change: per-client geometry with daemon-side arbitration (tmux shrinks to the smallest attached client). Or sidestep it — see Stage 1. |
| 2 | **No authentication** | The socket trust model is `chmod 0600` and nothing else (`internal/ipc/server.go`). Any IPC client can `create_pane` with arbitrary `instance_args`, i.e. spawn arbitrary processes. Exposing this over HTTP/WS without an auth layer is remote code execution. | Bearer token minted per gateway start, loopback-only bind, `Origin` check. Non-negotiable before any network exposure. |
| 3 | **UI logic lives in the TUI** | 33k LOC in `internal/tui`: dialogs 5.4k, palette 1.1k, sidebar 1.4k, layout 823, plus selection, keymatch, notes, editor. The daemon supplies state and bytes; everything else is client-side. | This is the actual project. Scope it deliberately rather than aiming for parity. |

Blocker 1 is a consequence of a deliberate design choice rather than an oversight: the
daemon owns exactly one PTY per pane, so there is exactly one true geometry. Every
multi-client terminal hits this.

Note also that the JSON frame cost measured in
[`daemon-language-rewrite.md`](./daemon-language-rewrite.md) hits a browser client
*harder* than the TUI — base64 adds 33% on the wire and the decode happens in JS. The
frame-encoding fix is a prerequisite that pays for itself twice.

## Options

### A. `quil web` bridge + xterm.js — recommended

A new subcommand shaped like `quil mcp`: dials the daemon socket, serves a static SPA and
a WebSocket, mints an auth token on startup.

- **Cost:** ~800–1500 LOC of Go for the gateway; the SPA is the real work.
- **Wins:** zero daemon changes for streaming. Works remotely for free over `ssh -L`.
  Unlocks what a terminal cannot do — inline images, real hyperlinks, drag-and-drop
  layout, side-by-side diffs, a genuinely rich notification center.
- **Risks:** blockers 1 and 2 above; two front-ends to maintain thereafter.

### B. Tauri or Electron shell over option A

Same gateway, packaged as a desktop app. Adds a native window and makes auth easier
(loopback plus native IPC).

- **Cost:** A, plus packaging and a second release pipeline. Tauri ≈5 MB plus the system
  webview; Electron ≈150 MB.
- **Verdict:** only worth it as a *replacement* for the TUI, not as a companion. Defer.

### C. Daemon-side VT, ship cell diffs

The daemon runs `x/vt` per pane (it already can) and broadcasts grid diffs instead of
bytes.

- **Cost:** the daemon performs per-pane rendering continuously instead of only on
  `screenshot_pane`; per-pane memory grows; and diffs are typically *larger* than the raw
  byte stream for streaming output.
- **Verdict:** **not recommended.** It discards the client-owns-VT design that keeps the
  daemon cheap, and it does not solve blocker 1 — the PTY still has one size.

## Staged path for option A

- **Stage 0** — land the IPC frame encoding fix. Independent value; do it regardless.
- **Stage 1** — `quil web`, **read-only**: attach, replay the ghost buffer, stream to
  xterm.js. No input, no resize sends. This sidesteps blocker 1 entirely and most of
  blocker 2, while proving the whole transport. Small.
- **Stage 2** — input (reusing the ordered-queue discipline from
  `Model.enqueueInput`; a browser client must not race frames either), auth token, and
  the resize-arbitration protocol change.
- **Stage 3** — layout tree, dialogs, palette, sidebar. This is where the 33k LOC bill
  comes due; pick a subset on purpose.

## Related

- [`daemon-language-rewrite.md`](./daemon-language-rewrite.md) — the other half of the
  same investigation.
- [`session-sharing.md`](./session-sharing.md) — overlaps on transport and auth; a web
  gateway is one way to deliver it.
- [`remote-daemon.md`](./remote-daemon.md) — the shipped ssh transport this would reuse.
