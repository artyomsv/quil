# Worktree Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The close dialog shows which files a worktree delete destroys and can delete the branch locally and on the remote. Quil records the worktrees that appear while agents run, and offers a view to clean them up.

**Architecture:** Three pull requests, each one shippable on its own.
- **PR A (Tasks 1–5):** close dialog. `gitworktree.Status` returns a file list. A new `gitworktree.ReadBranchFacts` gives the branch, unmerged count and remote. The destroy payloads gain bools plus check-only branch names, and the daemon re-derives every target. The remote-delete permission is a runtime daemon policy that the TUI pushes, in the same way as `overlay_policy`.
- **PR B (Tasks 6–9):** agent tracking. The three agent hooks spool a new `WorktreeProbe` line naming the directory a worktree- or branch-related command ran in. The daemon's spool watcher takes these lines out BEFORE the coalescer. The daemon compares `git worktree list` AND the local branches against a saved baseline for that repository, and writes new or missing worktrees and branches to `$QUIL_HOME/agent-worktrees.json`.
- **PR C (Tasks 10–11):** cleanup view. A dialog lists the record and acts on one entry by daemon-minted id. The daemon re-checks that entry against git before it reuses the PR A removal code.

**Tech Stack:** Go 1.25, Bubble Tea v2 / Lipgloss v2, git CLI, the existing hook producers (`internal/claudehook`, `internal/codexhook`, the OpenCode JS plugin).

**Spec:** This conversation's decisions (memory `worktree-close-cleanup-decisions`). The hook payload facts were measured on 2026-11-07 (memory `claude-hook-payload-probe`, and the table below). A plan review (Fable, 2026-11-07) is folded in.

## Global Constraints

- **No local builds or tests.** CI (`ci.yml`) is the verifier. "Push; CI green" means: push, then `gh pr checks <n> --watch`, then `gh run view <id> --log-failed` on a red check.
- **The wire never carries a deletion TARGET**, only bools, a daemon-minted record id, or check-only names. A check-only name is compared against a value the daemon derived itself, and is never acted on. Precedent: `DestroyPanePayload.RemoveWorktree` (`internal/ipc/protocol.go:544-559`).
- **Remote branch delete is OFF by default** and has two gates. The first gate is the F1 row "Allow remote branch delete" (`worktree.remote_branch_delete`, default `false`). The TUI pushes it to every attached daemon as `MsgWorktreePolicy`, which is admin class, so only a full-rights client can turn it on. The daemon refuses any remote delete while its runtime flag is off. The second gate is a per-close checkbox, which needs a typed `y`.
- Remote push: `git -c credential.interactive=false push <remote> :refs/heads/<b>`. The environment holds `GIT_TERMINAL_PROMPT=0` and `GCM_INTERACTIVE=never`, and `GIT_SSH_COMMAND="ssh -o BatchMode=yes"` only when the user has set neither `GIT_SSH_COMMAND` nor `GIT_SSH`. The context has a 30 s deadline. The FS permit is NOT held during the push. Every outcome becomes a sidebar event carrying the destroyed pane's id and name.
- A local branch delete is offered only after the dialog LOADED the branch facts without error. It is never offered for the default branch. It never runs for a branch that is not in `ConfirmedBranches`. `git branch -D` itself refuses a branch that is checked out in any worktree.
- Every new IPC type goes into `internal/clientauth/rights.go` AND into `sweep_test.go`'s `wantClassTable`.
- Every new file in `QUIL_HOME` gets its stem in `quilHomeStems` (`internal/ipc/acl.go:122-127`) and in `acl_test.go`. Otherwise Windows `ProtectDir` sees a foreign entry and the TCP listener stays off.
- Do NOT register a Claude `WorktreeCreate` hook: it REPLACES Claude's own worktree creation.
- `internal/codexhook` shares no code with `internal/claudehook`. Shared pure helpers go in `internal/hookevents`.
- Every remote string (file names, branch names, paths, commands) passes `sanitizeRemoteText` at render and is bounded in cells.
- Each PR adds one `changelog.d/<type>-<slug>.md` with a `headline:` line, and updates the scoped rule files it makes wrong.
- Commits: imperative mood, ≤ 72 characters, no AI attribution, `Part of #<issue>` when an issue exists. Never commit this plan file.

## Measured hook facts (2026-11-07)

| Agent | Event | Fields used |
|---|---|---|
| Claude 2.1.319 | `PostToolUse` tool `Bash` | `tool_input.command`, `cwd` |
| Claude | `PostToolUse` tool `EnterWorktree` | `tool_response.worktreePath`, `tool_response.worktreeBranch` |
| Claude | `PostToolUse` tool `ExitWorktree` | `tool_input.action` (`keep`/`remove`), `tool_response.worktreePath`, `cwd` = original directory |
| Claude | `SubagentStart` with `isolation:"worktree"` | `cwd` = `<repo>/.claude/worktrees/agent-<id>`; Claude removes it when it is clean |
| Codex 0.166.0 | `PostToolUse` tool `Bash` (no worktree tool) | `tool_input.command`, `cwd` = the command's own workdir |
| OpenCode 1.18.32 | plugin `tool.execute.after`, tool `bash` | `input.args.command`, `input.args.workdir` (RELATIVE or absent), `output.metadata.exit`; base = plugin init `input.directory` |

