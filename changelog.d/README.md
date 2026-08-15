# Changelog fragments

Every PR that changes production code adds **one new file in this directory** describing
the change from a user's point of view. At release time
`scripts/promote-changelog.sh` collects them into a new version section at the top of
`CHANGELOG.md` and deletes them.

You do not edit `CHANGELOG.md` by hand. That file is written by the release workflow.

## Why

Entries used to go straight into `CHANGELOG.md` under `## [Unreleased]` — the same anchor
line for every PR. Git merges by line range, so the second of two parallel PRs conflicted
with the first, every time, on a hunk the size of a multi-paragraph entry. Git has no
conflict concept for two distinct *added* files, so one file per PR removes the problem
rather than easing it.

## Naming

```
<type>-<slug>.md
```

`<type>` is one of:

| Type | Section it renders under |
|---|---|
| `added` | `### Added` |
| `changed` | `### Changed` |
| `deprecated` | `### Deprecated` |
| `removed` | `### Removed` |
| `fixed` | `### Fixed` |
| `security` | `### Security` |
| `internal` | `### Internal` |
| `none` | *(nothing — see below)* |

`<slug>` starts with a letter or digit and is otherwise free-form; deriving it from your
branch name is the easy way to keep it unique. Two PRs picking the same `<type>-<slug>`
is an add/add conflict — rare, loud, and fixed by renaming one of them. Uniqueness is a
convention, not a guarantee.

Anything in this directory that is neither `README.md` nor a valid fragment name is
**rejected** by the release gate, rather than skipped. A silently-ignored file is lost
prose that surfaces weeks later as a wrong release page.

## Content

The file holds the bullet body only — no `###` heading, no version header. The promoter
adds those. Write the same prose you would have written in `CHANGELOG.md`:

```markdown
- **A staged update no longer traps you on the version it staged.** Once a release
  was downloaded, `F1` → Update offered only to install *that* version, even if a
  newer one had been published since.

  Pressing the update row now asks the daemon what is actually the newest release
  before it does anything.
```

A fragment may hold several bullets, and one PR may add several fragments — including
more than one of the same type. Within a section they are concatenated in filename order.

## Nothing user-facing?

Say so explicitly. Add a `none-<slug>.md` fragment — its contents are ignored, and if it
is the only fragment in the release the version section reads `_No user-facing changes._`.

"Nothing to say" and "forgot to write it" must not look identical to the release gate,
which is why the empty case needs a file rather than the absence of one.

## Checking your work

```sh
sh scripts/promote-changelog.sh --check
```

Validates the directory and confirms there is something to promote. The PR gate in
`.github/workflows/ci.yml` calls the same script, so a fragment that passes locally
passes CI.
