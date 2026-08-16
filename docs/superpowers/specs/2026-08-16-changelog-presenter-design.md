# Changelog Presenter — "What's New" after an upgrade

**Date:** 2026-08-16
**Status:** Design approved, revised after pre-implementation review
**Branch:** `feat/changelog-presenter`

## Problem

A user running v1.48.0 who upgrades to v1.60.0 is told nothing about what changed.
The only available answer is `CHANGELOG.md`, which is deliberately verbose — the
v1.59.3 "Fixed" entry alone runs to roughly two hundred words explaining why the work
indicator went dark. That prose is correct and worth keeping, but nobody reads twelve
releases of it.

The gap is a second register: one line per change, scannable in about thirty seconds,
covering exactly the versions the user skipped and no others.

Nothing in the repository produces text at that register today, and nothing in the
running binary notices that the version changed.

## What exists already

| Thing | Where | Relevance |
|---|---|---|
| Per-PR changelog fragments | `changelog.d/<type>-<slug>.md` | The authoring surface a headline can attach to |
| Fragment promoter | `scripts/promote-changelog.sh` | Sole owner of fragment grammar; runs once per release |
| Release commit | `.github/workflows/release.yml:352,363` | Stages an **explicit file list** plus a path-scoped `-A` for `changelog.d` |
| Update pipeline | `internal/update/`, `internal/daemon/update.go`, `internal/tui/update.go` | Checks GitHub, stages, shows a once-per-version "Update available" dialog |
| Version marker | `notified.json` via `update.LoadNotifiedVersion` | Records a version the user was *told about* — not one they ran |
| Semver comparison | `internal/version.Compare` / `.Parsed` | Reused; no new dependency |
| Dialog idiom | `internal/tui/dialog.go`, `internal/tui/model.go:910` | Disclaimer / migration / update-notice pattern to follow |
| Text truncation | `internal/tui/palette.go:890` — `truncateToWidth` | Already exists in this package; reused, not redeclared |

There is no record of which version the user last *ran*. That is the missing primitive.

## Decisions

**Headline text is authored, not derived.** Each fragment carries a one-line headline
written by the PR author. Deriving it from the fragment's opening bold sentence was
rejected because the convention is followed inconsistently and some leads run long;
deriving it from conventional-commit subjects was rejected because those are written
for developers (`feat(keymap): add the keybinding action registry` tells a user
nothing).

**Data is embedded at build time, not fetched.** The promoter appends each release's
headlines to a checked-in file that the binary embeds. Fetching release bodies from
GitHub at launch was rejected: it needs network at the one moment the user just
restarted, costs N API calls for N skipped versions, and fails entirely offline. The
embedded file is always sufficient, because the binary doing the presenting is by
definition newer than every version in the range it must describe — and the version
gate (`cmd/quil/main.go:433`) refuses any TUI/daemon version difference, so there is no
split-brain where one side knows a version the other does not.

**The surface is a startup dialog plus an F1 entry.** It follows the established
disclaimer / migration / update-notice idiom and is guaranteed to be seen exactly once.
A sidebar notification was rejected because the sidebar may be closed and a notice
nobody opens is the same as no feature; a dedicated pane was rejected as disproportionate
to a thirty-second read.

**Fixes collapse, features do not.** New, Changed and Security render in full; fixes
collapse to a count that expands on a keypress. This holds the read time roughly
constant regardless of how many releases were skipped, without hiding anything. Showing
everything scrolled was rejected as a wall of text; a hard cap with a rollup was
rejected because an arbitrary cut can drop an important change below the line.

**Nothing is backfilled — not prose, not version numbers.** The data file starts empty
and the convention starts at the release that ships this feature. A version-index
backfill was considered and cut: it would only ever have fed an "N earlier releases"
count that no reachable state can produce, because only post-feature binaries write the
marker, so every computable window begins at or after the first release carrying data.
Cutting it removes the count, the epoch marker, and the `Unsummarised` field together —
see Known Limitations for what the user experiences at the transition.

---

## §1 — Fragment format

