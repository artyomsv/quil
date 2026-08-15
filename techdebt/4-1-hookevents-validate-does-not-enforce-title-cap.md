# Payload.Validate does not enforce MaxTitleBytes, so the documented cap is producer-only

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/hookevents/types.go` (`Payload.Validate`) |
| Found during | Security review of PR #162 (work-indicator start edges) |
| Date | 2026-08-15 |

## Issue

`hookevents` documents wire caps (title ≤ 200 bytes, data value ≤ 128, total ≤
2 KiB) and the producers honour them — every `spoolEvent` call site wraps its
title in `truncate(..., hookevents.MaxTitleBytes)`. But `Payload.Validate`, the
daemon-side gate that reads a spool line back, checks only schema version, pane
id, non-empty hook event, non-empty title, severity and source. It never
compares the title against `MaxTitleBytes`.

So the cap holds for anything Quil's own hook binary writes, and not at all for
a line appended to `$QUIL_HOME/events/<paneID>.jsonl` by anything else. The
spool is same-user writable and an agent running inside a pane knows both
`QUIL_PANE_ID` and `QUIL_HOOK_HOME` from its own environment. The only real
bound on such a title is the spool reader's 2 KiB line cap.

## Risks

Low as things stand, and lower than it looks. The rendering hazard this would
otherwise create was closed in PR #162: `internal/tui/notification.go` now runs
the title through `sanitizeRemoteText` before `truncateRunes`, so control
characters and bidi overrides cannot reach the frame regardless of length, and
the card is width-truncated anyway. What remains is a size/consistency gap
rather than an injection one — an oversized title occupies queue and snapshot
space it was documented not to.

## Fix sketch

Not simply "add the check". `Validate` returning an error means the daemon
DROPS the event, and dropping is the wrong direction for this failure: a
permission prompt with an overlong title must still surface. The fix wants to be
truncate-on-ingest rather than reject-on-ingest, which makes it a mutation and
therefore not `Validate`'s job as currently shaped — probably a normalise step
in the spool reader, with `Validate` left as the pure predicate it is.

## Notes

Deliberately not fixed in PR #162. The security review that found it rated the
render gap MEDIUM (fixed there, since that PR widened it by spooling a
`StopFailure` reason) and this half separately, as a backstop that does not hold.
It needs the reject-vs-truncate decision made explicitly rather than bolted onto
a validator.
