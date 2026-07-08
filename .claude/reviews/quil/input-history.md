# Code Review State: quil / input-history

Last reviewed: 2026-07-08
Rounds completed: 2

## Resolved (fixed in code; do not re-raise)
- [greptile/P1] Compact gated on the RAW line count, so historical synthetic junk could push a few-real-prompt pane over keepLast and trigger a rewrite that races the hook's append — gate now uses the filtered (real) entry count; junk is a finite non-growing residue (Append no longer records it), purged only on a genuine over-cap trim — round 2
- [greptile/P2] A real prompt starting by quoting `<task-notification` was dropped by Append — IsSyntheticPrompt now requires BOTH the open tag AND the `</task-notification>` close tag, so only a pure complete block is filtered; a sentence quoting the tag is preserved — round 2
- [code-quality/M-1] Compact rewrote on every previews read (whenever any synthetic entry was on disk), racing the Claude hook's cross-process O_APPEND and risking lost prompts — Compact now rewrites ONLY when the file exceeds keepLast lines; Read-time filtering keeps the display clean, junk is purged as a side effect of ring eviction — round 1
- [code-quality/L-1] `<task-notification>` prefix was brittle to attribute drift — prefix now omits the closing `>` so `<task-notification version="2">` still matches — round 1
- [code-quality/L-2] Negative keepLast could panic in the trim slice — guarded with `if keepLast < 0 { keepLast = 0 }` — round 1
- [code-quality/L-3] filterSynthetic allocated a fresh slice even when nothing needed filtering — added a fast path returning the input slice unchanged in the no-synthetic case — round 1
- [rules/L-4] TestIsSyntheticPrompt lacked the `_Scenario_Expected` suffix — renamed to TestIsSyntheticPrompt_MatchesKnownTags — round 1
- [qa/test-gap] Added coverage: all-synthetic Read, multiple interspersed synthetic entries, combined over-cap purge+trim, under-cap leaves-disk-untouched, Compact on missing file — round 1
- [rules/doc] CLAUDE.md internal/panehistory paragraph updated to document the synthetic-turn filter and the Compact race-avoidance rule — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/L-oversize] An oversized file whose large entries don't fit keepLast within maxReadBytes is not shrunk on disk — pre-existing behavior unchanged by this diff, reads stay memory-bounded via the tail guard, disk-only edge-of-edge (round 1)