A fragment gains a strict front-matter block. It must be the first thing in the file:

```markdown
---
headline: Option+Shift shortcuts work again on macOS
---
- **`Option+Shift+<letter>` reaches the shell again on macOS.** Chord parsing
  lowercased the key, and the same parser reads the incoming key press…
```

### Grammar

Exactly three lines: `---`, one `headline:` line, `---`. No other keys. No blank line
permitted inside the block. This is not YAML and must not be parsed as YAML — it is a
fixed three-line shape so that a POSIX-sh script can read it with `head` and `awk`.

### Rules

| Rule | Reason |
|---|---|
| Required for `added`, `changed`, `deprecated`, `removed`, `fixed`, `security` | A fragment without one vanishes silently from the presenter — the same lost-prose failure the fragment README already argues against |
| Forbidden for `none` and `internal` | Neither is user-facing. `none-*` carries no prose by design; `internal` renders under a heading users are not the audience for |
| Single line | The presenter renders one bullet per headline; a wrapped headline defeats scanning |
| At most 64 bytes | Fits the dialog's content width on one line (see §4). Measured in bytes under `LC_ALL=C` so the check is deterministic across shells; a headline using multi-byte characters is charged for them, which errs toward fitting |
| No control characters, no `"`, no `\` | Lets the promoter emit the data file with `printf` and no escaping logic |
| **The fragment must still hold prose once the block is removed** | The existing emptiness check runs on the raw bytes, so a file that is *only* a headline block passes it and then promotes a section heading with nothing under it — precisely the failure that check exists to prevent |

Printable non-ASCII is allowed — `→` and `•` already appear in Quil's dialogs. What is
forbidden is precisely what would make the generated file ambiguous or the shell writer
need quoting logic. The alternative — a shell script emitting JSON containing arbitrary
user prose — is a quoting bug waiting for the first headline with a quote in it.

### Enforcement

All of it lives in `validate_fragment_dir` in `scripts/promote-changelog.sh`, extending
the existing per-fragment checks. The grammar stays in that one script for the reason
already recorded in its header comment: `ci.yml` gates PRs by calling into the script
rather than approximating it, so a gate cannot disagree with the action it guards.

`ci.yml` runs `--validate` unconditionally on every pull request (`ci.yml:145`), ahead
of the docs-only exemption, so the new checks need no new invocation. **One line does
change:** the failure annotation at `ci.yml:147` enumerates the fragment rules for the
Files-changed tab, and must mention the headline. Leaving it stale would produce exactly
the "gate whose reason you have to go digging for" that its own surrounding comment
warns against.

### Promotion

`promote()` currently splices each fragment through
`{ tr -d '\r' < "$f" || exit 1; } | trim_blank_lines`. A `strip_front_matter` filter is
inserted between those two stages, so `CHANGELOG.md` output is byte-identical to today.
The filter drops a leading `---`…`---` block and nothing else; a file with no block
passes through unchanged.

---

## §2 — Generated data file

`internal/changelog/highlights.txt`, embedded with `//go:embed`. Newest release first,
mirroring `CHANGELOG.md`.

```
# generated by scripts/promote-changelog.sh — do not edit
V 1.61.0 2026-08-20
A Keybindings are now fully remappable
F Paste no longer drops the first character
V 1.60.5 2026-08-18
C F1 → Shortcuts is derived from your keymap
V 1.60.4 2026-08-17
```

### Records

| Prefix | Meaning |
|---|---|
| `#` | Comment; ignored |
| `V <version> <date>` | Starts a release. Written on **every** release, including one whose only fragment was `none-*` |
| `A` `C` `D` `R` `F` `S` | One headline, under Added / Changed / Deprecated / Removed / Fixed / Security |

Record letters map one-to-one onto fragment types, so the file is lossless. The
presenter does its own folding (§4); the data does not pre-decide it.

There is no epoch marker. Every record in the file is authoritative by construction:
the file starts empty and only `promote()` ever writes to it, so a version is either
present with complete data or absent entirely.

