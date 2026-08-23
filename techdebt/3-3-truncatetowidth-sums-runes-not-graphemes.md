# truncateToWidth sums independently-measured runes, so it can return a string wider than its budget

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/tui/palette.go` — `truncateToWidth`; ~10 call sites, several on remote-influenced text |
| Found during | Security review of PR #186 (offline pane area renders a remote-supplied detail) |
| Date | 2026-08-23 |

## Issue

`truncateToWidth` walks `for _, r := range s` and accumulates
`lipgloss.Width(string(r))` per rune, stopping when the sum would exceed the
budget. Its own doc comment claims "Cell-aware, NOT rune-count".

A rune measured ALONE does not always contribute that many cells in context. A
variation selector (U+FE0F) measures 0 on its own while promoting the character
before it from 1 cell to 2; a ZWJ sequence measures as its parts rather than the
single glyph the terminal draws. Summing independent measurements therefore
UNDER-counts, and the returned string can render up to roughly twice its budget.

`lastCellsToWidth` sits immediately below it in the same file, documents this
exact hazard verbatim — "a variation selector measures 0 alone while making the
pair before it 2, so summing independently-measured runes returns a suffix WIDER
than w" — and solves it with `uniseg` grapheme segmentation. `truncateToWidth`
was left on the per-rune path.

## Risks

- Every consumer treats the return value as bounded. `internal/tui/project.go`'s
  `offlineTabAreaMsg` is one of them, and its input is a string a remote daemon
  supplied — so the overflow is reachable by the far host, not only by a user
  with an emoji in a directory name.
- `lipgloss.Place` pads but never CLIPS (`PlaceHorizontal`: `gap := width -
  contentWidth`, `if gap <= 0 { return str }`), so an over-budget line escapes
  its box and the sidebar's `JoinHorizontal` emits rows wider than the terminal
  — the row-drift family this codebase has shipped before.
- The 8-cell margin `offlineTextCap` reserves is not enough to absorb a 2x
  overshoot on a long string.

## Not a PR #186 regression

The helper predates that PR, which only adds another caller. Flagged there
because the new caller renders remote-controlled text, which moves the reachable
input from "a name the user typed" to "a string the other machine chose".

## Suggested Solutions

1. Rewrite `truncateToWidth` on `uniseg.NewGraphemes`, mirroring
   `lastCellsToWidth` exactly — measure each grapheme CLUSTER with
   `lipgloss.Width` and accumulate that. The idiom already exists one function
   away, and `rivo/uniseg` is already a direct dependency for this reason.
   Preferred.
2. Keep the loop and re-measure the accumulated prefix with `lipgloss.Width`
   after each append, backing off when it exceeds the budget. Correct without a
   new dependency on the segmenter, but O(n²) on the prefix — acceptable only
   because these strings are bounded by a cell budget in the first place.
3. Do nothing in the helper and clamp at each call site instead. Rejected: ten
   call sites, and the next caller inherits the same wrong assumption from the
   doc comment.

Whichever is chosen, the doc comment's "Cell-aware, NOT rune-count" claim must
become true or be corrected — it is what invites callers to trust the bound.
