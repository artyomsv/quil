---
name: quil-techdebt-convention
description: quil has no /techdebt-add command and no techdebt/README.md — the tech-debt-tracking.md fallback file convention applies, flat under techdebt/ (single-service repo).
metadata:
  type: project
---

Confirmed 2026-08-01 while reviewing `fix/claude-resume-session-hijack`:
quil's repo root has a `techdebt/` directory with real entries (e.g.
`techdebt/3-3-discovery-scan-cannot-be-interrupted-mid-syscall.md`,
`techdebt/pty/4-1-windows-close-does-not-reap.md`), but no
`techdebt/README.md` and no `.claude/commands/techdebt-add.md`. Per
`~/.claude/rules/tech-debt-tracking.md`'s own precedence order, that means the
**fallback file convention** applies as-is: `{criticality}-{complexity}-{kebab
description}.md` directly under `techdebt/`, flat (single-service repo — no
per-service subfolder needed), except that one existing `pty/` subfolder groups
platform-specific PTY debt, which is a pre-existing local convention worth
following if a new entry is specifically about `internal/pty/`.

**How to apply:** when a PR's own description/commit body admits a deliberate
follow-up or known gap (e.g., "left out of this PR", "still uses X, which
carries the same risk"), and no existing `techdebt/*.md` already covers it,
flag the missing entry — this project does track debt this way and reviewers
before you have created entries under exactly this convention.
