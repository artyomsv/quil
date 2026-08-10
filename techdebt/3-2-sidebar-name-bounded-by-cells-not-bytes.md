# Sidebar row builders bound remote text by CELLS, never by bytes

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/tui/sidebar.go` (`truncateCells`), reached from `projectRow`, `paneRow`, `gitRow`, `sidebarTabHeading` |
| Found during | Security review of PR #146 (project sidebar badge colouring) |
| Date | 2026-08-09 |

## Issue

`truncateCells` opens with a width-only early return:

```go
if lipgloss.Width(s) <= w {
    return s
}
```

A string of printable ZERO-WIDTH codepoints measures 0 cells however long it is,
so that branch hands the whole string back untouched. `sanitizeRemoteText`
deliberately does not help: it is a control-character filter, and U+200B ZERO
WIDTH SPACE is neither C0/C1/DEL nor a bidi override, so it is preserved
byte-identically by design.

A remote daemon therefore names a project with 5 MB of U+200B and every sidebar
row builder passes it through whole, into a `Style.Render` that allocates a
multi-megabyte styled string, once per frame — against a strip repainted on
every message including the 100 ms spinner tick.

The row still measures **exactly `w` cells**, which is why nothing catches it:
`renderSidebar`'s closing `.Width(w)` is satisfied, and
`TestSidebarRows_MeasureExactlyTheirWidth` passes. `remoteTextSamples` already
carries hostile inputs and `TestCellCutters_SurviveAZeroWidthFlood` already
covers a zero-width flood — but that test asserts the cutters are LINEAR rather
than BOUNDING, which is the distinction this debt is about. The 2026-07 fix made
the walk cheap; it never made the output small.

Pre-existing and untouched by PR #146: the old single-style `projectRow` reached
the same early return through `padOrTrunc`, and `renderStyledSegments` inherits
the property unchanged without adding amplification.

## Risks

- Remote-driven memory amplification on a render path. Reachable by anyone who
  can write the far host's `workspace.json` or speak to its socket — not only by
  full host compromise — and quil's whole `--remote` premise is that the daemon
  is a machine the user may not fully control.
- The daemon runs for weeks. A name that survives one frame survives every
  frame, so this is sustained pressure rather than a spike.
- Invisible to every existing assertion, since the failure preserves the one
  property the sidebar's tests are built around.

## Suggested Solutions

1. Give `truncateCells` a BYTE budget alongside its cell budget, so both the
   early return and the cluster loop stop on `len(s)`. One place, covers every
   row builder, but it changes a function four callers depend on and would want
   its own sweep.
2. Cap `p.Name` at the `sidebarRows` call site before it reaches `projectRow`,
   mirroring `formMsgNameCap` (`internal/tui/projectdialog.go`) — the codebase
   already caps exactly this class of value for the project form's message line.
   Narrower, but leaves `paneRow` / `gitRow` / `sidebarTabHeading` uncovered.
3. Bound it in `sanitizeRemoteText` instead. Rejected on first reading: that
   function's contract is explicitly "control-character filter, not a bounding
   pass", and several call sites depend on it not shortening anything.
