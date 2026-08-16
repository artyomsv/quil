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

Anything in this directory that is neither `README.md` nor a valid fragment is
**rejected**, rather than skipped. A silently-ignored file is lost prose that surfaces
weeks later as a wrong release page. Beyond the name, a fragment must be:

- **A regular file.** Not a symlink, not a directory. The promoter splices a fragment's
  bytes verbatim into `CHANGELOG.md`, which is pushed to master and published as the
  release body — and a symlink is read *through*, so `fixed-x.md` pointing at a file
  elsewhere in the checkout would publish that file's contents.
- **Non-empty.** An empty fragment renders a section heading with nothing under it. If
  the change genuinely has nothing to tell users, that is what `none-<slug>.md` is for —
  "forgot to write it" and "nothing to say" must not produce the same output.
- **Free of `## [` headings.** The release notes are extracted with a range that ends at
  the next `## [`, so such a line truncates the published notes there. The promoter
  writes every heading; a fragment never needs one.

## Headline

Every fragment of a user-facing type carries a one-line headline in a front-matter
block at the very top of the file:

    ---
    headline: Option+Shift shortcuts work again on macOS
    ---
    - **`Option+Shift+<letter>` reaches the shell again on macOS.** Chord parsing
      lowercased the key, and the same parser reads the incoming key press…

The headline is what a user sees in the **What's New** dialog after upgrading — the
one-line register, not the explanatory one. `scripts/promote-changelog.sh` strips the
block before splicing your prose into `CHANGELOG.md`, so the published entry is
unchanged, and appends the headline to `internal/changelog/highlights.txt`, which the
binary embeds.

| Rule | |
|---|---|
| Required on | `added` `changed` `deprecated` `removed` `fixed` `security` |
| Forbidden on | `internal` `none` — neither is addressed to users |
| Shape | Exactly three lines: `---`, `headline: <text>`, `---`. Nothing else, no blank line inside |
| Length | At most 64 bytes. It renders on one dialog row |
| Characters | No control characters, no `"`, no `\`. Printable non-ASCII (`→`, `•`) is fine |
| Body | The fragment must still hold prose once the block is removed — a headline-only file is refused as empty |

Write it as a complete sentence about the outcome, not the mechanism. "Option+Shift
shortcuts work again on macOS", not "fix chord parsing case handling".

A fragment that gets any of this wrong is refused by `--validate`, which runs on every
pull request.

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
sh scripts/promote-changelog.sh --validate   # is this directory well-formed?
sh scripts/promote-changelog.sh --check      # ...and is there anything to release?
```

`.github/workflows/ci.yml` runs `--validate` on every PR and `.github/workflows/release.yml`
runs `--check` before it cuts a version, so a fragment that passes locally passes both.
The grammar lives in that one script for exactly that reason — a gate that can disagree
with the action it guards is how a green PR turns master red.
