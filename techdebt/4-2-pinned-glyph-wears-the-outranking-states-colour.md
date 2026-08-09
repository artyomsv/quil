# paneRow's surviving ◆ pin is painted in the colour of the state that outranked it

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Small |
| Location | `internal/tui/sidebar.go` (`paneRow`, the `pinnedAttention && glyph != glyphPinned` suffix) |
| Found during | Code review of PR #146 (project sidebar badge colouring) |
| Date | 2026-08-09 |

## Issue

`paneRow` picks ONE style for the whole line from the pane's winning state, then
appends the pin as a suffix when a live state outranked it:

```go
if pane.pinnedAttention && glyph != glyphPinned {
    suffix += " " + glyphPinned
}
...
return style.Render(padOrTrunc(prefix+label+suffix, w))
```

`style` there is `sidebarBlockedStyle` (amber) or `sidebarWorkingStyle` (blue),
so the ◆ that survives being outranked is drawn in the colour of the state that
outranked it and never in `sidebarPinnedStyle` (141). `sidebarPinnedStyle` is
reachable only on the branch where the pin WINS — the one case where the suffix
does not exist.

This is the same defect PR #146 fixed in `projectRow`, in the sibling function:
a row that carries two states painted in one colour. It was left out of that PR
deliberately, as a behaviour change that belongs in its own commit rather than
inside a fix scoped to the project rows.

The pin is still visible, which is what the surrounding comment promises ("a pin
never goes dark under a transient state") — so this is a weaker signal rather
than an absent one, hence Low.

## Risks

- The sidebar's colour vocabulary says something slightly false: a purple mark
  drawn amber reads as part of the blocked state rather than as the deliberate,
  never-auto-cleared pin it is.
- Low blast radius. Cosmetic, on a row that is already showing the pane.

## Suggested Solutions

1. Rebuild `paneRow` on `renderStyledSegments` (added in PR #146) and give the
   pin suffix its own `sidebarPinnedStyle` segment. Note the helper's
   cluster-boundary precondition — the suffix already begins with a space, so it
   is satisfied, but the label segment ends wherever `truncateCells` cut and the
   suffix budget arithmetic would have to move with it.
2. Leave it and document the choice in `.claude/rules/projects.md`, if the
   intent is that a row carries exactly one colour and the pin's shape alone is
   the signal.
