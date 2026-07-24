# Code Review State: quil / recent-locations-setup-dialog

Last reviewed: 2026-07-24
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/L-1] LoadRecentCWDs Lstat symlink rejection added (matches persist/notes.go) — round 1
- [security/L-2] LoadRecentCWDs caps to recentCWDMax after unmarshal — round 1
- [code-quality/S-1] pushRecentCWD `max` param renamed to `limit` (no builtin shadow) + negative guard — round 1
- [code-quality/S-4] recentcwd_test expectation loop simplified to literal slices — round 1
- [qa/G-1] handleCreatePaneSplit push+persist integration test added (QUIL_HOME tempdir) — round 1
- [qa/G-2] pathEqualCase extracted; both case branches tested portably — round 1
- [qa/G-3] LoadRecentCWDs corrupt-JSON + oversized-cap tests, SaveRecentCWDs write-error test added — round 1
- [greptile/P2] SaveRecentCWDs MkdirAll's parent dir before atomic write (matches persist/snapshot.go) — round 1
- [rules/H-1] commit message stray `@` lines fixed (PowerShell here-string ran in Bash); subject now valid conventional-commit — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/S-2] No Ctrl+V paste-to-jump in pick mode — "Browse…" reaches the browser (with paste) in one keystroke; duplicating the paste path isn't worth it (round 1)
- [code-quality/S-3] Stale entries linger in recent-cwds.json until aged out by the cap — intended; existingDirs filters the display, disk residue is harmless and bounded (round 1)
- [code-quality/L-3] SaveRecentCWDs returns raw (unwrapped) errors — deliberately mirrors SaveInstances; wrapping only the new sibling would diverge from the established pattern (round 1)
- [rules/L-2] branch named `feat/` not `feature/` — renaming a pushed branch is disruptive; noted for the next branch (round 1)
