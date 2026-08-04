# Code Review State: quil / projects-sidebar

Last reviewed: 2026-08-03
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [greptile/P1] MsgDestroyProject skipped cleanupPaneArtifacts on the detached panes — round 1
- [security/H1] Remote project names rendered unsanitized in the context-menu title, the rename form's Name field and the palette's pane labels; a width check is not a sanitiser because lipgloss.Width measures escapes as zero cells — round 1
- [security/M1] projectCWD ran an unbounded os.Stat + EvalSymlinks on the IPC dispatch goroutine; both spawn-CWD resolvers now share one bounded helper — round 1
- [security/L1] gitinfo.runGit did not set cmd.WaitDelay, so a git child holding the stdout pipe outlived the context kill — round 1
- [security/L3] The destroy-project confirm rendered confirmName raw while its disconnect neighbour sanitized it — round 1
- [code-quality/H2] persistDestination/forgetDestination/exit-save wrote the launch-time config struct whole, reverting the remote binary path the install had just recorded — round 1
- [code-quality/H3] SetRedialFactory captured the config by value, so a runtime-provisioned host never reconnected after its first link drop — round 1
- [code-quality/M4] A workspace broadcast buffered before Router.Remove could resurrect a disconnected host's projects — round 1
- [code-quality/M5] DestroyTab promoted the successor from the global tabOrder, moving the active tab into a different project — round 1
- [code-quality/M6] jumpToPane and jumpToNextBlocked changed the active tab without exitNotesModeInPlace — round 1
- [code-quality/S7] gitCache maps were never pruned; sweep now drops CWDs no live pane is in — round 1
- [code-quality/S8] The five project IPC handlers ignored DecodePayload errors; MsgCreateProject built a nameless project from a malformed frame — round 1
- [code-quality/S9] elideMiddle sliced []rune with cell-derived indices, overlapping the two halves on wide glyphs — round 1
- [code-quality/S10] disconnectDest left updateInfos/installedDests behind — round 1
- [code-quality/S11] The project picker aliased m.projects, never refiltered while open, and resolved Enter through indexOfProject's answers-0 contract — round 1
- [qa/coverage] ReorderProject had no coverage at any layer; Model.lastDaemon had none — round 1
- [rules/H1] No CHANGELOG entry, which the CI changelog gate fails the PR for — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/H2] Branch named `feat/projects-sidebar` where CONTRIBUTING.md documents `feature/` — the PR is open with 85 commits and the repo squash-merges, so the PR title is what becomes the commit. Renaming closes and recreates the PR to fix a prefix that reaches no artifact. (round 1)
- [rules/M3, rules/M4] Two commit subjects at 75 chars against a 72 limit — already pushed, and the squash-merge means the PR title is the message that ships. Rewriting 85 commits' history to fix two subject lines that will not appear in the release is not worth the force-push. (round 1)
- [qa/coverage-minor] dialDest/adoptDest/installDest have no dedicated tests beyond the end-to-end remote-flow test; gitFingerprint/gitWatcher and the sidebar pane-row click-through are likewise only covered transitively. Real gaps, but each needs a fixture larger than the behaviour it would pin, and the paths are exercised end to end. (round 1)
