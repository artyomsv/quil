# Code Review State: quil / subagent-work-indicator

Last reviewed: 2026-08-02
Rounds completed: 2

## Resolved (fixed in code; do not re-raise)
- [code-quality/L1] `working` documented as derived but maintained imperatively across branches — now recomputed at a single derivation point in applyWorkTransition; edge actions key off the before/after pair (round 1)
- [code-quality/L2] Lost-SubagentStop recovery path undocumented — WorkEventSubagentStop const comment now states recovery is deferred to terminal edges by design (no age-based drain) (round 1)
- [rules/L1 + qa/1] ClassifyWorkEvent had no in-package test (0% coverage when hookevents suite runs alone) — added internal/hookevents/workstate_test.go with a direct table test (round 1)
- [rules/note] coalescedCount malformed-input paths untested — added TestCoalescedCount table (nil/missing/zero/negative/malformed/empty) (round 1)
- [qa/2] Mute-bypass intent for subagent events implicit — TestEmitEvent_MutedPaneWorkStateEventBypassesQueue now emits SubagentStart/SubagentStop/SessionEnd explicitly (round 1)
- [qa/3] Replay double-increment concern — verified NOT reachable: attach fires once per TUI process (Model.attached guard, no reconnect path), so event replay always starts from zeroed counters; oldest-first ring eviction can only orphan stops, which are ignored. Documented in applyWorkTransition doc comment (round 1)
- [BUG] Phantom SubagentStop drained live background agents — Claude Code emits one unpaired SubagentStop with an empty `agent_type` at the end of every main turn (measured 1:1 against Stop on every AI pane; empty-`agent_type` starts never occur). The bare-int ledger made stops fungible, so each phantom cancelled a live agent and the work indicator went dark mid-work (observed: a 27-minute agent with no spinner); 18 subsequently-arriving real named stops were then swallowed as orphans. Ledger is now `map[string]int` keyed by `agent_type` — a stop cancels only a start it can be matched to (round 2)
- [code-quality/Important-1] The fix rested on an unenforced upstream invariant — `workSubagentStart` now REFUSES a start with an empty `agent_type`, converting "the empty key is never live" from an observation into a code invariant. Pre-existing subagent tests all passed `nil` data and so ran entirely on the `""` key (the one shape production never emits); they now name an agent (round 2)
- [code-quality/Important-3 + review] Phantom reached the notification sidebar as a card titled `" done"` once per turn per AI pane, aggregated to `" done" ×N` and re-promoted on each occurrence — dropped at the producer (`runhook.go` SubagentStop with empty agent_type → no spool line); TUI match-by-name guard retained as defence in depth (round 2)
- [code-quality/Important-4] Three stale comments still described the old `(paneID, hook_event)` coalesce key, including the `pending` field's own contract — updated to `(paneID, hook_event, agent_type)` (round 2)
- [code-quality/Suggestion-5] `outstanding <= 0` guard unpinned (mutating it to `== 0` kept the whole tui suite green) — added TestApplyWorkTransition_OverCountedStopDrainsWithoutWedging; a producer-controlled `coalesced` larger than the outstanding count would otherwise leave a negative entry and wedge the spinner ON (round 2)
- [code-quality/Suggestion-6] `return` in the unmatched-stop branch bypassed the stated single derivation point — changed to `break` (round 2)
- [code-quality/Suggestion-7] Ingester lacked the named-vs-phantom coalescing case — added TestIngester_Submit_PhantomStopDoesNotSwallowNamedStop (round 2)
- [qa/L1] `Cancel()` unpinned against the new third key segment — it matches on the `paneID + "\x00"` prefix so agent_type-keyed pending entries were already reaped correctly, but nothing tested it; added TestIngester_Cancel_DropsPendingSubagentKeys (mutation-verified: exact-match Cancel leaks all 3 pending entries) (round 2)
- [security/L2] NUL in `hook_event`/`agent_type` made the composite coalesce key non-injective — `("SubagentStart", "\x00X")` collided with `("SubagentStart\x00", "X")`, coalescing last-wins and erasing an identity. Introduced by the round-2 key change; fixed with `keyFieldEscaper` (identity for all legitimate values, so real keys are byte-identical) (round 2)

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/L3] session.error flashes the tab green like success — intentional per the feature plan (round 1, carried from work-in-progress-indicators)

## Superseded premises (do NOT cite these as still-valid dismissals)
- [security/L1, round 1] "Unbounded subagents counter via unmatched SubagentStart — no remediation required (own-pane blast radius, **single int**, rate-limited upstream, self-heals on SessionEnd/process_exit)."
  **The "single int" premise is void as of round 2.** The counter is now a `map[string]int` keyed by producer-controlled `agent_type`: bounded in rate but no longer in space, and `clear()` empties a Go map without returning its allocated table, so "self-heals" is weaker than `= 0` was. Round-2 security review re-derived the conclusion (still LOW: the precondition is arbitrary code execution as the user, which already grants strictly more than growing a map slowly) but the *reasoning* must not be reused as written. A `maxTrackedSubagents` cap (64) was added, so the space bound now holds by construction rather than by trusting the child.
  Also noted: legitimate cardinality is per-*task* agent names (`impl-task7`, `rev-task7`), not a small fixed vocabulary — "bounded by a small fixed enum" would be a wrong premise to write down.

## Notes for future rounds
- Round-2 code review MUTATION-TESTED the new tests (reimplemented fungible-stop semantics and dropped `agent_type` from the coalesce key): all 5 new tests failed and only those. The suite genuinely pins the behaviour.
- Pre-existing, NOT introduced here, and out of scope for this feature: real internal host `artyom@192.168.6.12` is committed in `docs/superpowers/plans/2026-07-28-remote-auto-install.md` (lines ~1423-1426) and `docs/superpowers/plans/2026-07-30-remote-phase-3-remainder.md` (line ~230) in a PUBLIC repo. A prior round standardised test *fixtures* onto RFC 5737 but missed the plan docs. Worth its own ticket.