## Review Focus

1. **A file name with spaces, quotes, a newline or non-ASCII** in `git status`. It renders as one sanitised row. `ParseStatusZ` uses `-z`, with tests for a space and a rename.
2. **The branch moves after the dialog opened.** The daemon must not delete a branch the dialog did not show. `ConfirmedBranches` is check-only, with a test where the mismatched branch survives.
3. **A credential prompt or an unreachable remote.** It must not hang a goroutine past 30 s or open a hidden prompt. A stub runner blocking past the deadline yields a failure event; a test asserts the push argv and env.
4. **Two probes in one 200 ms spool tick** (`git worktree add` then `EnterWorktree`, or parallel Bash calls). Both directories get compared. The probe bypasses the coalescer, with a test that submits two lines in one tick.
5. **A worktree the user (not the agent) made while an agent pane ran.** It is recorded, but its label says "appeared while <pane> ran", never "made by". It cannot be deleted without the file list on screen.

---

## PR A — close dialog

### Task 1: File list from `git status`

**Files:**
- Modify: `internal/gitworktree/gitworktree.go:436-486` (`Status`)
- Modify: `internal/daemon/worktree_status.go` (the seam `worktreeStatusFn`, the `Changes` read)
- Modify: `internal/daemon/worktree_status_test.go:18` (stub signature) and every other `worktreeStatusFn` stub (grep the package)
- Test: `internal/gitworktree/status_test.go`

**Interfaces:**
- Produces:
  - `type FileChange struct { Code, Path string }` — `Code` is git's two-character XY.
  - `type StatusReport struct { Changes int; Files []FileChange; Truncated bool }`
  - `const MaxStatusFiles = 200`
  - `func Status(ctx context.Context, path string) (StatusReport, error)`
  - `func ParseStatusZ(out string) (files []FileChange, total int)` — pure.

- [ ] **Step 1: Failing test** `TestParseStatusZ_SpacesRenamesIgnored`. Input `" M a b.txt\x00?? new dir/\x00R  new.go\x00old.go\x00!! .env\x00"` → 4 files `{" M","a b.txt"}`, `{"??","new dir/"}`, `{"R ","new.go"}`, `{"!!",".env"}`, `total == 4`.
- [ ] **Step 2: Failing test** `TestParseStatusZ_CapsAtMaxStatusFiles`. 250 entries → `len(files)==200`, `total==250`.
- [ ] **Step 3: Implement.** Run `--no-optional-locks status --porcelain -z --ignored` (traditional `--ignored` mode). A truncated read stays an error. Keep the existing comments' reasoning.
- [ ] **Step 4: Fix every daemon stub and the call site** to read `.Changes` (Task 2 sends the rest).
- [ ] **Step 5: Push; CI green.** Commit `feat(gitworktree): report the files a forced removal destroys`.

### Task 2: Branch facts and the extended status answer

**Files:**
- Create: `internal/gitworktree/branchfacts.go`, `branchfacts_test.go` (stubbed through the `runGit` seam, plus one real-git test)
- Modify: `internal/ipc/protocol.go:2105-2120` (`WorktreeStatus`), `internal/daemon/worktree_status.go`
- Test: `internal/daemon/worktree_status_test.go`

**Interfaces:**
- Produces:
  - `type BranchFacts struct { Branch string; Detached, IsDefault bool; Unmerged int; DefaultRef string; Remote, RemoteReason string; Unpushed int }`
  - `func ReadBranchFacts(ctx context.Context, repo, worktreePath, defaultRef string) (BranchFacts, error)`.
    - Branch: from `List(repo)`'s entry for `worktreePath`.
    - `Unmerged`: `rev-list --count <defaultRef>..refs/heads/<b>`. `defaultRef` is the FULLY QUALIFIED ref `defaultBranch` returns. Its short form is used only for `IsDefault` and display. Do not change `defaultBranch`'s return.
    - Remote: first `rev-parse --abbrev-ref --symbolic-full-name refs/heads/<b>@{upstream}`, split into remote and branch through `git remote` names (longest prefix). When there is no upstream, check each `git remote` name `r` with `rev-parse --verify --quiet refs/remotes/<r>/<b>`. Exactly one → `Remote = r`. None → `RemoteReason = "no remote branch"`. Several → `"branch exists on several remotes"`. No network.
    - `Unpushed`: `rev-list --count refs/remotes/<r>/<b>..refs/heads/<b>`.
  - `ipc.WorktreeStatus` gains `Files []WorktreeFile` (`json:"files,omitempty"`), `FilesTruncated bool`, `Branch string`, `BranchDeletable bool`, `BranchReason string`, `Unmerged int`, `DefaultBranch string` (short, display only), `Remote string`, `RemoteReason string`, `Unpushed int`. Also `type WorktreeFile struct { Code, Path string }`.
  - `BranchDeletable` is false with a reason for: detached, default branch, or a facts error. `Changes` and `Files` still arrive when the facts fail.
