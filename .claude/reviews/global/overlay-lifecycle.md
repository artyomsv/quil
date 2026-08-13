# Code Review State: global / overlay-lifecycle

Last reviewed: 2026-08-13
Rounds completed: 1

Branch `fix/overlay-lifecycle` (PR #153). Four agents in parallel: security,
code-quality, rules-compliance, qa. Round 1 found 0 Critical, 0 High, and six
findings worth fixing — all fixed in the same session and re-verified.

Note for a future round: this feature had already been through per-task reviews,
a whole-branch review and a scoped re-review BEFORE this pass. Round 1 here
still found two real defects, both in the same blind spot — paths that change
what is on screen, or that assert visibility, without reporting truth. If a
round 2 happens, start there rather than re-reading the sweep.

## Resolved (fixed in code; do not re-raise)

- [qa/Gap2 + quality/I4] Both daemon-side call sites were unpinned — commenting out `enforceOverlayCap(pane.ID)` failed no test, so the whole feature could be made inert with a green suite. Now pinned by `TestCreatePaneAt_OverlayPastTheCapEvictsThroughTheCreatePath` (controller re-verified by mutation on a scratch copy: removing the call now fails) — round 1
- [quality/I3] Project switching bypassed truth reporting, so a background project's overlay never idle-expired — the original leak through a different door — round 1
- [quality/I1] A daemon-side active-tab change adopted via broadcast (MCP `switch_tab`, another client) reported nothing — round 1
- [security/M2] Overlay visibility was a global last-writer-wins assertion; with two TUIs one client's tab switch destroyed the other's on-screen overlay. Now a per-connection claim; hidden only when no attached client claims it. This subsumed `markOverlaysHidden`, so there is one mechanism rather than two — round 1
- [quality/S1] Every successful eviction logged `pane not found` and broadcast twice — round 1
- [quality/I2] A visibility-only `MsgUpdatePane` triggered a full workspace broadcast carrying none of the changed data, on a path that now fires on every tab switch — round 1
- [security/L1] `time.Duration(IdleTimeoutMinutes) * time.Minute` overflowed silently; `setOverlayPolicy` now clamps to `[0, 525600]` at both entry points — round 1
- [qa/Gap1, qa/Gap3, quality/S2] Test gaps: two of five visibility flip sites unverified against the wire, `handleUpdatePane` dispatch untested for the overlay field, nothing pinned that all four `jumpToPane` callers propagate the returned Cmd — round 1
- [quality/S3, S4, S7] `overlayPolicyCmd` abandoned remaining destinations on the first encode error; the sweep's lock comment described a shape the code did not have; `attachedClientCount` had only a test caller — round 1
- [rules/MEDIUM-2] The ATTACHED-vs-CONNECTED distinction and the broadcast-batching rules were not in `daemon-lifecycle.md`, which owns `internal/daemon/**`. Added there; the overlay-specific detail stays in `plugins.md` rather than being duplicated — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)

- [security/M1] `MsgOverlayPolicy` is unauthenticated, and `max_live = 0` disables the cap. Dismissed as an authorization finding: any client that can reach that 0600 socket can already `create_pane`, `destroy_pane` and inject arbitrary input into the user's shells, so setting a retention policy is not an escalation. Its integer-overflow sibling (L1) WAS fixed. The DoS chain it names is tracked as `techdebt/3-1-overlay-sweep-broadcasts-per-pane.md` (round 1)
- [rules/MEDIUM-1] Docs lagged the code commit-by-commit within the branch (config + IPC landed at 0860010→2cafaac, docs caught up at b1ea74d). Dismissed: not fixable retroactively without rewriting a pushed branch, and the branch is self-consistent at its tip (round 1)
- [rules/LOW] Six commit-body lines run 73-76 chars against the 72-char guideline. Dismissed: would require a force-push of a live PR branch for a cosmetic wrap (round 1)
- [quality/S5, S6, S8, S9; security/L2, L3; qa/Gap4] Judged deferrable this round: cap runs before the new pane spawns; `createOverlay`'s destroy+create is an unordered Batch; the Settings dialog has grown to 12 rows with no scroll clamp; sweep/cap boilerplate duplication; a conn that attaches and never disconnects; remote-mode policy provenance; `goToPane`'s intentional double report (round 1)
