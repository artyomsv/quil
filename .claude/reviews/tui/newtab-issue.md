# Code Review State: tui / newtab-issue

Last reviewed: 2026-08-25
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/MEDIUM-1] handleNewTab's doc comment still credited "the escape path" with keeping the old bare-create_tab behaviour, which this change deleted — reworded (internal/tui/model.go) — round 1
- [code-quality/MEDIUM-2] The Ctrl+T Enter Enter promise (asserted in five documents) was undefended by any test and one display-name rename from being false, since the terminal category holds both `terminal` and `terminal-wide` and the plain one wins only on an alphabetical prefix — pinned by TestNewTab_EnterEnterCreatesAPlainTerminalTab, verified by mutation (renaming terminal-wide to sort first fails it with type = "terminal-wide"); the scoped rules file's singular "its plugin sorts first" wording corrected — round 1
- [code-quality/LOW-3] The two re-pointed routing tests set selectedPlugin directly and called handleCreatePaneSplit, bypassing their call site — both now drive two real Enter presses through handleCreatePaneKey via pressEnterTwice, which keeps the routing coverage and exercises the shortcut in the same run — round 1
- [code-quality/good-practice] The step-0 footer has rendered "Esc cancel" the whole time — the screen was already promising what the handler refused. Recorded in the Esc-branch comment, the scoped rule and the changelog fragment, since it is the strongest argument for the change — round 1

## Deferred to tech debt (filed, not fixed here)
- [code-quality/LOW-4] TestNewTab_OnlyOfflineProjectsStillReachesTheLocalDaemon survives the unconditional-stamping mutation, so only one of the two startup-window tests defends the unstamped-send branch — pre-existing on master, filed as techdebt/4-1-offline-projects-test-does-not-defend-the-unstamped-send.md — round 1
- [code-quality/LOW-6] The create-pane picker renders the title "New Pane" even in new-tab mode — pre-existing, filed as techdebt/4-1-create-pane-picker-titled-new-pane-in-new-tab-mode.md — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/LOW-5] sendCreateTab's nil case now has zero client-side callers — informational only. The parameter stays: nil is the wire's "the daemon picks", the daemon-side producers rely on it, and it keeps its own coverage in internal/daemon/create_tab_firstpane_test.go. Narrowing the client signature would encode a client-side fact into a shared wire helper (round 1)
- [rules/informational] `changed` vs `fixed` fragment type — kept `changed`. The prior behaviour matched its own documentation and was a deliberate UX decision being reversed, not a divergence from spec; the release section reads correctly either way (round 1)
- [security/pre-existing] The 3C.5 sweep re-surfaces a LAN address in four tracked .claude/reviews/ artifacts — prior review notes reporting the finding, recoverable from public git history regardless. Needs a history-rewrite decision, not a forward edit in this PR (round 1)
