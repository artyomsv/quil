# Alt+R can only promote to `--resume` when the hook recorded a transcript path

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/daemon.go:4728` (`locatedOwnSession`), `:4856` (`transcriptState`), `:5287-5300` (`spawnPane` promotion), `:4349-4360` (the removed CWD probe) |
| Found during | Investigation of a production incident, 2026-08-11; rewritten 2026-09-19 after issue #221 |
| Date | 2026-09-19 |

## History

The original entry described Alt+R on a claude-code pane re-passing
`--session-id` for a session that already had a transcript, which Claude refuses
("Session ID … is already in use", exit 129), leaving the pane dead on every
retry.

**That bug was fixed in v1.66.3 (PR #200)** — see `CHANGELOG.md`. `handleRestartPaneReq`
still calls `spawnPane(…, restoring=false)`, and `resumeTemplateFor` is still
unreachable from the restart path for `preassign_id`; the fix instead added a
promotion step inside `spawnPane` (`:5287-5300`) that calls `locatedOwnSession`
and rewrites `resumeID` before `resolveSpawnArgs` chooses the flag.

This entry is retained, downgraded, and rewritten for the **residual**: the
promotion has a precondition that is not always met, and when it is not met the
original failure returns unchanged.

## Issue

`locatedOwnSession` (`:4728`) promotes only when the top candidate's state is
`candidateLocated`. `transcriptState` (`:4856`) reaches that state only when the
candidate carries a recorded transcript path whose basename is `<id>.jsonl` and
which `stat`s. An empty path yields `candidateUnknown`, never `candidateLocated`.

`transcript_path` has exactly one provenance — the Claude hook record. Every
write of it (`daemon.go:540`, `:4987`, `:5315`) copies `rec.TranscriptPath` or
the hook id/path pair. The CWD-derived probe that could have supplied it
independently was deliberately removed (`:4349-4360`) because a moved worktree
made it answer about the wrong directory.

So in any environment where the hook does not fire — the shape reported in
issue #221 — the v1.66.3 promotion cannot engage, `resolveSpawnArgs` takes its
`!restoring && preassign_id` branch (`:5033-5041`), expands
`--session-id {session_id}` from the pane's existing, lived-in id, and Claude
refuses it exactly as before.

Note that `session_id` itself is minted by Quil at pane creation (`:5279-5283`,
`uuid.New()`), not by the hook. A pane therefore always HAS an id to re-pass.
The hook's contribution is rotation tracking plus the transcript path that the
located-check needs — which is why a hookless pane fails this way rather than
starting cleanly.

## Risks

- Alt+R — the remedy the pane's own error screen advertises — is again a
  guaranteed failure for a lived-in claude-code pane, but only on installs
  where the hook is not firing. The failure is therefore intermittent across
  users and does not reproduce on a healthy machine, which makes it expensive
  to diagnose from a bug report.
- The dependency is invisible at the call site. `locatedOwnSession` reads as a
  general "does this pane's session exist" check; nothing there says the answer
  is `false` whenever hooks are off.
- The two subsystems are coupled without a test that says so. A future change
  that narrows hook coverage further would silently re-break restart.

## Suggested Solutions

1. **Positive-only CWD fallback in `locatedOwnSession`.** When the top candidate
   has no recorded path, try `claudesessions.TranscriptPath(pane.CWD, id)`
   (`internal/claudesessions/claudesessions.go:198`). Treat a hit as promotion
   evidence and a miss as `candidateUnknown` — never as `candidateMissing`.
   Because a miss is never read as absence, this does not reintroduce the
   moved-worktree hazard that motivated removing the probe at `:4349-4360`.
2. **Refuse rather than re-pass.** If the promotion declines and the pane has a
   non-empty `session_id`, log why and spawn without `--session-id` at all
   rather than with an id Claude will reject. A fresh conversation is a worse
   outcome than a resumed one but a better one than a dead pane.
3. **Test the coupling.** Add a `restoring=false` case to
   `internal/daemon/spawn_args_test.go` covering a pane that carries
   `session_id` but NO `transcript_path` and no hook record — today the only
   `restoring=false` case there passes an empty `Pane{}`.