- Cost bound: `defaultBranch` is resolved ONCE per repo per request (map in the request). Facts are computed only for the first `confirmWorktreeMaxRows` (6) paths. The remaining rows get `BranchReason = "not checked (too many worktrees)"`. Everything shares the request's ONE 10 s deadline and ONE permit.

- [ ] **Step 1: Failing tests:**
  - `TestReadBranchFacts_DefaultBranchIsNotDeletable`
  - `TestReadBranchFacts_UpstreamWinsOverRefScan`
  - `TestReadBranchFacts_SlashInBranchResolvesRemote` (`origin` + `feat/x`)
  - `TestReadBranchFacts_TwoRemotesIsAmbiguous`
  - `TestReadBranchFacts_RealGit_UnmergedAndUnpushed` (real git: `master`; `feat` 2 commits ahead; `refs/remotes/origin/feat` 1 commit ahead → `Unmerged==2`, `Unpushed==1`, `Remote=="origin"`; and a LOCAL branch literally named `origin/master` must not change `Unmerged`)
- [ ] **Step 2: Failing tests:**
  - `TestWorktreeStatusResponse_CarriesFilesAndBranchFacts`
  - `TestWorktreeStatusResponse_DefaultBranchResolvedOncePerRepo` (count the stub calls)
  - `TestWorktreeStatusResponse_FactsOnlyForFirstSixPaths`
- [ ] **Step 3: Implement** (the repo = `worktreeListFn(ctx, path)[0].Path`, as `removeOneWorktree` does it).
- [ ] **Step 4: Push; CI green.** Commit `feat(daemon): send file list and branch facts with worktree status`.

### Task 3: Remote-delete setting and its policy push

**Files:**
- Modify: `internal/config/config.go` — add `Worktree WorktreeConfig \`toml:"worktree"\`` and `type WorktreeConfig struct { RemoteBranchDelete bool \`toml:"remote_branch_delete"\` }`. `Default()` leaves it `false`.
- Modify: `internal/config/settings.go` — `boolRow("worktree.remote_branch_delete", "general", "Allow remote branch delete", AppliesNow, …)`
- Modify: `internal/tui/dialog.go:234-430` (`settingsFields`) — add the row, which calls `applySetting("worktree.remote_branch_delete", …)`
- Modify: `internal/ipc` — `MsgWorktreePolicy = "worktree_policy"`, `WorktreePolicyPayload{ RemoteBranchDelete bool }`
- Modify: `internal/tui` — `worktreePolicyCmd()`, sent beside `overlayPolicyCmd()` at both of its call sites (the settings key handler near `dialog.go:963`, and attach near `model.go:9022`). The policy goes to every attached destination.
- Modify: `internal/daemon/daemon.go` — handle it like `MsgOverlayPolicy` (`daemon.go:1666-1675`), into `d.remoteBranchDelete atomic.Bool`, seeded from `d.cfg.Worktree.RemoteBranchDelete` at start
- Modify: `internal/clientauth/rights.go` (admin class) + `sweep_test.go`
- Test: `internal/config/settings_test.go`, `internal/tui/settings_policy_test.go`, `internal/daemon/worktree_policy_test.go`

- [ ] **Step 1: Failing tests:**
  - `TestSettings_RemoteBranchDeleteDefaultsOff` (default false; `LookupSetting` row with `AppliesNow`; `Set("true")` round-trips)
  - `TestSettingsDialog_RemoteBranchDeleteRowSendsPolicy` (through `Update`: toggle → a `worktree_policy` frame with `true`)
  - `TestAttach_SendsWorktreePolicy`
  - `TestHandleWorktreePolicy_SetsRuntimeFlag`
  - the class-table entry (admin)
- [ ] **Step 2: Implement. Push; CI green.** Commit `feat(config): add a remote branch delete switch, off by default`.

### Task 4: Daemon branch deletes on close

**Files:**
- Modify: `internal/ipc/protocol.go` — `DestroyPanePayload`, `DestroyTabPayload`: `DeleteBranch bool \`json:"delete_branch,omitempty"\``, `DeleteRemoteBranch bool \`json:"delete_remote_branch,omitempty"\``, `ConfirmedBranches []string \`json:"confirmed_branches,omitempty"\``. `DestroyPaneReqPayload` (MCP and browser) is unchanged.
- Create: `internal/gitworktree/branchdelete.go`, `branchdelete_test.go`:
  - `DeleteLocalBranch(ctx, repo, branch string) error` → `branch -D -- <b>`
  - `DeleteRemoteBranch(ctx, repo, remote, branch string) error` → argv and env exactly as in Global Constraints. The env builder is a pure `remotePushEnv(base []string) []string`.
