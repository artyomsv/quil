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