### Why a line format and not JSON

A headline is single-line by definition, so newline is the only delimiter and escaping
never arises. `printf '%s %s\n'` is the whole writer. A `bufio.Scanner` is the whole
reader. Shell-generated JSON would need quote and backslash escaping on text authored
by whoever opened the PR.

### Why every release gets a `V` record

The header counts releases crossed, and `Previous` walks the record list to build the
F1 window. A release whose only fragment was `none-*` still happened, still moved the
user's version, and must still be counted and traversable. It simply renders no bullets.

### Writing it

`promote()` appends after `CHANGELOG.md` is written and **before** the fragments are
deleted — it reads the same files. New records are prepended (write to temp, `cat` the
existing file, `mv`), matching the newest-first order.

`--validate` and `--check` never write it.

Promoting a version already present is refused, and the check runs inside `check()` —
before `CHANGELOG.md` is rewritten, not midway through. A refusal that fires after the
splice leaves a duplicated `## [x.y.z]` section behind in the file it was protecting.

**`release.yml` MUST be changed.** It does not use a bare `git add -A`: the release
commit stages an explicit list (`release.yml:352`) plus a path-scoped `git add -A --
changelog.d` (`release.yml:363`) whose `-A` exists to record the *deletions* of consumed
fragments. `internal/changelog/highlights.txt` is therefore not staged by anything, and
without this change every release would write its records into the CI workspace and
discard them — the tagged commit that goreleaser checks out would embed a file that
never grows, and the shipped binary would contain no data at all. The fix is to add the
path to the explicit list at `:352`. No test can catch this omission, because the
release job's own `go test` runs against the workspace copy, which *is* updated.

### Line endings

`.gitattributes` gains `internal/changelog/highlights.txt text eol=lf`. The file is
`go:embed`-ed and parsed line-by-line, so a CRLF checkout puts a trailing `\r` at the
end of every record — which lands inside the headline text and is rendered into the
TUI. This is the same class of failure the existing `internal/shellinit/scripts/**`
entry documents, and `internal/remoteinstall/embed_test.go` is the precedent for pairing
the attribute with a no-CR assertion.

### Go API

`internal/changelog` imports the standard library plus `internal/version`.

```go
type Kind int

const (
    KindAdded Kind = iota
    KindChanged
    KindDeprecated
    KindRemoved
    KindFixed
    KindSecurity
)

// Entry is one headline and the kind of change it describes.
type Entry struct {
    Kind Kind
    Text string
}

// Release is one version's highlights, in file order.
type Release struct {
    Version string
    Date    string
    Entries []Entry
}

// All returns every recorded release, newest first.
func All() []Release

// Latest returns the most recent recorded release, or nil when the file is
// empty. The F1 path uses it directly rather than deriving a range.
func Latest() *Release

// Window is what the dialog renders.
type Window struct {
    From     string    // the version the user was running
    To       string    // the version they are running now
    Total    int       // every release in (From, To], including ones with no entries
    Releases []Release // those with entries, newest first, deduplicated
}

// Between returns the window for an upgrade from → to. Both bounds are
// compared with version.Compare, so the range is exclusive-low and
// inclusive-high. Returns ok=false when either bound is unparseable; a
// from at or after to yields an empty window with ok=true.
func Between(from, to string) (Window, bool)
```

A malformed record is skipped, not fatal. The file is generated and checked in, so a
malformed line means a bug in the generator — and losing one bullet is a better
outcome for the user than a TUI that refuses to start.

`Window.Releases` drops releases with no entries; `Window.Total` still counts them.
A window with no releases carrying entries means there is nothing to say, and no dialog
opens.

Deduplication happens inside `Between`, not in the renderer — it is a property of the
window, so the F1 path and the startup path cannot disagree about it, and it is testable
without constructing a `Model`.

The package-level parse cache is guarded by a `sync.Once` held **behind a pointer**, so
tests can swap the corpus without copying a lock. `go vet`'s copylocks check runs in CI
(`ci.yml:47`) and in the release job, and a test helper that assigns a `sync.Once` by
value fails it.

