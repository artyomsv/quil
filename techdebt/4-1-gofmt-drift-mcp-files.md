# gofmt drift in cmd/quil/mcp.go and mcp_log.go

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `cmd/quil/mcp.go`, `cmd/quil/mcp_log.go` |
| Found during | feat/quil-restart pre-commit checks |
| Date | 2026-06-11 |

## Issue

`gofmt -l cmd/quil/` reports both files as unformatted (committed on master in that state). CI does not enforce gofmt, so the drift is silent.

## Risks

Noisy diffs the next time anyone saves these files with format-on-save; reviewers see formatting churn mixed into real changes.

## Suggested Solutions

Run `gofmt -w cmd/quil/mcp.go cmd/quil/mcp_log.go` in a standalone `style:` commit. Optionally add `test -z "$(gofmt -l .)"` to CI alongside `go vet`.
