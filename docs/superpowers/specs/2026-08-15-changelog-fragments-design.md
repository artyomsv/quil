# Changelog fragments — design

**Date:** 2026-08-15
**Status:** approved for implementation (spec review folded in)
**Branch:** `feat/changelog`

## Problem

Every PR that changes production code must add prose to `CHANGELOG.md` under the
`## [Unreleased]` heading (enforced by the `changelog` job in `.github/workflows/ci.yml`).
Every PR therefore inserts text at the *same anchor line*. Git merges by line range, so
the second PR to merge conflicts with the first — reliably, and on a large hunk, because
quil's entries are multi-paragraph prose (the v1.59.1 block is 25 lines).

Observable symptom in the repo's own history: entries written as separate follow-up
commits to dodge the collision — `e2d74f3 docs(changelog): add the frame encoding entry`,
`8c09458 docs(changelog): note the release-publishing fix`.

## Why not port `changelog.new` literally

The reference implementation is `stukans/monorepo`'s
`.github/actions/process-changelog`: one `<service>/changelog.new` per service, holding
`{VERSION}`/`{DATE}` placeholders, promoted into `changelog.md` and deleted at release.

That design does **not** eliminate conflicts — it partitions them across ~15 service
directories. Two PRs touching the same service still collide, and the monorepo's own
recorded experience says so:

> It is a merge-conflict magnet. A release between branching and pushing deletes the
> file on master, so your branch hits a modify/delete conflict.

> Writing it with `cat > changelog.new << 'EOF'` truncates whatever is already pending.
> That file frequently already holds an entry from a PR merged hours earlier but not yet
> released.

quil is a **single service**. A root `changelog.new` would collect 100% of PR traffic on
one file: the same conflicts as today, plus a silent-overwrite failure mode the current
design does not have.

The property that actually removes conflicts is *each PR creates a path no other PR
names* — git has no conflict concept for two distinct added files. That is the fragment
directory this document specifies.

## Design

### Fragment files

```
changelog.d/
  README.md                     documents the convention; never consumed
  fixed-update-recheck.md
  added-worktree-close.md
```

Filename grammar:

```
^(added|changed|deprecated|removed|fixed|security|internal|none)-[A-Za-z0-9][A-Za-z0-9._-]*\.md$
```

The eight types are the six Keep a Changelog sections, plus `internal` (already used
three times in `CHANGELOG.md`) and `none` (the sentinel — see below). The slug is
free-form and conventionally derived from the branch name.

**Directory validity rule.** A file in `changelog.d/` is valid if it matches the grammar
**or** is exactly `README.md`. Anything else — `.gitkeep`, `notes.md`, `banana-x.md`,
`Fixed-Typo.md` — is **refused** by validation, loudly. A silently-skipped file is lost
prose that surfaces as a wrong release page weeks later. `README.md` is the sole
exemption and is load-bearing beyond documentation: git does not track empty directories,
so without a permanent file `changelog.d/` disappears from the tree the moment a release
consumes every fragment.

A fragment may contain **multiple bullets**, and one PR may add **multiple fragments**,
including several of the same type. Content is the **bullet body only**, no `###`
heading — exactly the text that lands in `CHANGELOG.md` today:

```markdown
- **A staged update no longer traps you on the version it staged.** Once a
  release was downloaded, `F1` → Update offered only to install *that* version…
```

The promoter owns the section headings, so quil's existing prose style is unchanged.

Two PRs choosing the same `type-slug` is an add/add conflict — rare, loud, and resolved
by renaming one. "Each PR names a unique path" is a convention, not a guarantee; the
README says so.

`.gitattributes` gains `changelog.d/*.md text eol=lf`. Without it a fragment authored on
Windows can carry CRLF into `CHANGELOG.md`; the goreleaser job strips `\r` from release
notes (`release.yml:400`) but the committed changelog would keep mixed endings. The
promoter also strips CR defensively, so it is correct regardless of how a file reached
the tree.

### `none-*.md` — the no-user-facing-changes sentinel

