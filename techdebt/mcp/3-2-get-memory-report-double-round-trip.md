# `get_memory_report` issues two sequential IPC round-trips

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `cmd/quil/mcp_tools.go` — `registerGetMemoryReportTool` |
| Found during | Final code review of memory reporting feature (commits 1dd5d6a..c9becf4) |
| Date | 2026-04-20 |

## Issue

The `get_memory_report` MCP tool calls `bridge.request` twice in sequence:

1. `MsgMemoryReportReq` to fetch the per-pane memory snapshot.
2. `MsgListTabsReq` to resolve tab IDs to tab names.

Both requests are bounded by `mcpRequestTimeout` (10 s), so an agent calling
the tool waits up to 20 s in the worst case. The second round-trip exists
solely to enrich `TabID`s with human-readable names.

## Risks

- **Latency doubling** on every agent call. For agents that poll or batch
  memory reports, this compounds.
- **Race window**: tabs may be created / destroyed between the two IPC
  calls. The tool handles the orphan case (pane whose tab was just
  destroyed) by falling back to `TabID` as the display name, but the two
  snapshots are no longer consistent.
- **Duplicated code path**: every future MCP tool that needs tab names
  alongside some other payload will repeat the pattern.

## Suggested Solutions

**Option A (preferred)** — add `Tabs []ipc.TabInfo` to
`MemoryReportRespPayload` in `internal/ipc/protocol.go`. The daemon's
`handleMemoryReportReq` already has access to the session manager; populating
the tab list costs nothing extra. Remove the second round-trip from the MCP
tool. The wire format gains a few dozen bytes per response; the latency
halves.

**Option B** — cache the tab list in the MCP bridge with a short TTL
(~5 s). Avoids a wire change but introduces bridge-side state and a
stale-read window. Worse than Option A.

Both options are single-digit line counts plus a small test update.
