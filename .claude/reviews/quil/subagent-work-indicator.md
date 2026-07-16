# Code Review State: quil / subagent-work-indicator

Last reviewed: 2026-07-17
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/L1] `working` documented as derived but maintained imperatively across branches — now recomputed at a single derivation point in applyWorkTransition; edge actions key off the before/after pair (round 1)
- [code-quality/L2] Lost-SubagentStop recovery path undocumented — WorkEventSubagentStop const comment now states recovery is deferred to terminal edges by design (no age-based drain) (round 1)
- [rules/L1 + qa/1] ClassifyWorkEvent had no in-package test (0% coverage when hookevents suite runs alone) — added internal/hookevents/workstate_test.go with a direct table test (round 1)
- [rules/note] coalescedCount malformed-input paths untested — added TestCoalescedCount table (nil/missing/zero/negative/malformed/empty) (round 1)
- [qa/2] Mute-bypass intent for subagent events implicit — TestEmitEvent_MutedPaneWorkStateEventBypassesQueue now emits SubagentStart/SubagentStop/SessionEnd explicitly (round 1)
- [qa/3] Replay double-increment concern — verified NOT reachable: attach fires once per TUI process (Model.attached guard, no reconnect path), so event replay always starts from zeroed counters; oldest-first ring eviction can only orphan stops, which are ignored. Documented in applyWorkTransition doc comment (round 1)

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/L1] Unbounded subagents counter via unmatched SubagentStart — security-officer's own verdict: no remediation required (own-pane blast radius, single int, rate-limited upstream, self-heals on SessionEnd/process_exit) (round 1)