- Modify: `internal/daemon/worktree_remove.go` — `removeOwnedWorktrees(paths []string, opt branchDeleteOpts)`; the seam type `removeOwnedWorktreesFn` changes to match; extract the per-worktree body as `removeWorktreeWithBranch(path string, opt branchDeleteOpts, who paneRef) (notice string, err error)` (Task 10 reuses it).
- Modify: `internal/daemon/daemon.go` — ALL THREE callers:
  - `handleDestroyPane` → `teardownAfterDestroy` (`:3591`)
  - `handleDestroyPaneReq` → `teardownAfterDestroy` (`:8361`, passes zero opts)
  - `handleDestroyTab`'s own goroutine (`:2823-2825`), which calls `d.removeOwnedWorktrees` directly. Route it through `removeOwnedWorktreesFn` too.
- Modify: `internal/clientauth/rights.go` — `carriers()` gains a third flag `remoteDelete` with a case for `destroy_pane` and `destroy_tab`. `checkStandardPayload` refuses it from a non-full conn with the reason `"remote branch delete needs a full token"`. Update `sweep_test.go`.
- Test: `internal/daemon/worktree_branchdelete_test.go`

**Interfaces:**
- `type branchDeleteOpts struct { Local, Remote bool; Confirmed []string }`
- `type paneRef struct { ID, Name string }` — captured BEFORE the destroy (the `notifyPaneDestroyed` shape, `daemon.go:3561`)
- Seams: `deleteLocalBranchFn`, `deleteRemoteBranchFn`, `branchFactsFn`.
- Order per worktree:
  1. Read `BranchFacts` before the removal.
  2. Remove the worktree (unchanged code).
  3. If `Local`, and the branch is deletable, and it is in `Confirmed` → `DeleteLocalBranch`.
  4. Release the FS permit.
  5. If `Remote`, and `d.remoteBranchDelete.Load()`, and `Remote != ""`, and the branch is in `Confirmed` → `DeleteRemoteBranch`.
- Events (class System, carrying `paneRef`): `worktree_branch_deleted` (local/remote, named), `worktree_branch_kept` (the reason: setting off, no remote, not confirmed, git error, timeout). Every outcome is logged.

- [ ] **Step 1: Failing tests** (stubbed):
  - `TestRemoveOwnedWorktrees_DeletesLocalBranchOnlyWhenConfirmed`
  - `TestRemoveOwnedWorktrees_NeverDeletesDefaultBranch`
  - `TestRemoveOwnedWorktrees_RemoteRefusedWhenPolicyOff` (no push; one `worktree_branch_kept` carrying the pane id and name)
  - `TestRemoveOwnedWorktrees_RemoteTimesOut` (the stub blocks until ctx done → returns within 30 s + slack; event reason "timeout")
  - `TestRemoveOwnedWorktrees_NoBranchCallWhenWorktreeKept` (a live pane is in it)
  - `TestHandleDestroyTab_PassesBranchOpts`
  - `TestHandleDestroyPaneReq_NeverDeletesBranches`
  - `TestRemotePushEnv_KeepsUserSSHCommand`
  - `TestDeleteRemoteBranch_ArgvUsesFullRefspec`
  - the class-table / carriers entry
- [ ] **Step 2: Real-git test** `TestDeleteLocalBranch_RealGit_AfterWorktreeRemoved`.
- [ ] **Step 3: Implement. Push; CI green** (`test`, `test-race`, `integration`). Commit `feat(daemon): delete a closed worktree's branch locally and on its remote`.

### Task 5: Close dialog UI

**Files:**
- Modify: `internal/tui/worktree_close.go` — `confirmWorktree` gains the Task 2 fields; `applyWorktreeStatus` copies them
- Create: `internal/tui/worktree_rows.go` — the row and file-list render helpers (Task 11 reuses them)
- Modify: `internal/tui/model.go` — `confirmDeleteBranch`, `confirmDeleteRemote bool`, `confirmRow int` (0 worktree, 1 local, 2 remote), `confirmFileScroll int`, `confirmFileFocus int`. `resetConfirmWorktrees` clears ALL of them.
- Modify: `internal/tui/dialog.go:1083-1200` (keys), `:2047-2151` (render)
- Test: `internal/tui/worktree_close_test.go`, always driven through `Update`

**Behaviour:**
- Rows: `[x] Also delete its worktree` / `[ ] Delete local branch <b>` / `[ ] Delete remote branch <remote>/<b>`.
  - The branch rows are inert until the worktree row is armed.
  - The local row is greyed with `BranchReason` until loaded.
  - The remote row is greyed with `off in F1 → Settings` when `m.cfg.Worktree.RemoteBranchDelete` is false, else with `RemoteReason`.
- File list: rows `<code> <path>`, cut with `elideMiddle`, sanitised, budgeted against `dialogInnerWidth`. `Truncated` adds `…only the first 200 are listed`.
- Warnings when armed:
  - local, `Unmerged > 0` → `⚠ <n> commits not on <default> will be lost`
  - remote → `⚠ deletes <remote>/<b> for everyone`
  - remote, and `PaneModel.PRNumber > 0` and `PRStatus` is not merged or closed → `⚠ PR #<n> is open — deleting the branch closes it`
