---
name: project_test_tooling
description: How tests are run in the Quil/Calyx project — Docker-based, no local Go
type: project
---

All tests run via Docker through `./scripts/dev.sh test` (uses `golang:1.25` image). Go is NOT installed locally. Direct `docker run` with `-v quil-gomod:/go/pkg/mod` also works.

- Unit tests: `./scripts/dev.sh test` — runs `go test ./...` inside Docker
- Race detector: `./scripts/dev.sh test-race` — adds CGo + gcc inside Docker
- Platform-specific tests: `//go:build` tags used; Unix PTY tests run in the Linux Docker container; Windows PTY code is NOT exercised in CI (no Windows runner)
- Test files follow Go conventions: `*_test.go` in the same package or `_test` external package
- No test framework beyond stdlib `testing` — plain `t.Fatal`, `t.Error`, `t.Run`

**Why:** No local Go installation — Docker-first tooling policy per `local-environment.md`.
**How to apply:** Always use `./scripts/dev.sh test` when running tests. Never suggest `go test` as a bare command.

## Transient bind-mount glitches on this Windows host

Direct `docker run -v "$(pwd -W):/src" ...` against the repo on this Windows host occasionally serves a stale/inconsistent view of the source tree to a freshly-started container: observed a phantom `go vet` error for a nonexistent file (`open internal/tui/zz_tmp_height_check_test.go: no such file or directory`) and a phantom build failure in a package whose test file appeared to have an old function signature that didn't match the current on-disk source — both vanished on an immediate retry of the exact same command with no code changes in between.

**Why:** Docker Desktop's Windows file-sharing layer (gRPC-FUSE/VirtioFS) for bind mounts can desync briefly, especially right after other containers have just read/written the same mounted path.
**How to apply:** Never report a build/vet/test failure from a single docker run as real without retrying it once first (ideally with `-count=1` for tests to bypass any cache). If a failure disappears on retry with zero source changes, it was the bind-mount glitch, not a code issue — don't mention it in a QA report except perhaps as a footnote. If it reproduces on retry, it's real.

## Worktrees are shared and actively edited by other agents mid-QA

Observed directly (2026-07-31, `fix-input-history-ui` worktree): a `go test -race ./...` run failed with a genuine-looking compile error (`undefined: claudesessions`) referencing a file (`internal/tui/history.go`) that another teammate agent was actively editing at that exact moment (security-review-driven sanitization work landing incrementally). A `git status`/`git diff` taken seconds later showed the file mid-edit — the import and the usage were both present and consistent by the time I re-read it, and a retry of the exact same test command passed cleanly.

**Why:** Worktrees in this multi-agent session are not exclusive to the QA agent — other teammates (main, sec-review, etc.) can be writing to the same files in the same worktree concurrently while QA runs. A build/vet/test failure that mentions a symbol or file that looks unrelated to anything in the diff you were handed is a strong signal you raced a concurrent edit, not a real regression.
**How to apply:** On any unexpected/unfamiliar build failure, immediately run `git status --short` and `git diff --stat` before concluding it's real — if the failing file has uncommitted changes and doesn't match what you were told to review, retry the test after a few seconds rather than reporting it as a bug. Also re-check `git diff --stat` right before your FINAL test run and report against that snapshot, since the working tree can keep moving throughout a QA session — note in the report if scope grew beyond the originally-assigned file list, since that work hasn't been reviewed yet.

## The concurrency also happens in the PRIMARY worktree (`E:/Projects/Stukans/quil` itself), not just `.claude/worktrees/*`

Observed directly (2026-08-01, `fix/remote-stale-binary-record` / PR #118 QA): the repo's MAIN checkout (not a sub-worktree) had live uncommitted edits to exactly the files under review (`cmd/quil/remote_setup.go`, `remote_setup_test.go`) plus two files outside the assigned scope (`internal/remoteinstall/probe.go`, `probe_test.go` — an unrelated bidi-sanitization fix). Two `./scripts/dev.sh test` runs seconds apart against the SAME command gave different results for `cmd/quil` (all-pass, then a genuine `FAIL` with different assertions each time) because another agent was actively rewriting `healRemoteRecord`/`offerRemoteInstall` (adding an `ExistingDirWritable` privilege-escalation gate) mid-flight — not a Docker bind-mount glitch (that class reverts identically on retry; this one changed content between runs).

**How to apply:** If retrying a failure doesn't stabilize it, or two consecutive runs fail differently, suspect a live concurrent edit even when `pwd` is the main repo directory, not a named worktree. To get a trustworthy pass/fail read of the branch AS COMMITTED while someone else edits the same files live: `git worktree add --detach <scratchpad>/qa-clean-<name> <commit-sha>` (an ADDITIVE git op, safe — never `git stash`/`checkout -- .`/`reset --hard` on the shared directory, that would destroy the other agent's uncommitted work), run `./scripts/dev.sh test`/`vet` there, then `git worktree remove --force <path>` to clean up. Report results from that isolated run as the authoritative answer for the PR under review, and separately flag the live uncommitted files (with their paths) as out-of-scope concurrent work the report is NOT evaluating.
