---
name: project_test_tooling
description: How tests are run in the Aethel/Calyx project — Docker-based, no local Go
type: project
---

All tests run via Docker through `./scripts/dev.sh test` (uses `golang:1.25` image). Go is NOT installed locally. Direct `docker run` with `-v aethel-gomod:/go/pkg/mod` also works.

- Unit tests: `./scripts/dev.sh test` — runs `go test ./...` inside Docker
- Race detector: `./scripts/dev.sh test-race` — adds CGo + gcc inside Docker
- Platform-specific tests: `//go:build` tags used; Unix PTY tests run in the Linux Docker container; Windows PTY code is NOT exercised in CI (no Windows runner)
- Test files follow Go conventions: `*_test.go` in the same package or `_test` external package
- No test framework beyond stdlib `testing` — plain `t.Fatal`, `t.Error`, `t.Run`

**Why:** No local Go installation — Docker-first tooling policy per `local-environment.md`.
**How to apply:** Always use `./scripts/dev.sh test` when running tests. Never suggest `go test` as a bare command.
