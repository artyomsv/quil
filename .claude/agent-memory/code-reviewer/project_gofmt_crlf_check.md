---
name: gofmt-crlf-check
description: How to get a meaningful gofmt verdict in this repo — worktree files still carry CRLF, so `gofmt -l` in a Linux container flags files the diff never touched
metadata:
  type: project
---

Go is not installed on the host (everything runs through Docker), and many
already-checked-out files in the worktree still have CRLF endings. Running
`gofmt -l internal/...` inside the Linux container lists those files purely
because of the `\r`, so a raw run tells you nothing about the diff under review.

**Why:** `.gitattributes` with `eol=lf` landed 2026-07-28, but it only governs
files git re-materialises — files already on disk keep their CRLF, and
`git diff` prints "LF will be replaced by CRLF" warnings for exactly the ones
that are already LF. So the container's verdict is a mix of real drift and line
endings. Several files are also genuinely unformatted at HEAD:
`internal/tui/palette.go` (the `palAct*` const block — the project-actions run,
`palActRenameProject`…`palActPrevProject`, is over-padded), plus
`internal/ipc/protocol.go`, `internal/plugin/plugin.go`,
`internal/plugin/registry.go` and the MCP files in
`techdebt/4-1-gofmt-drift-mcp-files.md`.

**How to apply:** copy each changed file through `tr -d '\r'` into `/tmp` and run
`gofmt -l` on the copy; only report a file that is clean at HEAD and dirty in the
worktree. When a file IS dirty, run `gofmt -d` on the stripped copy and check
whether the misaligned run overlaps the diff — an unrelated const block in the
same file is not this change's problem. Adding a field to a long aligned struct
block (e.g. `Model` in `internal/tui/model.go`) is the usual way real new drift
appears: gofmt wants the whole contiguous run realigned.
