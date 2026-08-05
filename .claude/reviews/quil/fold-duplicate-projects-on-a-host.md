# Code Review State: quil / fold-duplicate-projects-on-a-host

Last reviewed: 2026-08-05
Rounds completed: 2 (4 agents + Greptile ×2)

## Round 2 — Greptile re-review

- [greptile/P1-root, RE-RAISED] The root-directory finding was re-raised against the disclosure fix, and re-raising it was RIGHT. Naming the change in the message is the weaker answer: the browse fires on every dialog open and lands within a second, so "the root field holds an artifact rather than a choice" is the ORDINARY state, not an edge case — disclosure made the common case dangerous and the rare case convenient. The fold now carries no root at all; `MergeProjectsPayload` has no `RootDir` field, so the guarantee is structural rather than a branch someone can drop. Relocating is `MsgUpdateProject`, from a dialog seeded with the project's own root. `survivorRoot`, `formMsgPathCap` and the root clause in `message()` are gone with it — resolved
- [greptile/P1-toctou] `destReachable` followed by `sendForDest` is a TOCTOU window. NOT reachable today — both run synchronously on the Update goroutine and `Router.Remove` is only called from it — but that is a property of the current call sites rather than of the router. `sendForDestStrict` checks and sends in one place, so a future off-goroutine remover cannot open the window. The early guard is KEPT beside it for a different reason: a disconnect also changes which BRANCH runs, and only the early guard can give the right message for that — resolved
- [qa/1, superseded] The "`dst.RootDir` asserted nowhere" gap is closed by removal rather than by assertion: there is no RootDir on the fold to get wrong. Both the unit and integration tests now assert the survivor's root is UNCHANGED, and the TUI test asserts the wire form carries no `root_dir` key at all — resolved

**Note on `sendForDestStrict`'s coverage.** Its error arm is unreachable from
`submitNewProject`, which checks reachability first — so mutating the call site
back to `sendForDest` is not caught by any end-to-end test, only by the two
contract tests written for it (`TestSendForDestStrict_ReportsAnUnreachableDest`,
`TestSendMergeProjects_ReportsAnUnreachableDest`). That is the honest shape for a
guard against a race the current architecture cannot produce; recorded so a later
round does not read the direct-call tests as a lapse.

## Resolved (fixed in code; do not re-raise)

### The confirmation gate
- [security/M1 = greptile/P1-root] Comparing PLANS is not the property the design needs — "the user pressed Enter while the sentence was on screen" is. The armed plan and the displayed message are independent fields: typing into the Name row clears `projectFormErr` and leaves `projectFormMerge`, backspace restores neither, so two keystrokes returned the form to a state where `sameAs` was true and the warning line was blank. The Host/User rows and a mid-arm dial reach it too. `foldIsConfirmed` checks the rendered line as well — round 1
- [greptile/P1-root] `message()` omitted the root directory, the one field the recompute could change silently. The opening browse resolves an EMPTY path, so the daemon answers with its own default CWD and `applyBrowseListing` writes it into `cwdBrowseDir` — a directory nobody picked. The re-arm was correct and invisible: identical text, then a real project's root replaced on the third Enter. Named only when it moves; the value is on the row above — round 1
- This invalidated `submitProjectForm`'s standing claim that `cwdBrowseDir` is "always one of three safe things". Second occurrence of the round-1 lesson that a comment asserting unreachability is a claim about every caller — round 1

### Delivery
- [greptile/P1-send = quality/Critical] `Router.Send` DROPS a message for a dest it has no conn for and returns nil (deliberately — `resizeAllPanes` must not break mid-iteration), so the `sendErr != nil` branch could not fire for the case its own comment named. `destReachable` guards the whole `projectFormDest != ""` branch. NOT `destConnected`: that answers "should this be dialled" and reports `[""]` for a non-Router client, which would refuse the one path guaranteed to deliver — round 1
- [qa/reachable] A host disconnecting between the two Enters also changes WHICH branch runs — its projects are dropped client-side, so the recompute finds none and falls through to the generic create. The user confirmed a fold and the client attempted a create, closing as though it worked. Same guard covers it — round 1
- [quality/Critical-followup] A conn that is PRESENT but refuses the write keeps the dialog open too; the next Enter re-arms rather than retrying, because `setFormError` replaced the warning. The message said "Enter retries" and was wrong by exactly one Enter — round 1

