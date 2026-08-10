# Code Review State: tui / pinned-attention

Last reviewed: 2026-08-09
Rounds completed: 1

Scoped to commit `c26bf1a` (daemon-owned, persisted attention pin + its four
display surfaces). The sibling feature on the same branch has its own state file,
`project-markers-colors.md` — the two were reviewed separately on purpose, so
this one got a full pass rather than a diminishing-returns round.

## Resolved (fixed in code; do not re-raise)
- [code-quality/I-1] **Real bug.** `Clear attention` gated its pin send on `pane.pinnedAttention`, which reports only what the last broadcast said — and `Mark attention` deliberately does not write locally. Mark → Clear inside the round trip therefore sent NOTHING and let the Mark's own broadcast restore the ◆ after the user cleared it, persisted. Two right-clicks; over ssh a window of hundreds of ms. Now sent unconditionally. Regression test `TestCtxMenu_ClearAttention_SendsBeforeTheMarkBroadcastLands`, verified to fail against the restored gate — round 1
- [code-quality/I-2] `Clear attention` also wrote the pin locally, which blinked the ◆ off/on/off against a broadcast already in flight and, with the link parked (`Router.Send` drops and returns nil), left the mark visibly gone until reconnect. The pin is send-only in both rows now, matching `ctxActAttention`; the three client-owned marks still clear in place — round 1
- [code-quality/I-3 + security/LOW-2] `sendPinnedAttention` resolved the destination inside the `tea.Cmd` (walking `m.projects` off the Update goroutine while `rebuildTabs` mutates it — the hazard `.claude/CLAUDE.md`'s input-pipeline invariant names) and used the loose `sendForPane`, which DROPS a message for an unreachable dest and returns nil. Now resolves `destOfPane` on the Update goroutine and sends via `sendForDestStrict`, naming the failure in the log — round 1
- [code-quality/S-4] `paneRow` emitted three segments where two carried the same style, doubling the SGR pairs on every pane row of every frame. Merged to two; only the pin genuinely differs — round 1
- [code-quality/S-5] Five comments the change invalidated: `workstate.go`'s `ackFocusedPane` (claimed the ctx-menu actions own the flag), `reconnect.go:748` (the preserve reason changed), `ctxmenu.go`'s "nothing is sent to the daemon", `projectRow`'s docstring ("working/blocked counts" — four now), and a worked example still using the pre-struct signature — round 1
- [code-quality/S-6] `.claude/rules/tui-dialogs.md` and `.claude/rules/hooks-and-sessions.md` still described the pre-change behaviour (sets the field locally; shares `unseenTabStyle`'s green; "all three marks"). Both corrected — round 1
- [code-quality/S-7] `counts().pinned` counts a focused pin while `tabPinnedAttention` excludes it, so focusing your only pinned pane drops the tab's ◆ while the project row still reads ◆1. Kept deliberately — they answer different questions ("which tab should I go to" vs "how many marks does this project hold") — and documented at `counts()` rather than left as two functions quietly disagreeing — round 1
- [security/LOW-1] `syncPaneMeta`'s unconditional copy is what makes a cross-client unmark work and also what removes the client's ability to refuse: a hostile remote daemon can assert and re-assert the pin. Display-only blast radius, and such a daemon already drives `blockedSince`/`unseen`. Accepted deliberately; the "user's own mark" framing in `.claude/rules/projects.md` now states the caveat and names the fix shape (a per-pane client dismissal epoch) — round 1
- [rules/M-1] gofmt drift in the `PaneInfo` struct — the new field's doc comment broke the alignment run. New drift, not pre-existing. `gofmt -w` applied — round 1
- [qa/1] No test drove a pinned tab through the real `hitTestTab`. Added `TestHitTestTab_PinnedTabShiftsLaterTabsByExactlyOneCell` — pin in the MIDDLE tab, asserts the hit map stays monotonic, every tab stays reachable, tabs before it do not move and tabs after it shift by exactly one column — round 1
- [qa/2] `sendPinnedAttention`'s degenerate paths (empty pane id, refused send) were uncovered. Added `TestSendPinnedAttention_DegenerateInputs` — round 1
- [qa/3] The roll-up had no test through the real `sidebarRows` with the pin in a NON-active project — the case the feature exists for. Added `TestSidebarRows_CountsThePinInANonActiveProject` — round 1
- [qa/4] No dedicated race test for `PinnedAttention`; the existing Overlay one only incidentally exercises a concurrent READ. Added `TestWorkspaceState_PinnedAttentionFlip_NoRace` mirroring it — round 1

## Noted, no change
- [code-quality] `renderStyledSegments`' cluster-boundary precondition: a leading space is not strictly a boundary under UAX #29 GB9b (`Prepend × Any`). Harmless — the inflation the precondition guards against needs an Extend/ZWJ/emoji codepoint (0 alone, ≥1 joined) where a space measures 1 either way. The comment now says "a space cannot change the width of the cluster before it" rather than claiming a boundary.
- [security] `pane.Name` is written and read WITHOUT `PluginMu` two lines from the correctly-guarded `PinnedAttention` read. Pre-existing, not introduced here. Not filed as techdebt this round — it is a one-line lock addition that belongs with a daemon-side change rather than a TUI PR.

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)