- Height: a named budget `confirmChromeRows` (title, question, notice, blank lines, the three checkbox rows, warnings, the "kept/goes" line, footer, border and padding). The file list gets `max(1, min(10, m.height - confirmChromeRows - worktreeRows))`. When the tab holds several worktrees, every worktree except `confirmFileFocus` collapses to ONE row (label + count). The fit test asserts the PROPERTY (the box height is at most the terminal height at 24 and at 30 rows), not a number.
- Keys:
  - `↑`/`↓` move `confirmRow` (it starts at 0 on every open).
  - `space`/`w` toggle the row under the cursor, and are a no-op on a greyed row.
  - `PgUp`/`PgDn` scroll files by the visible count; `←`/`→` change `confirmFileFocus`.
  - `Enter` accepts EXCEPT when the remote row is armed. Then only `y` accepts. That gate runs BEFORE `resetConfirmWorktrees()` (today at `dialog.go:1160`), so the arm survives a refused Enter.
  - Every footer stays inside the existing `truncateToWidth` (`dialog.go:2038`).
- Accept sends `DeleteBranch`, `DeleteRemoteBranch`, and `ConfirmedBranches` = the branches of the worktrees on screen whose branch row is deletable.

- [ ] **Step 1: Failing tests:**
  - `TestCloseConfirm_ListsFilesAndScrolls` (25 files; `PgDn` shows the next page)
  - `TestCloseConfirm_FileNameIsSanitizedAndBounded` (ESC sequence + bidi override)
  - `TestCloseConfirm_CursorStartsOnWorktreeRow`
  - `TestCloseConfirm_ToggleOnGreyedRowIsNoOp`
  - `TestCloseConfirm_BranchRowsInertUntilWorktreeArmed`
  - `TestCloseConfirm_RemoteRowGreyedWhenSettingOff`
  - `TestCloseConfirm_EnterDoesNotAcceptWithRemoteArmed` (Enter → still open and still armed; `y` → payload `DeleteRemoteBranch:true`, `ConfirmedBranches:["feat"]`)
  - `TestCloseConfirm_ResetOnEveryExit`
  - `TestCloseConfirm_FitsTerminal` (3 worktrees × 30 files, all armed, heights 24 and 30)
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Docs.** Update `docs/keybindings.md` (`Ctrl+W`), `docs/features.md`, and `.claude/rules/projects.md` "Removing worktrees on close": the file list, the branch rows, the policy push, `ConfirmedBranches` as check-only, and that `branch -D` closes the checked-out race. Add `changelog.d/feat-close-dialog-branch-delete.md`.
- [ ] **Step 4: Push; open PR A** `feat(tui): show lost files and offer branch delete on worktree close`. CI green, then the `review: requested` label.

---

## PR B — agent worktree tracking

### Task 6: Shared probe helpers in `hookevents`

**Files:** Create `internal/hookevents/worktreeprobe.go`, `worktreeprobe_test.go`

**Interfaces:**
- `const EventWorktreeProbe = "WorktreeProbe"`
- `const DataProbeTrigger = "trigger"`; values `ProbeShell = "shell"`, `ProbeEnter = "enter"`, `ProbeExit = "exit"`, `ProbeSubagent = "subagent"`
- `const DataProbeCommand = "command"` (display only, ≤ `MaxDataValueBytes`, never part of a key)
- `const MaxProbeDirBytes = 1024`
- `const WorktreeCommandPattern = `(?i)worktree|git\s+branch\s|checkout\s+-[bB]|switch\s+-[cC]|push\b.*(--delete|\s:)`` (exported so the JS test can compare)
- `func IsWorktreeCommand(cmd string) bool`
- `ClassifyWorkEvent(EventWorktreeProbe)` stays at the default (no work kind). Do NOT add it to `IsWorkStateOnly`.

- [ ] **Step 1: Failing table test** `TestIsWorktreeCommand`:
  - true: `git worktree add ../x -b y`, `cd a && git -C r worktree remove p`, `git switch -c feat`, `git branch feat`, `git branch -D feat`, `git push origin --delete feat`, `git push origin :feat`
  - false: `git status`, `git push origin feat`, `git branches-report` (no whitespace after `branch`)
  - `ls worktrees.md` is an accepted true, with a comment saying so
- [ ] **Step 2: Implement. Push.** Commit `feat(hookevents): add the worktree probe event`.

### Task 7: Hook producers

