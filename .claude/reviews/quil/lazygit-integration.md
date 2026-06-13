# Code Review State: quil / lazygit-integration

Last reviewed: 2026-06-12
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/H-1] gitdiscover.canonical rejects UNC/device paths (`\\`, `//`, device) before any os.Stat/ReadDir — blocks OSC7-driven Windows SMB/NTLM probe (8a3f452) — round 1
- [security/M-1] daemon handleUpdatePane skips persisting a UNC-prefixed CWD to workspace.json (defense-in-depth; discovery-layer fix is the primary mitigation) (8a3f452) — round 1
- [security/M-2] Overlay-flag trust model documented on CreatePanePayload (any IPC client may set it; MCP bridge deliberately does not expose it) (173bfab) — round 1
- [code-quality/M1] overlay PTY resized on initial create — reconcileOverlayPane signals newly-visible tabs, applyWorkspaceState returns resize cmds, caller batches overlayResizeCmd (was booting 80×24 until manual resize) (09971d8) — round 1
- [code-quality/L2] picker + setup candidate lists left-truncate long repo paths (leftTruncPath) to keep rows aligned (f09be2f) — round 1
- [code-quality/L3] crisp flash expiry via flashExpireMsg + flashCmd (tea.Tick); handleToggleLazygit flash branches return it; sizePollTick kept as backstop (f09be2f) — round 1
- [rules/M1] onPaneExit logs the DestroyPane error instead of //nolint:errcheck (no golangci-lint in project) (173bfab) — round 1
- [rules/M2-M4] t.Parallel() added to the 21 stateless overlay/reconcile/flash tests (d1966e8) — round 1
- [qa/1] TestTabModel_OverlayVisible_ViewReturnsOverlayContent — View() returns overlay content when visible (d1966e8) — round 1
- [qa/2] TestHandleOverlayKey_Quit_ReturnsQuit + _Redraw_InvalidatesCaches — closes the two untested key-router branches (d1966e8) — round 1
- [qa/3] TestCreateOverlay_DefenseInDepth_UnavailableFlashes — createOverlay re-check exercised via direct call (d1966e8) — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/L1] gitdiscover Candidates sub-repo dedup is defensive-only — reviewer confirmed correct as written (SubRepos is one-level, ReadDir names unique); no change warranted — round 1
- [rules/M5] 5 commit subjects exceed 72 chars — immutable history; squash-merge subject should stay ≤72; noted for future commits — round 1
- [rules/M6] handleToggleLazygit is 62 lines / 7 steps — kept intentionally: the numbered step comments are spec-traceability; extracting a dispatcher would sever that without reducing cognitive load (both rules-compliance and the TUI implementer concurred) — round 1
