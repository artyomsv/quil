# No read buffering: 2 syscalls + 2 allocations per IPC message

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Trivial |
| Location | `internal/ipc/protocol.go:403-423` (ReadMessage), `internal/ipc/server.go` Conn.Receive, `internal/ipc/client.go` |
| Found during | Whole-project performance audit |
| Date | 2026-06-10 |

## Issue

Each message read = `binary.Read` of the 4-byte length prefix (one syscall +
an internal 4-byte allocation per call) + `io.ReadFull` (second syscall) +
`make([]byte, length)`. The daemon pays this per keystroke (`MsgPaneInput`);
every client (TUI, MCP bridge) pays it per `MsgPaneOutput` frame — hundreds
per second under streaming load.

## Risks

Avoidable syscall + alloc overhead on the hottest IPC path; grows linearly
with output rate and client count.

## Suggested Solutions

Wrap each conn's read side in a `bufio.Reader` (read path is single-goroutine
per conn) and read the prefix with `io.ReadFull(r, lenBuf[:4])` into a
per-conn array. Halves read syscalls, removes the per-message 4-byte alloc.

Related (separate, larger): the write path copies each broadcast frame ~4×
(payload marshal → envelope re-marshal → bytes.Buffer → slices.Clone);
building the wire frame in a single allocation in `WriteMessage`/`Broadcast`
removes two of the copies. The MCP bridge also receives and discards the full
pane-output broadcast stream (`cmd/quil/mcp.go` readLoop drops `ID == ""`
messages) — an opt-in output subscription flag on attach would cut daemon
write volume per MCP client.
