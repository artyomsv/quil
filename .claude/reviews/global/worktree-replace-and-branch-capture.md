# Code Review State: global / worktree-replace-and-branch-capture

Last reviewed: 2026-08-07
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/M1 + code-quality/1 + qa/4] worktreeReplaced leaked permanently when any broadcast landed mid-flight — both settling paths gate on worktreeCreates, which rebuildTabs cleared. Dispose moved to the leaf-fill; rebuildTabs skips the held pane — round 1
- [code-quality/6] PrunePlaceholders cannot repair a ROOT placeholder, so the root-insert fallback panicked on tab.Leaves()[0]. Guarded — round 1
- [code-quality/2 + security/L1 + qa/5] renderPendingPane overflowed its rect: the 18-cell prefix was never budgeted and lipgloss pads rather than truncates (10x4 rendered 10x5; 1x4 rendered 1x18). Whole line budgeted + MaxWidth/MaxHeight — round 1
- [code-quality/4 + qa/6] an ordinary split's placeholder rendered "Creating worktree…". Empty branch now renders blank — round 1
- [code-quality/3 + qa/3] a second create on the same tab overwrote worktreeCreates, worktreeReplaced and pendingSplit. Refused with a reason — round 1
- [code-quality/5] the post-add re-check covered the tab but not the replace target, orphaning a worktree. Re-checked, and every abandonment after a successful add now removes the worktree + branch (gitworktree.Remove) — round 1
- [security/M2] the failure-restore premise was false when the spawn failed after a successful swap. CreatePaneRespPayload.Swapped added; settleReplacedPane is the single restore/dispose choke point — round 1
- [security/L2 + code-quality/10] p.TabID was validated but ReplacePane resolves pane ids globally, so a mismatched pair destroyed another tab's pane. Validated before and after the add — round 1
- [qa/1] TestReplace_WithoutAWorktreeStillDisposesImmediately was vacuous — deleting old.Dispose() kept the suite green. Now asserts the dispose itself (p.vt nilled) — round 1
- [code-quality/8] the timeout mutated the leaf before its own nil-root guard, leaving a stale leaves cache. Folded into settleReplacedPane — round 1
- [code-quality/9] fmt.Errorf put the verb mid-sentence; reworded to context-first — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/7] the timeout flash says "worktree not created" even after a success response whose pane never landed. Not reachable as stated: success now settles on the broadcast that fills the leaf, which also clears worktreeCreates, so the timeout returns early and flashes nothing (round 1)
