# EncodeFrame has no write-side frame cap

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/ipc/protocol.go` (`EncodeFrame`), read-side cap in `ReadMessage` |
| Found during | Code review of the IPC framing rewrite (2026-06-10) |
| Date | 2026-06-10 |

## Issue

`ReadMessage` rejects frames over 10 MB, but `EncodeFrame` will happily
produce one — the receiving peer then kills the connection with an opaque
"message too large", attributing the failure to the wrong side. (Pre-existing:
the old `WriteMessage` had no check either.) Also `uint32(len(data))` would
silently truncate a >4 GiB marshal, unreachable for current payloads.

## Risks

A future oversized payload (e.g. an unchunked buffer dump) surfaces as a
mysterious peer disconnect instead of a producer-side error.

## Suggested Solutions

Promote the 10 MB limit to a shared `maxFrameSize` const and add
`if len(data) > maxFrameSize { return nil, fmt.Errorf("frame too large: %d bytes", len(data)) }`
to `EncodeFrame`, failing fast at the producer.
