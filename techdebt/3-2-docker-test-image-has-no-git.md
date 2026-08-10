# The Docker test image has no git, so every real-git test silently skips

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `scripts/dev.sh:6` (`GO_IMAGE="golang:1.25-alpine"`), consumed by the `test` / `test-race` / `test-integration` targets |
| Found during | Code review of PR #145 (worktree default-branch selection) |
| Date | 2026-08-09 |

## Issue

`golang:1.25-alpine` ships without a `git` binary (`docker run --rm golang:1.25-alpine git --version` → `sh: git: not found`).

Both real-git suites guard on that and skip:

- `internal/gitworktree/realgit_test.go:22` — `realGitRepo` does `t.Skip("git is not on PATH")`
- `internal/daemon/worktree_realgit_test.go` — same guard

So `./scripts/dev.sh test internal/gitworktree` prints `ok` while running none of them. The documented local loop in `CONTRIBUTING.md` and `.claude/CLAUDE.md` is exactly that command, so an author verifying a change to worktree creation gets a green run that never exercised it.

This is not hypothetical. PR #145 fixed a defect that lives entirely in what git *does* with an argv the stub tests already agreed was correct, and `.claude/rules/projects.md` now states outright that "the load-bearing tests are the real-git ones". Those are precisely the tests the standard command does not run. A follow-up defect in the same PR (a dangling `origin/HEAD` failing every worktree create) was likewise invisible to the Docker leg and was caught only by a hand-built native test binary.

CI is unaffected — `.github/workflows/ci.yml` uses `ubuntu-latest` + `setup-go`, which has git — so the gap is between the local loop and CI, which is the worst place for it: the author sees green, CI sees the truth.

## Risks

- A change to `internal/gitworktree` or the daemon's worktree paths can be developed, self-verified and pushed without ever running the tests that cover it.
- The skip is silent. `go test` reports `ok`, not `SKIP`, unless `-v` is passed, so nothing signals that coverage was lost.
- The workaround (cross-compile a Windows test binary in Docker, run it natively) is undocumented, several commands long, and has to be rediscovered each time.

## Suggested Solutions

1. **Add git to the test image.** A small `Dockerfile` (or reuse of the existing `ensure_race_image` pattern at `scripts/dev.sh:19-25`, which already builds a derived image with `apk add`) giving `apk add --no-cache git`. Cost is one cached image build; the real-git tests then run in the standard loop on every platform. Preferred.
2. **Fail loudly instead of skipping.** Replace the `t.Skip` with a `t.Fatal` when an env var like `QUIL_REQUIRE_GIT=1` is set, and set it in the `dev.sh` test target. Cheaper, but leaves the tests unrun locally — it only stops the silence.
3. **Document the native procedure** in `CONTRIBUTING.md`. Strictly worse than (1); relies on the author remembering.
