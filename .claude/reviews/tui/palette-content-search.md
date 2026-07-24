# Code Review State: tui / palette-content-search

Last reviewed: 2026-07-24
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [quality/M-1] scroll window budgeted by rendered lines (a hit row counts as 2) — fixes vertical dialog overflow when the window fills with two-line pane-match rows — round 1
- [rules/M-1] fefd496 commit body rewrapped to <=72 columns — round 1
- [quality/L-2] removed the unreachable "No matching commands" render branch — round 1
- [rules/L-2] removed the dead `hitRows` test helper — round 1
- [qa/gap-1] added direct table-driven `TestClampPaletteCursor` — round 1
- [qa/gap-2] added closed-palette no-op subtests for the debounce and timeout Update branches — round 1
- [qa/gap-3] added `TestRenderCommandPalette_ScrollWindowWithHits` + `TestPaletteWindow_LineBudget` incl. a height bound — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [quality/L-4] content search fires on any non-empty query including a single character — intended "search as you type" design; both the security and quality reviewers rated it a note, not a defect; the command filter still works from the first character; the 150ms debounce + daemon per-pane cap bound the cost. User is aware (offered a >=2-rune guard, not requested). (round 1)
- [security] no findings — presentation-layer change, query never reaches an unsafe sink, excerpts sanitized daemon-side. (round 1)
