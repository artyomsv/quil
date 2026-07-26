# Git repo pick dialog uses the pre-fix box-width accounting

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/tui/dialog.go:990` (`renderGitRepoPickDialog`) |
| Found during | Fixing the wrapped session rows in the pane setup dialog |
| Date | 2026-07-26 |

## Issue

`renderGitRepoPickDialog` budgets its rows as:

```go
// gitRepoPickWidth - 4: 2 for cursor marker, 2 for dialog border padding.
const pickMaxWidth = gitRepoPickWidth - 4
```

Two things are off, both the same class of bug just fixed in the setup dialog
(`setupBoxChrome`):

1. `dialogBorder` is `Padding(1, 2)` — 2 cells on **each** side, so padding
   costs 4, not 2.
2. lipgloss draws the border **inside** `Style.Width`, so the border costs
   another 2 on top of that.

A row is `"  "` (2) + the path, so the real limit is `gitRepoPickWidth - 2 - 6`,
i.e. `-8`, not `-4`. A repo path long enough to hit the budget renders up to 4
cells past the wrap limit, and reflow moves the tail onto its own line at
column 0 — the exact visual break the session list had.

Separately, `leftTruncPath` truncates by **rune count**, not display cells, so
a path containing CJK or emoji overflows further still. `truncateToWidth`
(palette.go) is the cell-aware equivalent, but it trims the wrong end for
paths — a left-truncating cell-aware variant does not exist yet.

## Risks

Cosmetic only, and only for long repo paths: the Alt+G repo picker is a short
list of discovered git repos (cap 10), and most paths are well under the
budget — which is why this has never been reported. No incorrect repo is ever
opened; only the rendering breaks.

## Suggested Solutions

1. Reuse the fixed accounting: `const pickMaxWidth = gitRepoPickWidth - 2 - setupBoxChrome`
   and rename `setupBoxChrome` to something dialog-generic (`dialogBoxChrome`)
   since it describes `dialogBorder`, not the setup screen specifically.
2. Add a cell-aware `leftTruncToWidth` next to `truncateToWidth` and switch both
   `leftTruncPath` call sites to it, so a wide glyph in a path cannot overflow.
3. Guard with the same two-sided constant test that pins `setupBoxChrome`
   (`TestSetupBoxChrome_MatchesLipglossWrapLimit`) — it already covers
   `dialogBorder`, so the git picker only needs a row-fits-the-box assertion.
