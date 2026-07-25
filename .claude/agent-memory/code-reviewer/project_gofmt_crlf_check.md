---
name: gofmt-crlf-check
description: How to get a meaningful gofmt verdict in this repo — the working tree is CRLF, so `gofmt -l` in a Linux container flags every file
metadata:
  type: project
---

The repo is checked out with CRLF line endings on Windows, and Go/make are not
installed on the host (builds run through Docker). Running `gofmt -l internal/`
inside a Linux container therefore lists **every** file — the `\r` alone makes
gofmt consider them unformatted — so a raw run tells you nothing about the diff
under review.

**Why:** the fix (a `.gitattributes` with `eol=lf`) has not landed; several
files are also genuinely unformatted at HEAD (`internal/ipc/protocol.go`,
`internal/plugin/plugin.go`, `internal/plugin/registry.go`, plus the MCP files
tracked in `techdebt/4-1-gofmt-drift-mcp-files.md`), so "gofmt flags it" is not
by itself evidence the change introduced drift.

**How to apply:** when checking formatting on changed Go files, strip `\r`
first, then compare the working-tree verdict against the same check on the HEAD
version of each file. Only report a gofmt finding when a file is clean at HEAD
and dirty in the working tree. Adding a field to a long aligned struct block
(e.g. `Model` in `internal/tui/model.go`) is the usual way new drift appears —
gofmt wants the whole contiguous run realigned.
