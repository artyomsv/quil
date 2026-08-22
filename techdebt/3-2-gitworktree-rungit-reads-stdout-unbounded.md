# gitworktree.runGit buffers git stdout with no limit

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/gitworktree/gitworktree.go:63` (`runGit`), consumed by `List` (`:102`) and `Branches` (`branches.go:48`) |
| Found during | Security review of PR #185 (worktree preparation) |
| Date | 2026-08-23 |

## Issue

`runGit` runs `cmd.Output()`, which buffers the whole of stdout into memory with
no cap. Only **stderr** is bounded — Go's `Output()` captures it through a
`prefixSuffixSaver` limited to ~32 KiB. Nothing bounds stdout.

Two callers read listings whose size is a property of the repository rather than
of the request:

- `List` — `git worktree list --porcelain`, a few lines per worktree.
- `Branches` — `git for-each-ref refs/heads`, **one line per local branch**.

`Branches` applies `maxBranchList` (2000) while scanning, so what it *retains* is
bounded — PR #185 moved that cap ahead of the split precisely so it bounds the
allocation too. But the listing is still fully **read** before the scan sees a
byte of it.

The `dir` those commands run in comes straight off the wire
(`WorktreeListReqPayload.Path`, unvalidated), and the RPC is reachable by any IPC
client — a pane's own child runs as the same user with the socket right there.

## Risks

A repository with a very large `packed-refs` reaches this: a mirror clone of a
large upstream, or one an agent in a pane creates deliberately (a million refs is
a single file write). ~30 MB of refs means ~30 MB buffered in the daemon in one
burst.

Bounded by `worktreeListTimeout` (10 s) and serialised by the `worktreeScanning`
single-flight, so it is a repeatable spike rather than a leak. But the daemon
hosts **every pane on the machine** and runs for weeks, so an OOM there takes
down every session including panes doing unrelated work.

Note this is **pre-existing** — `List` has had the property since stage A.
PR #185 did not introduce it; it made it worth writing down by adding a second
caller whose output scales with the repository.

## Suggested Solutions

1. **A `Branches`-local read limit.** Wrap stdout in an `io.LimitedReader` at
   roughly `maxBranchList * (maxBranchLen+1)` bytes. Scoped, and safe because a
   truncated branch list already degrades correctly — `branchTaken` refuses only
   on a positive match, so a short list means "no opinion", never "available".

2. **A limit inside `runGit`** — closes both callers at once, but needs care:
   `List` parses a record format, and a listing truncated mid-record parses into
   a confidently wrong answer rather than an error. It would need the limit to
   surface as an explicit error, not a silent cut. This is why PR #185 did not
   do it inline.

3. **Do nothing, deliberately.** The exposure requires a pathological repository
   the user themselves has on disk, and the spike is time- and concurrency-bounded.
   Recording the property is most of the value; `maxBranchList`'s doc comment was
   amended in #185 to say it bounds the wire payload rather than daemon memory,
   so the next reader is no longer misled.
