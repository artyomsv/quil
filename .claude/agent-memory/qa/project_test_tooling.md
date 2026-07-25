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