---

## §3 — Detecting the upgrade

A new marker at `$QUIL_HOME/update/lastrun.json` — beside `notified.json`, under the
same `UpdateDir()`:

```json
{ "version": "1.60.0" }
```

It lives in `internal/update/state.go` as `LoadLastRunVersion` / `SaveLastRunVersion`,
because that file already owns the small-marker idiom and its package doc already
describes itself as holding "the small state files shared between daemon and TUI".

`notified.json` is deliberately not reused. It means *"a version I told you about"*.
Conflating the two would let dismissing an update offer suppress the what's-new for a
version the user never installed.

`config.LastRunPath()` is added alongside `config.UpdateNotifiedPath()`.

### Decision table

Evaluated in `NewModel`, which already runs the disclaimer decision.

| `lastrun` vs current | Behaviour |
|---|---|
| current is not parseable semver | Skip entirely, writing nothing — dev and unstamped builds. Recording `"dev"` would make the next real launch look like a downgrade |
| absent or `""` | Fresh install, or a first launch after upgrading from a pre-feature version — indistinguishable. Record the version; show nothing |
| equal | Record; show nothing |
| greater than current | Downgrade. Record; show nothing |
| less than current | Record; compute the window; show the dialog if it carries any release with entries |

The marker is written whenever the version is parseable and differs from what is
recorded — not only when the dialog opens. A write failure is logged and otherwise
ignored; the cost is the dialog appearing once more.

Because the check compares a persisted string against the running binary's version, it
is indifferent to *how* the upgrade happened — self-update, `go install`, a package
manager, or a manual unzip all change the binary and none of them are otherwise
observable.

### Dialog priority

The documented chain becomes:

```
migration  >  what's-new  >  update-notice  >  disclaimer
```

What's-new outranks the update notice because telling someone to upgrade again in the
same breath as "here is what you just got" is noise. No change to
`maybeShowUpdateNotice` is required: its guard at `internal/tui/update.go:378-380` is
`if m.dialog != dialogNone && m.dialog != dialogDisclaimer { return }`, so an open
what's-new already suppresses it — and that return happens *before* the marker save at
`:386`, so the notice reappears on the next launch rather than being lost.

`NewModel`'s disclaimer condition gains one term, so the what's-new replaces the
disclaimer for that launch. The disclaimer reappears next launch, which is its existing
documented behaviour.

---

## §4 — The dialog

```
┌─ What's new in Quil ─────────────────────────────────┐
│  You updated  v1.57.2 → v1.60.0         3 releases   │
│                                                      │
│  New                                                 │
│   • Keybindings are now fully remappable             │
│   • Projects group tabs above the tab bar            │
│   • Desktop notifications on Windows                 │
│                                                      │
│  Changed                                             │
│   • F1 → Shortcuts is derived from your keymap       │
│                                                      │
│   › 23 fixes                        → to expand      │
│                                                      │
│   github.com/artyomsv/quil/releases                  │
│                     [  OK  ]                         │
└──────────────────────────────────────────────────────┘
```

`internal/tui/whatsnew.go` holds the renderer and key handler. It renders a
`changelog.Window`, so the startup path and the F1 path share one implementation and
differ only in how the window is built:

- **Startup:** `Between(lastRunVersion, currentVersion)`.
- **F1 → What's New:** a one-release window built directly from `Latest()`. Deriving it
  as `Between(previous, current)` was rejected: on the release that ships this feature
  there is no previous record, so the window would come back empty and the menu row
  would be a silent no-op on exactly the version that introduces it.

### Width

This dialog uses `min(76, termWidth-4)` rather than the shared `dialogWidth = 60`. It is
a reading surface, not a button form, and 60 columns leaves roughly 53 usable after the
border and bullet prefix — short enough that headlines stop being sentences. The 64-byte
headline cap in §1 is set against this width. Rows are cut with the package's existing
`truncateToWidth` (`internal/tui/palette.go:890`) — truncation, not wrapping, so one
bullet stays one row.

