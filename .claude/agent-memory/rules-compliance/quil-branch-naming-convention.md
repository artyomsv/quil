---
name: quil-branch-naming-convention
description: quil's actual branch-naming convention uses "feature/" (not "feat/"), per CONTRIBUTING.md and repo history — flag "feat/*" branches as a naming-convention drift.
metadata:
  type: project
---

quil's own `CONTRIBUTING.md` (Branch naming section) documents `<type>/<short-description>`
with example `feature/plugin-opencode` — the type token is `feature`, not `feat`. Prior
branches confirm this: `feature/auto-update`, `feature/command-palette`,
`feature/palette-content-search`, `feature/pane-context-menu`, `fix/mcp-bridge-parent-watchdog`
(5 `feature/*`, 0 `feat/*` before 2026-07-24).

**Why:** the generic global `git-workflow.md` rule lists common prefixes as `feature/`, `fix/`,
`chore/`, `docs/`, `refactor/`, `hotfix/` — it doesn't mention `feat/` either, so both the
global rule and the project's own CONTRIBUTING.md agree on `feature/`. Commit *subject lines*
correctly use the Conventional Commits type `feat(scope): ...` (that's correct — Conventional
Commits mandates `feat` there), which is a different vocabulary from branch names and is easy
to conflate.

**How to apply:** when reviewing a quil branch named `feat/...`, flag it as a LOW-severity
branch-naming drift against `git-workflow.md` + `CONTRIBUTING.md`, and note the correct prefix
is `feature/`. First observed on branch `feat/recent-locations-setup-dialog`
(commit 40b1aad, 2026-07-24).
