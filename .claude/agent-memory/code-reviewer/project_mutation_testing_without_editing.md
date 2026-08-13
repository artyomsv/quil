---
name: mutation-testing-without-editing
description: How to mutation-test quil's Go code during a read-only review — robocopy the tree to the scratchpad and run the Docker toolchain against the copy
metadata:
  type: project
---

To prove a test really pins a behaviour, mutate a COPY in the scratchpad, never the reviewed tree:

1. `robocopy <worktree> <scratchpad>\mut /E /XD .git .quil node_modules` (robocopy exits 1 on success — ignore it).
2. Edit the copy, then run the same container `scripts/dev.sh` uses, pointed at the copy:
   `docker run --rm -v "<scratchpad>/mut":/src -v quil-gomod:/go/pkg/mod -v quil-gocache:/root/.cache/go-build -w //src golang:1.25-alpine go test -count=1 -run <pattern> ./pkg/...`
   Add `-count=N` — `dev.sh test` hits the Go build cache and reports `(cached)`, which reads as a pass.
3. Delete the copy when done (~80 MB).

**Why:** review tasks here forbid editing files, and there is no local Go toolchain, so the only
way to distinguish "the test asserts this" from "the test happens to pass" is a throwaway copy.
Mutations have found both real gaps and false alarms — e.g. the `markDead` must-not-close-raw
property looks unpinned inside `internal/ipc` but is caught deterministically by two `cmd/quil`
ordering tests.

**How to apply:** reach for it whenever a finding would be "no test covers X" or whenever a long
WHY-comment claims a named test guards a property. Verify before writing the finding.

Related: [[gofmt-crlf-check]]