The startup dialog is constructed in `NewModel`, before any `WindowSizeMsg` has arrived,
so the width falls back to the maximum on the first frame and corrects itself on the
first resize. That is cosmetic and self-healing.

### Sections

Rendered in this order, each omitted when empty:

| Section | Sources | Collapses |
|---|---|---|
| New | `A` | No |
| Changed | `C`, `D`, `R` | No |
| Security | `S` | Never |
| Fixes | `F` | Yes |

Deprecated and Removed fold into Changed because the distinction matters to a changelog
reader and not to someone scanning what happened while they were away. Security never
collapses regardless of count.

### Keys

| Key | Action |
|---|---|
| `→` / `l` | Expand the fixes list |
| `←` / `h` | Collapse it |
| `↑` `↓` `PgUp` `PgDn` | Scroll, engaged only when the content overflows the terminal |
| `Enter` / `Esc` / `q` | Dismiss |

`Enter` dismisses rather than expanding, matching the disclaimer and update-notice
dialogs where `Enter` activates the focused button. The fixes list is auto-expanded when
the count is 5 or fewer — collapsing three items is friction with no benefit.

### Deduplication

Exact-string match across releases, newest occurrence kept. A fix shipped in 1.58.1 and
described identically again in 1.59.0 appears once. This directly serves the "do not
repeat ourselves" requirement and is safe precisely because headlines are short,
authored strings rather than generated prose.

### Other behaviour

- **Header** shows `v<from> → v<to>` and `Window.Total`, which counts every release in
  the range including ones that carried no user-facing change, because they did happen.
  The count is omitted when it is 1.
- **Remote mode** — the dialog describes the local binary and is shown normally. Unlike
  the update notice, there is no wrong-machine hazard: the version gate guarantees the
  remote daemon is the same version, and the dialog offers no action.

---

## §5 — Files

### New

| Path | Purpose |
|---|---|
| `internal/changelog/changelog.go` | Embed, parse, `All`, `Latest`, `Between` |
| `internal/changelog/highlights.txt` | Generated data. Ships holding only its comment line; the first release cut after this lands writes the first records |
| `internal/changelog/changelog_test.go` | Parser and window tests |
| `internal/tui/whatsnew.go` | Dialog renderer and key handler |
| `internal/tui/whatsnew_test.go` | Dialog tests |
| `changelog.d/added-whatsnew.md` | This feature's own fragment, carrying a headline — the feature validates itself |

### Modified

| Path | Change |
|---|---|
| `scripts/promote-changelog.sh` | `strip_front_matter`; headline validation and post-strip emptiness in `validate_fragment_dir`; duplicate-version refusal in `check()`; highlights emission in `promote()` |
| `scripts/test-promote-changelog.sh` | Existing fixtures gain headlines; new cases appended from 25 |
| `.github/workflows/release.yml` | Stage `internal/changelog/highlights.txt` in the release commit (`:352`) |
| `.github/workflows/ci.yml` | Failure annotation text mentions the headline rule (`:147`) |
| `changelog.d/README.md` | Document the headline block and its rules |
| `.gitattributes` | `internal/changelog/highlights.txt text eol=lf` |
| `internal/config/config.go` | `LastRunPath()` |
| `internal/update/state.go` | `LoadLastRunVersion`, `SaveLastRunVersion` |
| `internal/tui/model.go` | `dialogWhatsNew` enum, Model fields, what's-new decision in `NewModel` |
| `internal/tui/dialog.go` | F1 row, key dispatch, render dispatch |
| `.claude/CLAUDE.md` | Release-process paragraph; correct the "stages with `git add -A`" phrasing that caused the staging misreading |

---

## §6 — Testing

### `internal/changelog`

Table-driven against a golden corpus held in the test, not the embedded file — the
embedded file changes every release and a test bound to its contents would fail on
release commits. The corpus is injected through a pointer-held cache seam, never by
copying the `sync.Once`.

- Parse: comments, `V`, each entry letter, unknown letters skipped, entries before any
  `V` skipped, malformed lines skipped without error, empty file.
