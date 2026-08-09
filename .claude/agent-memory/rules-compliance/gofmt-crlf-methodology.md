---
name: gofmt-crlf-methodology
description: How to get a meaningful gofmt verdict in the quil repo on this Windows checkout — raw gofmt -l flags every file due to CRLF, and the codebase has pre-existing unformatted files unrelated to any given diff
metadata:
  type: project
---

Go files in this worktree are checked out with CRLF line endings. That is
NORMAL and not a bug to chase: what git STORES is LF (verify with
`git show HEAD:internal/tui/sidebar.go | cat -A` — no `^M`), and
`.gitattributes` pins `eol=lf` for the files where CRLF actually breaks
something (`scripts/*.sh`, which run inside the Linux build container, and the
`//go:embed`-ed shell init). Go sources are left to git's normal Windows
checkout conversion.

The consequence for this check: running `gofmt -l` or `-d` directly against the
working-tree files inside a Linux container (e.g.
`docker run golang:1.25 gofmt -l internal/...`) flags **every** file — the `\r`
alone makes gofmt consider them unformatted. That result is meaningless.
Check the BLOB, not the working tree.

**Correct procedure** — the one-liner first, since it needs no scratch files
and no Docker mount at all when a local gofmt is reachable:
`git show :internal/tui/model.go | gofmt -l /dev/stdin` (or `HEAD:` for the
committed version). It reads the LF blob straight out of the index, so there is
no `\r` to confuse gofmt. Use the longer route below only when gofmt must run
inside the container:

1. Strip `\r` from each changed `.go` file into a scratch copy (`tr -d '\r'`).
   Docker Desktop on this host only shares certain drives — write scratch
   copies under the same drive as the repo (e.g. `E:/Projects/...`), not
   `/tmp` (git-bash's `/tmp` is not visible to the Docker daemon, mount
   silently fails with "no such file or directory" for every path inside it).
2. Run `docker run --rm -v "<scratch>:/src" -w /src golang:1.25 gofmt -l/-d
   <files>` (use `export MSYS_NO_PATHCONV=1` first or git-bash mangles the
   `-v` path).
3. Do the SAME strip-and-check against the parent commit's version of each
   file (`git show <parent>:<path> | tr -d '\r'`).
4. Only attribute a gofmt finding to the commit under review when a hunk is
   clean at the parent and dirty in the new version. Known pre-existing drift
   in this repo (confirmed present at HEAD independent of any diff):
   `internal/ipc/protocol.go`, `internal/tui/pane.go`, `internal/tui/styles.go`,
   `internal/daemon/session.go`, plus the MCP files tracked in
   `techdebt/4-1-gofmt-drift-mcp-files.md`. CI does not enforce `gofmt -l`, so
   this drift is silent and low-criticality by project convention — don't
   over-weight it if it's pre-existing.

**Where new drift actually comes from:** adding a field (especially one with
a preceding doc comment) into the middle of a long contiguous struct field
block. gofmt breaks the vertical-alignment "run" at a full-line comment, so
the fields above the comment realign to their own (usually narrower) column
and the fields below need to realign to match the new field — but a manual
edit that only touches the new field's neighbourhood typically leaves the
rest of the block at its old alignment. Confirmed instance: PR #146
(`feat(tui): persist the attention pin and give it its own mark`, commit
c26bf1a) added `PinnedAttention bool` + a 3-line doc comment ahead of
`Overlay` in `PaneInfo` (`internal/tui/model.go`, struct starts ~line 95) —
clean at parent commit b8b95bd, dirty after: `ID`/`TabID`/`CWD`/`Name`/`Type`/
`Muted`/`Eager` need to narrow, and `Pending`/`SessionID`/`HistoryLines` need
to widen to match `PinnedAttention`'s column. `internal/tui/model.go` is a
recurring site for this because it holds several very long, comment-interspersed
structs (`PaneInfo`, `Model`). See sibling agent memory
`.claude/agent-memory/code-reviewer/project_gofmt_crlf_check.md`, which
documents the same pattern independently — that one is code-reviewer's copy,
this is rules-compliance's copy (separate memory stores, same finding).