**Files:**
- Modify: `internal/claudehook/claudehook.go` — the `PostToolUse` matcher becomes `AskUserQuestion|ExitPlanMode|Bash|EnterWorktree|ExitWorktree`. Rewrite the `matchedHookEvents` and `promptToolMatcher` comments (`:154-173`).
- Modify: `internal/claudehook/runhook.go` — `claudeStdin` adds `CWD string \`json:"cwd"\``, `ToolInput json.RawMessage \`json:"tool_input"\``, `ToolResponse json.RawMessage \`json:"tool_response"\``. In `case "PostToolUse"`: a prompt tool keeps today's branch; ANY OTHER tool spools only a `WorktreeProbe` line or nothing — never a `PostToolUse` line (that line is a work-start edge that clears unseen marks, `hookevents/workstate.go:151`). `SubagentStart` additionally spools a probe with `cwd`, EXCEPT when the cwd is under `<repo>/.claude/worktrees/` (Claude-managed, auto-removed).
- Modify: `internal/codexhook/codexhook.go` — `registeredEvents` adds `{name: "PostToolUse", key: "post_tool_use", timeout: hookTimeoutSec}`; rewrite the comment at `:134-137`
- Modify: `internal/codexhook/runhook.go` — the same three stdin fields; `PostToolUse` with `tool_name == "Bash"` spools a probe or nothing
- Modify: `internal/opencodehook/scripts/quil-session-tracker.js` — keep `input.directory` from init; add `"tool.execute.after"` for tool `bash`; dir = `path.resolve(directory, args.workdir || ".")`; spool `WorktreeProbe` with `cwd` (dropped when > 1024 UTF-8 bytes), `data.trigger="shell"`, `data.command`
- Both Go producers: a `spoolProbe(env, nowMs, sessionID, dir, trigger, cmd string) error` helper sets `Payload.CWD` and a non-empty `Title` ("Worktree command"). Do NOT gate probes on `agent_id`: a subagent's worktree is wanted (unlike the `PreToolUse` branch above it).
- A dir over `MaxProbeDirBytes` is DROPPED, never truncated, and logged to `hook.log`.

- [ ] **Step 1: Failing tests:**
  - `TestRunHook_EnterWorktreeSpoolsProbeWithWorktreePath` (the measured payload, with a 140-byte Windows path)
  - `TestRunHook_BashPostToolUseNeverSpoolsResumeEdge`
  - `TestRunHook_BashWithoutWorktreeWordSpoolsNothing`
  - `TestRunHook_SubagentProbeSkipsClaudeManagedWorktrees`
  - `TestRunHook_ProbeDirOverCapIsDropped`
  - `TestBuildSettingsJSON_PostToolUseMatcherCoversWorktreeTools`
  - `TestRunHook_CodexBashWorktreeAddSpoolsProbe`
  - `TestBuildConfigOverride_RegistersPostToolUse` (the new key exists; the ten existing keys and hashes are unchanged)
  - `TestOpencodeScript_ProbeRegexMatchesGo` (the embedded script contains `WorktreeCommandPattern` verbatim)
- [ ] **Step 2: Implement. Push; CI green.** Commit `feat(hooks): report worktree-related agent commands to the daemon`.
- [ ] **Step 3: Measure, do not assume:** codex 0.166 must run a matcher-less `PostToolUse` group with no trust prompt. Repeat the 2026-11-07 `codex exec -c hooks=…` probe with Quil's real override (built from a dev daemon's spawn args or a test print), and confirm `hook: PostToolUse Completed`. Cost to state in the PR: one extra hook process per Bash call (~80 ms on Windows) for Claude and Codex.

### Task 8: Daemon record and the compare

**Files:**
- Create: `internal/daemon/agentwt.go`, `agentwt_test.go`
- Modify: `internal/config/config.go` — `AgentWorktreesPath() string` (`$QUIL_HOME/agent-worktrees.json`)
- Modify: `internal/ipc/acl.go:122-127` + `acl_test.go` — add the stem `"agent-worktrees"`
- Modify: `internal/daemon/daemon.go` — in `hookEventsWatcher` (`~:7290`), BEFORE `Submit`: a line with `HookEvent == EventWorktreeProbe` goes to `d.agentWT.probe(p)`, after repeating `emitHookEvent`'s mode=="off" and unknown-pane checks; it never enters the coalescer or the queue. In `emitHookEvent`, a payload that `ClassifyWorkEvent` maps to a stop also calls `d.agentWT.turnEnded(p.PaneID)` after the normal emit.
- Modify: `internal/daemon/worktree_add.go` — on success, `d.agentWT.noteOwnWorktree(repo, path)`
- Daemon start: load the record (small file). Changes are written through `internal/atomicfile`, debounced 2 s, and flushed on stop.

**Interfaces:**
- `type AgentWTEntry struct { ID, Kind, Repo, Path, Branch string; FirstSeen, GoneAt time.Time; Gone bool; PaneID, PaneName, Source, SessionID, Trigger, Command string }` (`ID` is a daemon-minted uuid; `Kind` is `"worktree"` or `"branch"`; a branch entry has an empty `Path`)
- `agentWTStore` methods: `probe(p hookevents.Payload)`, `turnEnded(paneID string)`, `noteOwnWorktree(repo, path string)`, `entries() []AgentWTEntry`, `byID(id string) (AgentWTEntry, bool)`, `forget(id string) bool`, `knownPath(norm string) bool`
- On disk: `{"v":1,"repos":{"<normRepo>":{"worktrees":[...],"branches":[...],"seen":"<time>"}},"entries":[...]}`. The `repos` map is the BASELINE.

