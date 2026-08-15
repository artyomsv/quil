---
name: mutation-testing-without-editing
description: How to mutation-test quil's Go code during a read-only review — robocopy the tree to the scratchpad and run the Docker toolchain against the copy
metadata:
  type: project
---

To prove a test really pins a behaviour, mutate a COPY in the scratchpad, never the reviewed tree:

1. Copy the tree. `git archive HEAD | tar -x -C <scratchpad>/mut` is far faster than robocopy and
   drops `.git`/`.quil` for free — but it copies **HEAD, not the working tree**, so when the fix
   you are re-verifying is still UNCOMMITTED use
   `tar -cf <scratchpad>/mut/t.tar $(git ls-files) <any untracked test files>` then extract it.
   (robocopy still works: `robocopy <worktree> <scratchpad>\mut /E /XD .git .quil node_modules`,
   exits 1 on success — ignore it.)
2. Edit the copy, then run the same container `scripts/dev.sh` uses, pointed at the copy:
   `MSYS_NO_PATHCONV=1 docker run --rm -v "<WINDOWS-style scratchpad>/mut":/src -v quil-gomod:/go/pkg/mod -w /src golang:1.25 go test -count=1 -run <pattern> ./pkg/...`
   Add `-count=1` — `dev.sh test` hits the Go build cache and reports `(cached)`, which reads as a pass.
3. Delete the copy when done (~80 MB).

**Two path traps in step 1-2, both of which cost a failed command each time:**
`docker -w /src` gets rewritten to `C:/Program Files/Git/src` without `MSYS_NO_PATHCONV=1`, and the
error ("the working directory ... is invalid") does not name MSYS. Conversely `tar -cf` REJECTS a
`C:/...` destination ("Cannot connect to C: resolve failed") and needs `/c/...`. So the same session
needs the `/c/...` form for tar and the `C:/...` form for the docker `-v` mount.

`scripts/dev.sh test` takes ONE package argument (`go test "$(pkg_target "${2:-}")"`) — passing three
paths silently runs only the first and prints one `ok` line that reads like all three passed.

**Why:** review tasks here forbid editing files, and there is no local Go toolchain, so the only
way to distinguish "the test asserts this" from "the test happens to pass" is a throwaway copy.
Mutations have found both real gaps and false alarms — e.g. the `markDead` must-not-close-raw
property looks unpinned inside `internal/ipc` but is caught deterministically by two `cmd/quil`
ordering tests.

**How to apply:** reach for it whenever a finding would be "no test covers X" or whenever a long
WHY-comment claims a named test guards a property. Verify before writing the finding.

Related: [[gofmt-crlf-check]]
