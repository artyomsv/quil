# Code Review State: quil / work-in-progress-indicators

Last reviewed: 2026-06-09
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/M1] mute-mid-turn strands working=true + pins the 100ms tick — `syncPaneMeta` now clears `working` when a pane is muted during workspace-state reconciliation; regression test `TestSyncPaneMeta_MuteClearsWorking` — round 1
- [security/M1] RunHook used unvalidated QUIL_PANE_ID in file paths — `validatePaneID` now called at RunHook entry; also strengthened to reject control chars; test `TestRunHook_RejectsTraversalPaneID` — round 1
- [security/L1] paneID log-injection into hook.log — subsumed by security/M1 (control-char rejection + RunHook logs a fixed "invalid" id on rejection) — round 1
- [rules/M2] stale CLAUDE.md bullet describing EnsureScripts/scripts — rewritten to the native `quild claude-hook` / `RunHook` / `HookCommand(os.Executable())` design — round 1
- [rules/M1] TestBuildTopBorderFocus* missing t.Parallel() — added — round 1
- [rules/L3] TestBuildTopBorderFocus* names missing _Expected segment — renamed to `TestBuildTopBorder_Focus_LabelVisibilityByWidth` / `_Focus_CentersLabel` — round 1
- [rules/L4] RunHook too long — split the per-event switch into `dispatchHookEvent`; RunHook is now read/decode/validate/dispatch — round 1
- [code-quality/L2] tab/pane spinner desync at turn start — `applyWorkTransition` workStart seeds `pane.workFrame = m.workSpinnerFrame` — round 1
- [qa/H1] 8 of 11 RunHook switch branches untested — added `TestRunHook_AllSpoolBranches` (table) covering SessionEnd, Notification, PermissionRequest, PreCompact (both reason forks), PostCompact, SubagentStart/Stop, TaskCreated/TaskCompleted — round 1
- [qa/M2] buildTopBorder small-width edges untested — added `TestBuildTopBorder_SmallWidths` (width 0/1/2 + narrow-CWD-with-spinner) — round 1
- [qa/L3] spinner modulo-wraparound untested — added `TestWorkSpinnerTick_FrameWraparoundMirrors` — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/L3] session.error flashes the tab green like success — intentional per the feature plan (any turn-end clears working and flashes); a distinct "errored" amber flash is a possible future enhancement, not a defect (round 1)
- [qa/L4] BuildSettingsJSON-failure branch in claudeHookSpawnPrep untested — `json.Marshal` of the static `settingsSchema` struct cannot fail; the branch is unreachable without contrived marshaler injection, so a test would only exercise the mock, not real behavior (round 1)