Ports today's `_No user-facing changes._` escape hatch with identical semantics: a
`none-*.md` fragment satisfies the CI gate, contributes no section, and is deleted at
release. **A lone `none-*` fragment counts as "something to promote"** — otherwise a
`fix:`-typed release carrying only a sentinel would fail validation and red-master, which
is precisely the case the sentinel exists to serve. When it is the only fragment, the
promoter emits `_No user-facing changes._` as the version body; `release.yml:446-449`
already recognises that exact line and falls through to generated release notes. If real
fragments are also present, the sentinel is suppressed and the `none-*` file is still
consumed.

### `scripts/promote-changelog.sh`

POSIX `sh`, `set -eu`, matching the idiom of `scripts/check-claude-md-size.sh` and
`scripts/install.sh`. The logic lives in a script, not a `run:` block, for two
precedent-backed reasons: `ci.yml` already runs `shellcheck -S warning scripts/*.sh`
(`ci.yml:86`) and `sh scripts/test-install.sh` (`ci.yml:89`), so a script gets a real test
on every PR for free; and this is the step that can destroy unreleased prose, which is
exactly the class of code that must not be untestable inline YAML.

Three modes:

| Invocation | Behaviour |
|---|---|
| `promote-changelog.sh --filter-names` | Reads candidate paths on stdin, prints those matching the fragment grammar. Writes nothing else. This is what `ci.yml` calls. |
| `promote-changelog.sh --check` | Validates the working tree: fails if `changelog.d/` holds an invalid name, or if there is nothing to promote. Writes nothing. |
| `promote-changelog.sh <version> <date>` | Runs `--check`'s validation, then rewrites `CHANGELOG.md` and deletes the consumed fragments. |

**The grammar exists in exactly one place — this script.** `ci.yml` does not hand-write an
approximating regex. Spec review caught the first draft doing exactly that: a looser
`^changelog\.d/[a-z]+-…` in the gate would pass `banana-x.md` that `--check` later
refuses, producing a green PR that turns master red on the next release. That is the
repo's own most expensive changelog bug (`ci.yml:120-127`, `release.yml:155-161`, #130,
v1.49.0 → v1.49.1) reproduced in a new place. A gate that can disagree with the action it
guards is the defect, regardless of how either is worded.

Rendering order is Keep a Changelog's: Added, Changed, Deprecated, Removed, Fixed,
Security, then Internal. Within a section, fragments concatenate in `LC_ALL=C` filename
sort order — arbitrary but deterministic, which is what reproducible output needs.

Output shape, matching `CHANGELOG.md` byte-for-byte as it is written today (verified
against the v1.54.0 block, which carries three sections): version header, blank line,
section heading, bullets immediately with **no** blank line after the heading, and exactly
one blank line before the next `###` or `##`.

```
## [Unreleased]

## [1.60.0] - 2026-08-16

### Added
<contents of added-*.md>

### Fixed
<contents of fixed-*.md>

## [1.59.1] - 2026-08-15
…
```

`## [Unreleased]` is **retained** as a static anchor. Keeping it means `release.yml`'s
insertion point and the goreleaser job's
`sed -n "/^## \[${VERSION}\]/,/^## \[/"` extraction (`release.yml:399`) both work
unchanged — the diff stays confined to *what fills the section*, never *how the file is
shaped*.

### Leftover `[Unreleased]` prose

If prose sits under `## [Unreleased]` at promotion time, the promoter moves it into the
new version block **first**, with rendered fragments below it, and counts it as
"something to promote" for `--check`.

This is not transition scaffolding — it is the permanent fail-soft path. A hand-edit of
`CHANGELOG.md` degrades to slightly ugly output (a duplicated `### Fixed` heading, if both
sources carry one) instead of silently dropping content. Loud and visible in review beats
correct-looking and lossy.

It is deliberately asymmetric with the CI gate, which requires a **fragment** and does not
accept a bare `CHANGELOG.md` edit. Otherwise nothing drives adoption and the conflicts
persist.

### `ci.yml` — the `changelog` job

The gate flips from "did the PR touch `CHANGELOG.md`?" (`ci.yml:148`) to "did the PR
**add** a fragment?", delegating the grammar to the promoter:

```sh
FRAGMENTS=$(git diff --name-status --diff-filter=A "$MERGE_BASE" "$HEAD" \
  | cut -f2 | sh scripts/promote-changelog.sh --filter-names)
```

