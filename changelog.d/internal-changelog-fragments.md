- **Changelog entries are now written as one file per pull request.** Every PR used to
  add its entry under the single `## [Unreleased]` heading in `CHANGELOG.md` — the same
  anchor line for all of them — so two PRs open at once conflicted as soon as the first
  one merged. Entries are now added as a file under `changelog.d/`, named
  `<type>-<slug>.md`, and the release workflow collects them into a version section and
  deletes them. Two PRs never touch the same path, so there is nothing left to conflict.

  `CHANGELOG.md` is no longer edited by hand; `changelog.d/README.md` documents the
  naming, the `none-` sentinel for releases with nothing user-facing, and
  `scripts/promote-changelog.sh --check` for validating a fragment before pushing.
