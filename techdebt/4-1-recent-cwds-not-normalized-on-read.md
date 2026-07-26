# LoadRecentCWDs does not normalize or de-duplicate on read

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/tui/recentcwd.go:59` (`LoadRecentCWDs`) |
| Found during | Code review of PR #106 (setup-dialog blurred selection marker) |
| Date | 2026-07-26 |

## Issue

`pushRecentCWD` applies `filepath.Clean` to the incoming directory and
de-duplicates the rest of the list against it with `pathEqual` (case-insensitive
on Windows). `LoadRecentCWDs` does neither — it unmarshals the JSON, caps the
length at `recentCWDMax`, and returns the raw strings:

```go
var list []string
if err := json.Unmarshal(data, &list); err != nil {
    return nil
}
if len(list) > recentCWDMax {
    list = list[:recentCWDMax]
}
return list
```

So a `recent-cwds.json` written by an older build, hand-edited, or synced from
another machine can hold `C:\Foo\` and `C:\Foo` — or `C:\foo` and `C:\Foo` on
Windows — as two entries for one directory. Both survive `existingDirs` (they
`os.Stat` fine) and render as two rows in the setup dialog's recent-locations
pick list. They collapse to one only as a side effect of the user picking that
directory again, which is when `pushRecentCWD` finally runs `pathEqual` over
them.

## Risks

Cosmetic only, and bounded by `recentCWDMax = 5`. The user sees what looks like
the same folder listed twice and cannot tell the rows apart. No incorrect
directory is ever spawned: both rows resolve to the same real path.

Specifically **not** a risk for the blurred-selection marker added in PR #106.
That marker matches on `pick[i] == m.cwdBrowseDir`, and in pick mode
`cwdBrowseDir` is always assigned by copying an element out of the very slice
being rendered — so exactly one row is marked even when two rows name the same
directory, and the comparison never depends on `pathEqual`'s case folding.

## Suggested Solutions

1. **Normalize + dedupe on read** (preferred). In `LoadRecentCWDs`, after
   unmarshal and before the cap, run each entry through `filepath.Clean` and
   drop later entries that `pathEqual` an earlier one. Cap last, so dedupe
   cannot leave fewer than `recentCWDMax` real entries. Keeps the one-writer
   invariant and fixes existing files on first read.
2. **Normalize on write only.** Have `SaveRecentCWDs` clean and dedupe. Simpler,
   but does not repair a file already on disk until the next save.
3. **Do nothing.** The list self-heals once the user re-picks the directory, and
   the cap keeps the blast radius at five rows.
