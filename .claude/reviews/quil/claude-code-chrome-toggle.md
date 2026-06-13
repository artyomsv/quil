# Code Review State: quil / claude-code-chrome-toggle

Last reviewed: 2026-06-14
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [qa/GAP-1] setupDialogWidth() unit test added — TestSetupDialogWidth_FloorGrowthAndCap covers floor / label-growth / terminal-cap branches — round 1
- [code-quality/L-3] toggleChrome=6 comment now covers all box glyphs ([x]/[ ]/(•)/( )), not just the checkbox form — round 1
- [code-quality/L-5] m.width>2 clamp guard now has an intent comment (skip until first WindowSizeMsg) — round 1
- [rules/L-6] test renamed TestLoadPluginTOML_ClaudeCodeSetup_ParsesPromptsCWDAndToggles (dropped stale "Permission" scope, now asserts chrome too) — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/L-1] --chrome capability expansion — informational only; correctly user-gated (default=false, independent opt-in, honest label). Quil just forwards a documented upstream flag (round 1)
- [code-quality/L-2] setupDialogWidth recomputed per render — negligible cost (map lookup + ~3-iter loop once per frame); not worth threading the value as a param (round 1)
- [code-quality/L-4] browsed CWD path raw-print can wrap on long paths — pre-existing behavior, not introduced by this diff; out of scope for the chrome-toggle change (round 1)
