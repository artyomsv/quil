# Code Review State: quil / project-form-message-and-duplicates

Last reviewed: 2026-08-05
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/M1] The refusal message interpolated a remote-chosen project name with no length bound, on the one dialog row without truncation — lipgloss wraps, so a long name became thousands of lines per frame. Capped at `formMsgNameCap` — round 1
- [security/L1] The message line's render site trusted its input; sanitising happened only at the eight set sites. Render sanitises unconditionally now, pinned by a test that assigns the field directly — round 1
- [security/L2 = quality/B] The adopt path routed a create's empty root dir into a rename, and `UpdateProject` has no unchanged-value guard, so the adopted project's `RootDir` was erased. Substitutes the project's own root — round 1
- [security/H-repo] A real host and username (`artyom@192.168.6.12`) in `internal/tui/projectdialog_test.go` and `internal/tui/sidebar.go` of a PUBLIC repo. Replaced with the `gpu01` placeholder, matching `eefef22`; repo-wide grep clean — round 1
- [quality/A] `hostProjectState` returning (nil, nil) fell through to a create, so submitting before a host's first `workspace_state` landed created a project beside the bootstrap one the client could not see — reachable because `destDialedMsg` batches the attach with the browse. Waits on `m.attached[dest]` instead — round 1
- [quality/C] `openNewProjectDialog` seeded `projectFormDest` from the active project but left the ssh fields blank, so the form read "this machine" while aimed at a remote host — harmless for a create, destructive once naming can adopt. Seeds Remote/user/host from the dest — round 1
- [quality/D] Two clients adopting one host each renamed the other's freshly named project. `UpdateProjectPayload.AdoptBootstrap` makes the update a compare-and-swap the daemon refuses when the project is no longer a bootstrap — round 1
- [quality/E] `MsgUpdateProject` was the only project mutation without `requestSnapshot`, so a rename — which now also clears a persisted flag — could be lost to a kill inside the 30 s ticker window — round 1
- [quality/F] Remote ssh diagnostic text on the message line was unbounded (2000 bytes upstream ≈ 40 wrapped rows). Capped at `formMsgDetailCap` — round 1
- [quality/G] All refusal tests called `submitNewProject` directly; one now drives Enter through `handleProjectDialogKey` and asserts the RETURNED Model carries the message — round 1
- [quality/H] `uniqueProjectName` trimmed only on the collision path, so a name kept its padding alone and lost it when disambiguated. Trims on both — round 1
- [quality/I] `UpdateProject`'s bool was discarded; its two failure modes (unknown ID, adopt losing the race) left no trace. Logged — round 1
- [qa/gap] Nothing exercised `workspaceStateFromSnapshot` writing `"bootstrap"` — dropping that line left the whole normal suite passing (verified by mutation) while every project silently became non-adoptable after a restart. Integration test drives `snapshot()` → `persist.Load()` → `restoreWorkspace()` — round 1
- [qa/minor] The adopt path's dialog close was asserted nowhere — round 1
- [greptile/P1-rename] A RENAME could recreate the indistinguishable pair a create is refused for — `submitRenameProject` had no duplicate check and `UpdateProject` assigned directly. My earlier scoping ("a rename is deliberate, so the user knows which is which") was wrong: intent does not survive the rename, and afterwards the two rows read identically. Guarded at both layers, excluding the project's own name so a root-only change still works — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/L3] `projectByID` resolves an ID across every daemon, so a cross-daemon collision would route a rename to the wrong project. Pre-existing (the sidebar rename path has it too), needs a 32-bit UUID-prefix guess, and the cheap collisions are already closed by `isSyntheticProject` → `destSupportsProjects` (round 1)
- [quality/J] `hostLabel("")` → "this machine" is currently dead, since its only caller sits inside the `projectFormDest != ""` branch. Kept deliberately: it is correct for a local caller, and removing it would make a future one silently name nothing (round 1)
- [rules/1] Commit bodies run 73–80 chars against the 72-char guideline. The repo's whole history reads this way, so this PR is consistent with existing practice rather than deviating; a change here is repo-wide, not PR-scoped (round 1)
- [rules/2] Commit `85650de` bundles the message-severity change with the duplicate-name guard. The repo squash-merges, so the final history is one commit either way (round 1)
