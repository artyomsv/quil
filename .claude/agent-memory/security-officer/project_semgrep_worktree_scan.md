---
name: semgrep-worktree-scan
description: Semgrep in Docker scans 0 files in this repo's git worktrees; copy the changed files to a scratch dir and scan that instead
metadata:
  type: project
---

Semgrep run over `.claude/worktrees/<branch>` finds **zero targets** and still
exits 0 with "Scan completed successfully" — it is not a clean scan, it is a
silent no-op. A worktree's `.git` is a FILE pointing at the main repo's
`.git/worktrees/<name>`, which is outside the container mount, so
`git ls-files` fails and semgrep's git-based target discovery yields nothing.
Same trap in reverse from the Bash tool: Git Bash rewrites `-w /src` into
`C:/Program Files/Git/src`, so run the docker command from PowerShell.

**Why:** the failure looks identical to a genuinely clean scan — "Findings: 0
(0 blocking)" — so a review can report SAST coverage it never had. The only
tell is `Targets scanned: 0` in the JSON `paths.scanned`.

**How to apply:** copy the changed files into a non-git scratch dir preserving
their relative paths, mount THAT as `/src`, and always assert
`paths.scanned.Count` matches the file count before trusting a zero-finding
result. Relates to [[project_remote_daemon_taint]] only in that both are about
not trusting a confident-looking empty answer.

Two things that make `paths.scanned` read short WITHOUT the worktree bug being
back, so check them before re-debugging the mount:

- Semgrep **skips `*_test.go` by default** (its own `--exclude` defaults). A
  five-file Go changeset with three test files legitimately scans two. That is
  fine for a security review — the finding surface is production code — but
  the count will not match the file count, so assert against the NON-test
  count.
- Do **not** write the `--json` output into the directory being scanned.
  Semgrep picks its own `out.json` up as a target and reports
  `Syntax error at line /src/out.json:1: missing element`, an error that looks
  like a scan failure and is nothing but self-ingestion. Write it one level
  above the mount.

Working invocation (PowerShell, Go + TS changeset):
`docker run --rm -v "${scratch}:/src" semgrep/semgrep semgrep scan --config p/owasp-top-ten --config p/golang --config p/typescript --json --metrics=off /src`
— note `p/golang`, not `p/java`-style `p/go`, which does not resolve.