**Branch compare** (beside the worktree compare, same run, same permit and deadline; `branchListFn = gitworktree.Branches`):
- When `Branches` reports `truncated`, or it errors, skip the BRANCH half only. The worktree half still runs. A capped list must never mark a branch gone, or re-record it as new later.
- First sight: the current branches join the baseline.
- A new branch → a `Kind:"branch"` entry, EXCEPT when a worktree entry created in the same compare already carries that branch. One entry per piece of work.
- A gone branch → its branch entry gets `Gone`. A worktree entry's `Branch` is not touched by the branch half.
- The repository's default branch is never an entry. `noteOwnWorktree(repo, path, branch)` also puts the branch into the baseline.

**Compare rules:**
- The repo key is `normalizeWorktreePath(List(dir)[0].Path)`.
- Skip without listing: a non-repo dir, and a probing pane that is a sandbox pane (`SandboxImage != "" || SandboxBackend != ""`).
- A `List` error compares NOTHING and marks nothing gone.
- First sight of a repo: the current worktrees become the baseline, and nothing is recorded.
- After that:
  - a path that is new against the baseline → an entry attributed to the probing pane (`PaneID`, `PaneName`, `Source`, `SessionID`, `Trigger`, `Command`), and added to the baseline;
  - a baseline path that is gone → the matching entry gets `Gone=true`, or is just dropped from the baseline when there is no entry;
  - an entry that turns Gone within 60 s of `FirstSeen` is deleted rather than kept.
- `noteOwnWorktree` paths go into the baseline only. The main checkout is never an entry.
- `turnEnded(pane)`: compare every repo that pane probed since its last turn end, plus the repo of its current `CWD`.
- Concurrency: per-repo single-flight with a dirty flag (one more run); a worker goroutine; `claimBlockingFSCall`; 10 s deadline.
- Bounds: 64 repos (least recently seen evicted), 500 entries (Gone first, then oldest).

- [ ] **Step 1: Failing tests:**
  - `TestAgentWT_FirstSightIsBaselineOnly`
  - `TestAgentWT_NewWorktreeAttributedToProbingPane`
  - `TestAgentWT_TwoProbesInOneTickBothCompared` (Review Focus 4: two spool lines, one watcher tick)
  - `TestAgentWT_BurstOfProbesListsOncePerRepo`
  - `TestAgentWT_OwnWorktreeIsNotRecorded`
  - `TestAgentWT_ListErrorMarksNothingGone`
  - `TestAgentWT_RemovedWorktreeMarkedGone`
  - `TestAgentWT_ShortLivedEntryIsDropped`
  - `TestAgentWT_SandboxPaneIgnored`
  - `TestAgentWT_TurnEndComparesProbedReposAndPaneCWD`
  - `TestAgentWT_PersistRoundTripKeepsBaseline` (`t.Setenv("QUIL_HOME", t.TempDir())`)
  - `TestAgentWT_CapsEvictGoneFirst`
  - `TestAgentWT_NewBranchRecorded`
  - `TestAgentWT_BranchOfNewWorktreeIsNotASecondEntry`
  - `TestAgentWT_TruncatedBranchListSkipsBranchHalfOnly`
  - `TestAgentWT_DefaultBranchNeverRecorded`
  - `TestHookEventsWatcher_ProbeBypassesCoalescerAndQueue`
  - the `IsQuilHomeEntry` drift test stays green
- [ ] **Step 2: Implement. Push; CI green.** Commit `feat(daemon): record worktrees that appear while agents run`.

### Task 9: PR B docs and the end-to-end check

- [ ] **Step 1: Docs.**
  - `.claude/rules/hooks-and-sessions.md`: the codex "NOT PostToolUse" text, the "PostToolUse only for interactive-prompt tools" text, the new matcher, the OpenCode handler, the probe's path around the coalescer.
  - `internal/hookevents/workstate.go:147-152` comments.
  - `.claude/rules/projects.md`: a new section "Worktrees that appear while agents run" — the baseline, the hook as a TRIGGER and the compare as the truth, "appeared while", not "made by" (so nobody later strengthens the claim), and the sandbox skip.
  - `docs/features.md`.
  - `changelog.d/feat-agent-worktree-tracking.md`.
- [ ] **Step 2: Dev-mode end-to-end check, ONLY if the user asks for one** (it needs `./scripts/dev.sh build`). In a dev claude pane, the agent runs `git worktree add ../x -b y` → `.quil/agent-worktrees.json` holds one entry with that pane id and `trigger:"shell"`.
- [ ] **Step 3: Push; open PR B** `feat(daemon): track worktrees that appear while agents run`. CI green, then `review: requested`.

---

## PR C — cleanup view

### Task 10: IPC for the record

**Files:**
- Create: `internal/ipc/agentwt.go` — `MsgAgentWorktreesReq/Resp`, `MsgAgentWorktreeActReq/Resp`
- Create: `internal/daemon/agentwt_ipc.go`, `agentwt_ipc_test.go`
- Modify: `internal/daemon/worktree_status.go` — the ownership filter also accepts `d.agentWT.knownPath(norm)` (entries that are not Gone)
- Modify: `internal/clientauth/rights.go`, `sweep_test.go`