### Daemon
- [security/L1] `MergeProjects` appended tab IDs with no membership check. The `id == into` guard covers only the self-merge shape; `restoreProjects` copies `tab_ids` verbatim, so a snapshot listing one tab under two projects reaches the identical state — one TabModel built twice, both fighting over one layout tree — round 1
- [security/L2] `sm.activeTab` was not re-derived when the absorbed project was active and its remembered tab was empty or gone, leaving activeTab outside the survivor's TabIDs. `DestroyProject` re-derives for exactly this reason — round 1
- [quality/5] `recoverEmptyProject` was the one project mutation not calling it. A fold of projects that all hold zero tabs left the survivor empty — a blank screen with no in-band way out, since Ctrl+T files against the active project — round 1
- [quality/4] After a fold absorbing the ACTIVE project, `indexOfProject` returns 0 for an ID that no longer exists, landing the user on `m.projects[0]` — possibly another machine — with `syncActiveDest` re-pointing the router behind it. Not an edge case: `openNewProjectDialog` seeds the dest from `activeDest()`. `sendMergeProjects` points at the survivor — round 1

### Presentation
- [quality/7] `"%d tabs move"` rendered `1 tabs move` on the one sentence the user must read before confirming — round 1
- [quality/6] `projectFormMsgWarn` took busy's 208 while the file header reserves amber 214 for blocked-on-user, which warn IS. The two comments contradicted; warn takes 214 — round 1
- [security/L3] Remote-chosen project IDs written raw to `quil.log`, which `docs/troubleshooting.md` tells users to tail. `%q` on the new lines — round 1

### Public-repo hygiene
- [rules/HIGH] The real test-VM host and IP reintroduced in the **PR body** — a regression of round 1's `security/H-repo`, one layer out where a grep over tracked files cannot see it. Scrubbed to `artyom@gpu01`. The round-1 review record itself quoted the address while reporting it; that quotation is removed too. Edit is mitigation, not erasure — both were public — round 1
- [security/INFO] `/home/artyom` in the daemon fixture normalised to `/home/build` — round 1

### Coverage (all mutation-verified)
- [qa/1] `dst.RootDir` was asserted NOWHERE — dropping the assignment left all 8 daemon unit tests AND the wire+restart integration test green, while a folded project silently kept its old root (what new panes spawn in, what the git subsystem probes) — round 1
- [qa/2] `sm.projectOrder` cleanup untested. `Projects()`/the snapshot skip map-missing IDs defensively, but `DestroyProject`'s `sm.projectOrder[0]` fallback has no such check, so a stale absorbed ID can later become `activeProject` — round 1
- [qa/3] `sameAs`'s per-element absorb comparison untested — every other test differs in LENGTH. The case with no visual signal at all, since the message names only counts — round 1
- [qa/persistence] The integration test called `d.snapshot()` directly, bypassing the trigger: deleting the handler's `requestSnapshot()` left it green. Asserts the queued request (`snapshotCh`, buffered 1) — round 1
- [qa/4] The all-Bootstrap survivor fallback had no fixture — round 1
- [quality/3] **Integration-tagged tests never ran in CI at all** — neither `go test ./...` nor `dev.sh test` passes the tag, so they were not even type-checked, and deleting a `case ipc.Msg…:` arm left everything green. Added `dev.sh test-integration` and a CI step. Repo-wide: 5 files were affected — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/unbounded-host] `hostLabel(p.dest)` is interpolated unbounded. Not attacker-reachable: `Message.Origin` is `json:"-"` and `Router` stamps `ProjectModel.Dest` from its own map key, so a dest is always the user's own ssh destination. Matches the pre-existing unbounded host interpolations beside it; a self-inflicted wrap, not a finding (round 1)
- [qa/5] `sendMergeProjects`'s `m.projectFormMerge = nil` is unpinned and currently redundant, since both dialog-open paths reset it. Kept as defence in depth on the one path that ends the fold; a test asserting a redundant assignment pins the redundancy, not the behaviour (round 1)
- [qa/broadcast] No test drives a real `Update(WorkspaceStateMsg{…})` between the two Enters. `applyWorkspaceState` never touches `projectFormMerge`, and the membership + survivor-name + root comparisons cover what such a broadcast can change. Worth adding when the form grows a field the plan reads indirectly (round 1)
- [quality/2-partial] The two daemon repairs the reviewer found uncovered were covered before the report landed (`TestMergeProjects_RefusesToListOneTabTwice`, `TestMergeProjects_ReDerivesAStrandedActiveTab`), both mutation-verified. Recorded so a later round does not re-raise from the stale snapshot (round 1)
