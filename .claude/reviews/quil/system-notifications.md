# Code Review State: quil / system-notifications

Last reviewed: 2026-08-14
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/CRITICAL] nil Notifier dereferenced in a defer — notify.New returns (nil, nil) off Windows, panicking every Linux/macOS session at exit — round 1
- [security/H-1] named-pipe client dialled without SECURITY_SQOS_PRESENT, allowing a squatting server to impersonate the user — round 1
- [security/M-2] %1 substitution injected argv into quil-activate; parseArgs kept scanning past the URI and honoured an injected --home — round 1
- [security/M-3] remote-controlled pane.ID reached put_Tag unvalidated; one malformed id silently disabled all toasts — round 1
- [security/L-6] HSTRING from get_Tag leaked once per toast — round 1
- [code-quality/I3] a clicked "waiting for input" toast outlived its own click; sweep now withdraws for the pane being watched — round 1
- [code-quality/I4] trailing separator in QUIL_HOME produced --home "D:\quil\", whose \" swallowed the URI — round 1
- [code-quality/S4] cooldown stamped before the send, so a dropped toast silenced its pane for a full window — round 1
- [code-quality/S3] duplicated pane.Muted check swallowed the debug line emitToast promises — round 1
- [code-quality/L1] focusEverReported was write-only; now named in the suppression whose premise can be silently wrong — round 1
- [code-quality/C3] `notify setup` claimed a toast was "displayed" when Show returns S_OK for an unindexed AUMID — round 1
- [code-quality/L2,L3,L4,L5,L6,L7 + rules/1,2] stale comments and docs describing the removed window raise or the old whole-terminal focus gate (quil-activate package doc, .goreleaser.yml, model.go ReportFocus, dev.sh help, docs/roadmap.md, site copy, CLAUDE.md binary count + M17) — round 1
- [qa/gap-1] RunActivation had no test on any platform; covered for malformed URIs, rejected pane ids, and the no-listener case — round 1
- [qa/gap-2] parseArgs was untestable behind //go:build windows; moved to a neutral file, which exposed a real unknown-flag bug — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/L-4] readOne watchdog CancelIoEx race — needs a same-user process driving a ~2 s timing window to kill click routing for the session; the SQOS fix removes the impersonation value of doing so. Recorded as tech debt rather than fixed under review pressure, because the fix is a lock-ordering change to the listener and deserves its own change (round 1)
- [security/L-5 / code-quality/I1] Close() can CancelIoEx a closed handle — same file, same lock-ordering fix, same reasoning (round 1)
- [security/L-7] workQueueDepth rationale stale after the cooldown change (30 s → 5 s) — the 64-slot bound is still generous for any real workspace; the comment overstates its proof, the number is not wrong (round 1)
- [code-quality/I2] queue/lifetime logic in notify_windows.go sits behind //go:build windows — correct in principle and the same argument that paid off for parseArgs, but extracting it needs a comOps seam through the COM layer and is a refactor, not a review fix (round 1)
- [code-quality/I5] setup_windows_test.go assertions are weak (substring "activate" matches inside "quil-activate.exe") — Windows-only test file, never run by CI; worth fixing when someone next runs those natively (round 1)
- [code-quality/C1] com_windows.go CoInitializeEx comment describes an apartment change that RPC_E_CHANGED_MODE prevents — comment accuracy only, no behaviour change; the shell-link work succeeds via the STA host proxy either way (round 1)
- [code-quality/C2] a comment claims the setup command warns about indexing delay — now true after the C3 wording fix (round 1)
- [rules/3] commit 5179385 subject is 74 chars vs the 72 limit — history is published; rewriting it would rewrite the branch (round 1)
- [rules/4] branch is feat/ where CONTRIBUTING.md says feature/ — squash-merge classifies from the PR title, not the branch (round 1)