**Interfaces:**
- `AgentWorktreesReqPayload{}` → `AgentWorktreesRespPayload{ Entries []AgentWorktreeInfo; Error string }`. `AgentWorktreeInfo` = the entry fields + live `Exists bool` + `InUseBy string`.
- `AgentWorktreeActReqPayload{ ID string; RemoveWorktree, DeleteBranch, DeleteRemoteBranch, Forget bool; ConfirmedBranch string }` → `AgentWorktreeActRespPayload{ ID, Error, Notice string }`.
- The act handler runs on a worker goroutine with its own single-flight. Before ANY removal it re-checks against git:
  - `list := List(entry.Path)`
  - `normalizeWorktreePath(list[0].Path) == entry.Repo`
  - `entry.Path` matches a NON-main entry of `list`
  - `paneInWorktree(entry.Path) == ""` (it covers CWD and recorded paths)

  On any failure it refuses with a reason. Then it calls `removeWorktreeWithBranch` (Task 4) with `Confirmed = [ConfirmedBranch]`. `Forget` drops the entry and keeps the baseline.
- A `Kind:"branch"` entry accepts only `DeleteBranch`, `DeleteRemoteBranch` and `Forget` (`RemoveWorktree` is refused). Re-check before acting:
  - the branch is still in `Branches(repo)`;
  - it is not the default branch;
  - `ConfirmedBranch == entry.Branch`.

  The local delete is `DeleteLocalBranch`, which git refuses when the branch is checked out anywhere. The remote delete is the Task 4 path, with the same policy gate.
- `AgentWorktreeInfo` for a branch entry carries `Unmerged`, `Remote`, `RemoteReason` and `Unpushed` from `ReadBranchFacts` run against the repo, computed only for the entry the dialog asks about (a new `AgentWorktreeFactsReq{ID}` → `ipc.WorktreeStatus`, so the Task 5 helpers render it unchanged).
- Rights: both requests `ClassAct`; `delete_remote_branch` joins the `carriers` arm from Task 4; both responses `neverAccepted`.

- [ ] **Step 1: Failing tests:**
  - `TestAgentWTAct_UnknownIDRefused`
  - `TestAgentWTAct_PathNoLongerListedRefused`
  - `TestAgentWTAct_RepoMismatchRefused`
  - `TestAgentWTAct_MainCheckoutRefused`
  - `TestAgentWTAct_InUseRefused`
  - `TestAgentWTAct_RemoteRefusedWhenPolicyOff`
  - `TestAgentWTAct_ForgetKeepsBaseline`
  - `TestWorktreeStatus_AcceptsRecordedAgentPath`
  - the class-table entries
- [ ] **Step 2: Implement. Push; CI green.** Commit `feat(daemon): list and act on agent worktrees over IPC`.

### Task 11: Worktrees dialog

**Files:**
- Create: `internal/tui/agentwt_dialog.go`, `agentwt_dialog_test.go`
- Modify: `internal/tui/dialog.go` (the new `dialogAgentWorktrees`), the palette (`Worktrees…` in **Projects**), the project context menu (`Worktrees…`), `internal/keymap` (`project.worktrees`, unbound, `TierLate`)

**Behaviour:**
- Opens on the active project's destination; 6 s timeout with a visible reason.
- Rows are grouped by repo, the active project's repo first: `<branch>  <path tail>  appeared while <pane name|closed pane> ran (<source>)  <age>  [gone|in use]`. Windowed with `historyWindow`; the cursor row is marked with `> `.
- `Enter` opens a detail panel in the same box, built from the Task 5 helpers in `worktree_rows.go`: the file list, warnings, the three checkboxes, and the same `y` gate for remote.
- `f` forgets the entry (Enter confirms). `g` toggles Gone entries (hidden by default).
- Everything is sanitised and bounded; the fit test asserts the property.

- [ ] **Step 1: Failing tests** (through `Update`):
  - `TestAgentWTDialog_ListsGroupedByRepoActiveFirst`
  - `TestAgentWTDialog_LabelSaysAppearedWhile`
  - `TestAgentWTDialog_DetailReusesCloseRowsAndRemoteNeedsY`
  - `TestAgentWTDialog_TimeoutShowsReason`
  - `TestAgentWTDialog_FitsTerminal`
  - `TestAgentWTDialog_HidesGoneByDefault`
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Docs.** Update `docs/features.md`, `docs/keybindings.md`, and `.claude/rules/tui-dialogs.md`. Add `changelog.d/feat-agent-worktree-view.md`.
- [ ] **Step 4: Push; open PR C** `feat(tui): add a view to clean up agent worktrees`. CI green, then `review: requested`.

## Out of scope (named so nobody assumes it)

- The MCP `destroy_pane` tool and the browser keep `remove_worktree` only, with no branch deletes. `handleDestroyPaneReq` passes zero options.
- Remote daemons need nothing extra: the record lives where the agent ran, and the policy is pushed to every attached destination.
