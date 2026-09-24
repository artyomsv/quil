---
name: quil-changelog-ci-gate
description: quil's CI changelog gate now requires a NEW changelog.d/<type>-<slug>.md fragment (not an edit to CHANGELOG.md); CHANGELOG.md is written only by the release workflow and must never be hand-edited.
metadata:
  type: project
---

**Superseded the old "touch CHANGELOG.md" rule.** As of the `changelog.d/`
migration (commit e5242c7, `chore(release): promote per-PR changelog fragments
at release time`, PR #163), `.github/workflows/ci.yml`'s `changelog` job
requires a **newly ADDED** `changelog.d/<type>-<slug>.md` file
(`git diff --diff-filter=A`, piped through
`sh scripts/promote-changelog.sh --filter-names`). Editing `CHANGELOG.md` by
hand no longer satisfies it — and `CHANGELOG.md` is written by
`release.yml` only.

The gate fires when the diff still has files after a **denylist**:
`^(site|docs|tools|techdebt|marketing|\.github|\.claude|changelog\.d)/`, root
`*.md`, `VERSION|LICENSE|NOTICE|Makefile|package-lock.json|
.gitignore|.gitattributes|.editorconfig|.dockerignore`, and `_test.go$`. So a
test-only PR is exempt. `release.yml` uses the SAME denylist on purpose — a
divergence is what turned master red in #130.

Fragment rules live ONLY in `scripts/promote-changelog.sh` and
`changelog.d/README.md`:

- Types: `added changed deprecated removed fixed security internal none`
- A `headline:` front-matter block is REQUIRED on the six user-facing types and
  FORBIDDEN on `internal` and `none`
- Check locally with `sh scripts/promote-changelog.sh --validate` (writes
  nothing, safe to run in a read-only review)

**How to apply:** on any quil diff, (1) run `--validate` rather than
hand-checking the grammar; (2) if non-test `cmd/`/`internal/` code changed and
no NEW fragment was added, that is a HIGH, CI-enforced finding; (3) also compare
the fragment TYPE against the commit/PR TYPE — `feat` bumps a minor while an
`internal` fragment declares the change non-user-facing, which is a real
mismatch worth flagging. Remember the PR TITLE is the squash subject the release
classifies. See also [[quil-branch-naming-convention]].