- `Between`: exclusive-low and inclusive-high bounds; `from` equal to `to`; `from` newer
  than `to`; releases with no entries dropped from `Releases` but counted in `Total`;
  unparseable bounds return `ok=false`.
- Dedupe: identical headlines across two releases collapse to the newest.
- Prerelease: `version.Compare` treats `1.2.3-rc1` as equal to `1.2.3`, so an upgrade
  from a prerelease to its release yields an empty window. Asserted so the behaviour is
  deliberate rather than discovered. (`promote()` refuses any non-`X.Y.Z` version at
  `promote-changelog.sh:267`, so the file itself never holds one.)
- `Latest`: newest record; nil on an empty file.
- The embedded file: every record parses, ordering is strictly newest-first, no CR bytes.

### `internal/tui`

- Dialog priority: migration beats what's-new; what's-new replaces the disclaimer; the
  update notice yields to what's-new **without writing its marker**, so the offer is not
  lost.
- First run (absent marker) shows nothing and records the version.
- Downgrade shows nothing and records; a non-release version shows nothing and records
  nothing.
- Collapse and expand driven **through `Update`**, not by calling the handler directly —
  a direct-call test can pass against code the call site makes unreachable.
- F1 → What's New opens a non-empty dialog even when the embedded file holds exactly one
  release.
- Render at a 60-column terminal: no wrapped bullets, no row exceeding the width.
- `t.Setenv("QUIL_HOME", t.TempDir())` in every test that touches a marker. Docker's
  throwaway `/root` masks home writes, so a test that is green in CI can pollute the real
  `~/.quil` when the same binary is run on the host.

### `scripts/test-promote-changelog.sh`

The suite currently has 23 labelled cases running to label 24. Existing fixtures for
user-facing types must gain headlines — the new validation refuses them otherwise, and
roughly eleven cases expect a successful `--check` or promote. New cases append from 25.

- Headline required for each user-facing type; rejected when missing.
- Headline forbidden for `none` and `internal`; rejected when present.
- Rejected when the block is unclosed, uses a wrong key, exceeds 64 bytes, or carries a
  quote, backslash, or control character. Printable non-ASCII accepted.
- A fragment that is *only* a headline block is refused as empty.
- Front-matter absent from the promoted `CHANGELOG.md`; a fragment without a block
  promotes byte-identically to today.
- `highlights.txt`: correct record letters, type order, newest-first prepending, a `V`
  record written for a `none`-only release, no entries from an `internal`-only release.
- Promoting a version already present is refused **and `CHANGELOG.md` is untouched**.
- `--validate` and `--check` write nothing.

### Success check

```sh
./scripts/dev.sh test internal/changelog
./scripts/dev.sh test internal/tui
./scripts/dev.sh test internal/update
sh scripts/test-promote-changelog.sh
./scripts/dev.sh vet
```

all green — `dev.sh test` takes one package argument and silently drops the rest, so
each package is its own invocation — followed by a dev-mode launch with a hand-edited
`.quil/update/lastrun.json` showing the dialog.

---

## Known limitations

**A user upgrading from a pre-feature version sees nothing on that one launch.** Their
`lastrun.json` does not exist, and an absent marker is indistinguishable from a fresh
install — the binary that would have written it did not contain this feature. The
marker is written on that launch, so every subsequent upgrade works as designed. This
is inherent to any marker-based scheme and is not closed by backfilling data.

**The headline can drift from the body.** Nothing enforces that they agree; they are
simply authored in the same file at the same moment. This is the accepted cost of
authoring over derivation.

**F1 → What's New shows only the most recent release.** Browsable full history is out of
scope; the footer links to the releases page.

**A fix legitimately re-shipped under an identical headline appears once**, silently,
because deduplication is by exact string.

## Out of scope

- Backfilling anything — prose or version numbers.
- Serving highlights over IPC for a remote daemon — the version gate makes client and
  daemon the same build.
- Localisation.
- A machine-readable feed or an MCP tool exposing highlights.