`--diff-filter=A` is load-bearing: the old gate was satisfiable by editing an
already-released section; this one is not. The job already checks out the repo with
`fetch-depth: 0` (`ci.yml:102-105`), so the script is present.

The job's header comment (`ci.yml:91-96`) and its `::error` text (`ci.yml:153`) both
describe the CHANGELOG.md-touch mechanism and are rewritten. A workflow comment describing
a mechanism that no longer exists is how the next person reintroduces the old behaviour.
The new error message must name the file to create, the valid types, and the sentinel.

### `release.yml`

| Step | Change |
|---|---|
| `Require a non-empty [Unreleased] section` (`:246-283`) | Replaced by `Validate changelog fragments` → `sh scripts/promote-changelog.sh --check` |
| `Update version files` (`:285-308`) | The version-header `sed` (`:294`) becomes `sh scripts/promote-changelog.sh "$VERSION" "$DATE"`. The two `site/src/data/seo.ts` seds are untouched. |
| `Commit version bump and tag` (`:328-360`) | `git add` line (`:349`) gains `if [ -d changelog.d ]; then git add -A changelog.d; fi` |

`git add -A` is load-bearing: it stages the **deletions**. The monorepo action carries a
comment recording exactly this bug (`action.yml:65-70`) — a plain `rm` left the removal
unstaged, so `changelog.new` was re-promoted on every subsequent release.

The `inputs.publish_tag == ''` guards (`:276`, `:286`, `:343`) are preserved, so recovery
mode still skips bump/commit/tag. Dry-run mode deletes fragments in the runner workspace
and commits nothing; the `Run tests` step that follows is Go-only and unaffected by a
mutated `changelog.d/`.

The back-to-back-merge recovery comment (`:266-275`) is rewritten: under fragments the
symptom is `--check` reporting nothing to promote, and the recovery is to confirm the
previous version's section absorbed this run's entries — not to "move it down rather than
writing a new one", which describes an `[Unreleased]` block that no longer accumulates.

### Both denylists

`changelog.d/` joins the "does not reach the binary" denylist in **both**
`ci.yml:139-142` and `release.yml:201-204`:

```
grep -vE '^(site|docs|tools|techdebt|marketing|\.github|\.claude|changelog\.d)/'
```

Without it, a typo fix in a pending fragment reads as payload and cuts a release whose
five platform binaries are byte-identical to the previous tag. Same edit, both files, one
commit — per the standing "keep this list in sync" comments on each.

## Testing

`scripts/test-promote-changelog.sh`, POSIX sh, modelled on `scripts/test-install.sh`
(temp dir + `trap`, `fail()` helper, self-contained, no network). Added to `ci.yml`'s
`test` job next to the existing `sh scripts/test-install.sh`. `shellcheck -S warning
scripts/*.sh` picks up both new scripts automatically.

Cases:

1. Single fragment → correct section heading, correct version header, fragment deleted.
2. Multiple types → rendered in Keep a Changelog order, not filename order.
3. Multiple fragments of one type → concatenated in filename sort order.
4. `none-*` alone → body is `_No user-facing changes._`, and `--check` passes.
5. `none-*` alongside real fragments → sentinel suppressed, `none-*` still deleted.
6. Leftover `[Unreleased]` prose → preserved, placed above rendered fragments.
7. Nothing to promote (no fragments, empty `[Unreleased]`) → `--check` exits non-zero.
8. Invalid filename (`banana-x.md`, `Fixed-X.md`, `.gitkeep`) → `--check` exits non-zero
   and the promote path writes nothing.
9. `README.md` is never consumed, never deleted, and never fails validation.
10. `## [Unreleased]` anchor survives promotion.
11. Previously released sections are byte-identical after promotion (strict assertion —
    this is the guard against a splicing bug silently eating history).
12. CRLF in a fragment does not put `\r` into `CHANGELOG.md`.
13. `--filter-names` accepts exactly the names `--check` accepts and rejects the rest —
    the anti-divergence test for C1.
14. Exact whitespace: no blank line after a `###` heading, exactly one before the next
    `###`/`##`.

Go tests are untouched — no Go code changes in this work.

## Transition

`origin/master`'s `## [Unreleased]` is empty and **there are no open PRs** (confirmed:
`gh pr list --state open` returns `[]`; #162, #161 and #138 were closed while this spec
was under review). Nothing to migrate, nobody to coordinate with, no grace period in the
gate. Strict from the first commit.

The leftover-prose tolerance stays regardless, as the permanent fail-soft path described
above.

## This PR's own changelog entry

`scripts/` is not in either denylist (correctly — `scripts/fetch-conpty.sh` is a genuine
Windows build input), so this PR counts as payload and must carry a fragment under the new
gate its own head commit installs. It gets `changelog.d/internal-changelog-fragments.md`.

The PR title uses `chore(` so no version bump is computed — cutting a release for a
process change would publish five binaries identical to v1.59.1. The fragment stays
pending until the next real release consumes it, which also serves as the first live
exercise of the promoter.

## Non-goals

- **The back-to-back-merge sweep.** `release.yml:266-275` documents a failure where one
  run absorbs two PRs' entries (v1.32.1's macOS fixes filed under v1.32.0). Fragments
  neither fix nor worsen it: a run consumes whatever is present in its checked-out tree,
  exactly as the `[Unreleased]` block does today. The comment is updated to describe the
  new symptom; the behaviour is unchanged.
- **A `merge=union` driver on `CHANGELOG.md`.** Unnecessary once humans stop editing it.
- **Adding `scripts/` to the denylists.** The denylist is deliberately inverted so build
  inputs added later count by default. Narrowing it is a separate decision with its own
  blast radius.
- **Changing quil's prose style, section vocabulary, or Keep a Changelog conformance.**

## Post-review hardening (as built)

Review found that validating the fragment **name** and nothing else was the design's
weak point — any legal name admitted whatever the file happened to be, and the promoter
splices bytes verbatim into a file that is pushed to master and published as the release
body. Validation now covers the file itself:

| Rule | Why |
|---|---|
| Must be a **regular file** | A symlink is read *through*. `fixed-notes.md -> ../.git/config` published the `AUTHORIZATION` header `actions/checkout` persists in the workspace — and in the release job that credential is `RELEASE_PAT`, a ruleset bypass actor. Reproduced end to end before fixing. |
| Must not be a **directory** | Passed `--check`, then failed mid-promote *after* `CHANGELOG.md` was already replaced. |
| Must be **non-empty** | Rendered a bare section heading. Reached the release page too, since the notes fallback only fires on a wholly blank body. `none-*` exempt — its content is ignored by design. |
| Must not contain **`## [`** | The release-notes extraction range ends at the next version heading, so such a line truncates the published notes and leaves a heading every later extraction mis-parses. |

Each is refused with its own reason rather than skipped, per the refuse-never-skip rule.

A third mode, `--validate`, does directory hygiene without the "something to promote"
requirement, so `ci.yml` applies these rules to **every** PR — including the docs-only
ones exempt from needing a fragment. Without the split an invalid file lands via a
docs-only PR and first surfaces as a failed release: the same gate/action divergence the
design exists to remove.

**Splice arithmetic.** `rest` and `total` must both come from `awk`. `wc -l` counts
newlines, `awk` counts lines, and they differ by one on a file with no trailing newline —
mixing them deleted every released section when the following heading was the last line
of an unterminated file. Pinned by case 23b.

Smaller corrections: the read guard tested the pipeline's status rather than `tr`'s;
`--filter-names` dropped an unterminated final line; a `section_heading` failure inside
`$( )` exited only the subshell; `..`-prefixed names matched neither glob; `--validate`
failures now annotate rather than only logging; and the `[ -d changelog.d ]` staging
guard was dropped because it silently staged nothing in the one case most worth staging.

Final state: 23 checks, green under dash and busybox ash, shellcheck clean at `-S style`.

## Docs updated

- `changelog.d/README.md` — the convention, worked example, collision note
- `CONTRIBUTING.md` — the PR section
- `.claude/CLAUDE.md` — Release Process
- `docs/versioning.md` — Release Process steps
- `ci.yml:91-96` job header, `ci.yml:153` error text, `release.yml:266-275` recovery
  guidance — stale mechanism descriptions
