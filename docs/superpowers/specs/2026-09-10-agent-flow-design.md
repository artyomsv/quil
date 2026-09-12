SUPERSEDED by [Workspace templates](2026-09-12-workspace-templates-design.md); retained as historical design only.

# Agent Flow — design

Date: 2026-09-10
Status: approved design, not started. Re-checked 2026-09-10 against the merged code.
Branch: `feat/orchestration`, rebased onto master at v1.72.0
Depends on: PR #212 (`feat(mcp): projects, remote hosts and pane-to-pane tasks`), released as v1.72.0

## 1. Problem

A feature today is built by three AI panes by hand: an analyst (Claude) plans
and files tickets, a developer (Codex) implements and opens a PR, a reviewer
(Claude) comments on the PR, and the developer fixes. The user is the message
bus, and three things go wrong:

1. After a context switch the user cannot tell which pane the flow is waiting
   on. The sidebar knows pane states, not flow stages.
2. Every handoff is a hand-written prompt. A pane has no role, so an agent
   using the quil MCP server must be told tab and pane names.
3. When the analyst is asked to hand work to the developer pane, it often does
   the work itself with an internal subagent instead. An LLM that owns the loop
   picks the easy path, and no prompt fixes that reliably.

## 2. Decisions

These were agreed in the brainstorm and are not open:

| # | Decision | Why |
|---|---|---|
| 1 | The loop lives in the **daemon**, as code (saga orchestrator). Agents are steps. | An agent cannot skip a step it does not own. Fixes problem 3 by construction. State survives restarts. |
| 2 | A step reports back through **MCP** (`report_step`), never by screen scraping. | Structured results (PR number, verdict). Scraping breaks on wording. |
| 3 | **One built-in flow with knobs**, editable in F1. No general flow engine. | Fits the real case; no engine before it is needed. |
| 4 | **One cycle for the whole epic.** One branch, one PR. | Fewer handoffs. |
| 5 | **Quil makes the panes**: a new worktree tab, one pane per role. | Clean state; the flow owns its tab; the sidebar can label it. |
| 6 | When stuck, **pause and call the user**. No automatic retry. | A misread task must not burn tokens N times unseen. |

Assumptions stated and accepted:

- A flow survives a daemon restart (state in the workspace snapshot).
- Quil never talks to GitHub. Agents use `gh` in their panes. Quil carries only
  the PR number and the review verdict between steps.

## 3. What this builds on

v1.72.0 (PR #212) ships the primitives. This design reuses them and adds
nothing parallel. What is on master, verified:

- `internal/daemon/task.go` — `delegateTask`: types a prompt into a pane
  (bracketed paste, then CR after `pasteSettle`), `taskObserve` follows the
  target's work ledger, `finishTask` ends the task on the settled idle edge
  (`onPaneIdle`, 2 s `agentIdleSettle`), captures the last 30 lines as
  `result`, fails on `process_exit` or pane destroy, times out through
  `TimeoutMs`, and `addLive` enforces one live task per target. The registry
  is runtime-only, bounded to 200. `finishTask` emits a `task_done` event and
  types the notify-back only when `FromPane` is set.
- `internal/daemon/workstate.go` + `internal/hookevents/ledger.go` —
  `Pane.Work` ledger, `applyWorkEvent`, `settleIdle`, `onPaneIdle`.
  **An empty `AgentState` means UNKNOWN, not idle** — a pane whose hooks never
  loaded never produces an idle edge, so a task aimed at it never ends on its
  own (see 4.3 for how the flow copes).
- `create_req.go` — `create_pane_req` / `create_tab_req` with `toggles` by
  NAME (`resolveToggles`, refused when unknown), `name`, `worktree_branch`.
  `handleCreateTabReq` with a worktree answers AT ONCE with a PTY-less
  placeholder pane and swaps the real pane in later (`worktreeAddAndCreate`
  on a goroutine, `worktree_ready` names the replacement). It does not
  switch the active tab.
- `cmd/quil/mcp_version.go` — every bridge probes its daemon's version;
  tools that send request types newer than `mcpDaemonMinVersion` call
  `requireDaemon` first, because an old daemon drops an unknown type
  silently and the tool reads as a 10 s timeout.
- `task_done` notifications and the sidebar attention states (M12/M17).

A flow step IS a task whose requester is the daemon (`FromPane` empty, so
`notify` is off). The engine does not re-implement delivery or idle
detection. It hooks in at exactly two points:

1. `finishTask` calls `d.flowOnTaskEnd(info)` after it releases the registry
   lock, beside the `task_done` emit. That is the engine's only completion
   signal.
2. `task` gains one field, `report *stepReport`, written by `report_step`
   under the registry lock and read by `flowOnTaskEnd`.

## 4. Components

### 4.1 `internal/flow/` — pure state machine (new package)

Stdlib only. No daemon, PTY, or IPC import. Holds the `Flow` type, the step
graph, the transition function, and the result validation. Everything a test
can drive without a `Daemon`.

```go
type Stage string

const (
    StagePreparing  Stage = "preparing" // worktree add + pane creation in flight
    StagePlan       Stage = "plan"
    StageBuild      Stage = "build"
    StageReview     Stage = "review"
    StageFix        Stage = "fix"
    StageReadyForYou Stage = "ready_for_you"
    StageDone       Stage = "done"
)

type Role string // "analyst", "developer", "reviewer"

type Flow struct {
    ID        string
    TabID     string
    Branch    string
    Feature   string            // the user's text
    Stage     Stage
    Paused    bool
    PauseWhy  string            // human-readable, shown in sidebar
    Round     int               // review rounds completed
    Panes     map[Role]string   // role → pane id
    Results   Results
    TaskID    string            // live task for the current stage; runtime-only, cleared on load
    CreatedAt int64
    UpdatedAt int64
}

type Results struct {
    Plan    string // from plan
    PR      string // from build
    Verdict string // "approved" | "changes", from review
    Notes   string // reviewer notes, from review
}
```

Transition table (`Next(f, report) (Flow, error)`):

| Stage | Role | Required result key | On done |
|---|---|---|---|
| preparing | — | — | panes exist → plan |
| plan | analyst | `plan` | → build |
| build | developer | `pr` | → review |
| review | reviewer | `verdict` (+ optional `notes`) | approved → ready_for_you; changes → fix (Round++) |
| fix | developer | none | → review |
| ready_for_you | — | — | terminal until the tab closes |

Guards:

- `review` with `verdict=changes` when `Round+1 > MaxReviewRounds` → paused,
  `PauseWhy = "review round limit reached"`.
- A missing required key → paused, `PauseWhy = "step reported no <key>"`.
- A `blocked` report → paused, `PauseWhy` = the agent's question text.
- Task `failed` (process exit / pane destroyed) or `timeout` → paused with the
  task's error text.
- Idle with no report → paused, `PauseWhy = "agent stopped without reporting"`.

Resume re-enters the same stage. `Round` is not reset.

### 4.2 `internal/daemon/flow.go` — engine

Owns the flow registry (`map[flowID]*flow.Flow`, under `sm.mu` like tabs) and
the glue:

- `startFlow(feature, branch, project)` — in-daemon, so it uses the same
  building blocks `handleCreateTabReq` uses, not the IPC round trip:
  1. Validates everything first, as `handleCreateTabReq` does: all three
     role agents resolve to available `ai` plugins, their toggle names
     resolve (`resolveToggles`), the worktree root resolves. A refusal
     creates nothing.
  2. Creates the tab and the analyst placeholder (`CreateTabInProject` +
     `constructPreparingPane`), answers the requester with the flow id and
     tab id, and records the flow at stage `preparing`.
  3. On a goroutine: `worktreeAddAndCreate` swaps the analyst pane in. Then
     the developer and reviewer panes are built with `constructPaneAt` into
     the same tab with `CWD` = the new worktree path, and ONE broadcast
     follows both, per the one-frame rule. Pane `Name` = role name.
  4. Stores `Panes[role]`, moves to `plan`, dispatches the plan step.
  A worktree add failure follows the existing placeholder rule
  (`failPreparingPane`); the flow pauses at `preparing` with the git error.
  Resume from `preparing` re-runs step 3 only if the tab still holds the
  failed placeholder; otherwise it refuses.
- `dispatch(f)`: renders the stage's prompt (4.4), calls `delegateTask` with
  `ToPane = Panes[role]`, `FromPane` empty, `TimeoutMs` from the
  `step_timeout` knob (0 = none), stores `TaskID`. A refusal (the pane
  already has a live task, or its input queue is full) pauses the flow with
  the refusal text.
- `flowOnTaskEnd(info)`: called by `finishTask`. Looked up by `TaskID`. Reads
  the report stored on the task (4.3). Calls `flow.Next`. Persists. If the
  new stage has a role, dispatches it. If paused or `ready_for_you`, emits a
  sidebar event (`flow_paused` / `flow_ready`) on the step's pane, which also
  drives the desktop toast through the existing attention path.
- `resumeFlow(id)`: only when `Paused`. Clears `Paused`, re-dispatches the
  current stage. Nothing re-dispatches on its own.
- Tab destroy → flow removed. Pane destroy inside the tab → task fails →
  paused ("pane <role> was closed"); resume refuses until a pane of that role
  exists again (out of scope to recreate it in v1; the user closes the tab).

Locking: same rules as `task.go`. Never call `delegateTask` or `deliverPrompt`
under `sm.mu`; take the flow snapshot under the lock, act outside it.

### 4.3 `report_step` — MCP tool + IPC

New IPC pair `MsgReportStepReq` / `MsgReportStepResp`; new tool in
`cmd/quil/mcp_tools_tasks.go`.

Input: `status` ("done" | "blocked"), `result` (object of string values),
`task_id` (optional). Without `task_id` the daemon resolves the one live task
whose `ToPane` is the caller's `QUIL_PANE_ID`; no such task is an error ("no
step is waiting on this pane"). With `task_id` the pane must still be the
task's target, or it is refused. A pane can therefore only report on its own
step, which is the same trust level `delegate_task` already grants a pane's
child.

The report is stored on the task (`task.report`, runtime-only, written under
the registry lock like every other task field) and is read by
`flowOnTaskEnd` when the task ends. **Done = report received AND settled
idle**, in that order. A report after the task already ended is refused
("step already ended"). A second report overwrites the first (an agent may
correct itself before going idle).

**Hookless pane rule.** `AgentState` empty means UNKNOWN: the pane's hooks
never loaded, so no idle edge will ever come and the task would never end.
When a `done` report lands on a task whose target ledger has never seen an
edge, `report_step` itself finishes the task (`finishTask(t, taskDone, "")`)
after `agentIdleSettle`. A pane WITH a ledger keeps the strict rule, because
there the report can arrive mid-turn and the idle edge is the truth.

**Version gate.** `report_step` sends a new request type, so it calls
`requireDaemon` and `mcpDaemonMinVersion` is bumped to the release that ships
it. Otherwise a bridge on a new binary against an old daemon reads the
refusal as a 10 s timeout. The bridge's own pane is always on the local
daemon, so `report_step` never routes to a remote host.

`result` values are untrusted remote text. They are stored raw and pass
`sanitizeRemoteText` at render, like every other daemon-sourced string.
Size cap: 8 KiB per value, 16 values, else refused.

### 4.4 Prompts and roles — `flows.toml`

Path: `$QUIL_HOME/flows.toml` (`config.FlowsPath()`, same shape as
`bindings.toml`). Default embedded via `go:embed`; a missing user file means
the default. Loaded by the daemon at start and on reload (F1 save sends a
reload, like plugins).

```toml
max_review_rounds = 3
# Minutes a single step may run before it pauses as "timed out". 0 = never.
step_timeout_minutes = 0

[roles.analyst]
agent = "claude-code"
# Plugin toggle NAMES, as create_pane takes them (see list_plugins).
# An unattended pane MUST skip permission prompts, or it sits on one forever.
toggles = ["dangerously_skip_permissions"]
prompt = """
You are the analyst. Feature request:

{{feature}}

Study the repository, write an implementation plan, create a GitHub epic with
subtickets via gh, and put the detailed specification in the tickets.
"""

[roles.developer]
agent = "codex"
toggles = ["auto_workspace_write"]
prompt = """
You are the developer. Implement this plan on the current branch and open a
pull request with gh:

{{plan}}
"""
fix_prompt = """
Address every review comment on PR {{pr}}. Reviewer notes:

{{review}}

Push the fixes to the same PR.
"""

[roles.reviewer]
agent = "claude-code"
toggles = ["dangerously_skip_permissions"]
prompt = """
You are the reviewer. Review PR {{pr}} for correctness, tests and project
conventions. Leave your findings as PR review comments with gh.
"""
```

Placeholders: `{{feature}}`, `{{plan}}`, `{{pr}}`, `{{review}}`. Rendering
is plain string replacement; an unknown placeholder is left as-is and the F1
editor warns.

**Fixed tail.** The daemon appends to every rendered prompt, unconditionally
and not editable:

```
When you finish, call the quil MCP tool report_step with status="done" and
result={"<key>": ...}. Required keys for this step: <keys, or "none">. If you cannot
finish, call report_step with status="blocked" and result={"question": "..."}.
Do not do the work of any other role. Do not start subagents to do it.
```

The tail is the protocol; the editable prompt is the task. A bad edit cannot
break the loop.

`agent` must name a plugin of category `ai` that is available on the
destination (`pluginAvailableFor`); an unavailable agent refuses the flow at
start with a clear error, never silently falls back to `terminal`. `toggles`
go through `resolveToggles`, so an unknown name refuses the flow too. The
default toggle names above are checked against the shipped plugin TOMLs in
the plan; the F1 page offers only the names `list_plugins` reports.

### 4.5 MCP server in the role panes

Quil does not register its MCP server in AI panes today; hooks ride
`--settings` (claude), `-c hooks=…` (codex) and `OPENCODE_CONFIG_CONTENT`
(opencode). A role pane must be able to call `report_step`, so the spawn path
for a flow pane adds the server per spawn, the same way it adds hooks:

- claude-code: `--mcp-config <inline json>` naming `quil mcp`
- codex: `-c mcp_servers.quil.command=<quil exe> -c mcp_servers.quil.args=["mcp"]`
- opencode: `mcp` section in the same `OPENCODE_CONFIG_CONTENT`

Flow panes only, keyed on a `FlowRole` field on the pane (persisted, like
`WorktreeOwned`). Non-flow panes are untouched. Exact flag shapes are verified
in the plan against each agent's current docs; if an agent has no per-spawn
way, the flow refuses to start with that agent for that role.

Sandbox panes: out of scope in v1. A role pane is a plain AI pane.

### 4.6 TUI

- **Palette: "New flow"** → dialog (existing dialog system): feature text
  (multi-line editor), branch name (prefilled `feat/<slug>`), project (the
  active one). Submit sends `MsgStartFlowReq`; the daemon answers with the
  flow id and tab id, or an error shown in the dialog. The daemon does not
  switch the active tab (the `create_tab_req` rule: no focus steal); the TUI
  switches itself to the returned tab, because here the user asked for it.
- **Palette: "Resume flow"** — enabled when the active tab's flow is paused.
  **"Cancel flow"** — confirm dialog, then destroys the tab through the
  existing close-tab path (worktree removal dialog included).
- **Sidebar:** a tab that owns a flow shows `<stage>` after its name, and
  `<stage> · round N` from the second review round; paused adds the existing
  "needs you" mark on the step's pane and `⏸` on the tab. `ready_for_you`
  shows `✓ PR <n>`. Rendered from `Flow` fields in the workspace broadcast;
  no new sidebar state.
- **F1 → Settings → Flows:** agent per role (list of available `ai` plugins),
  prompt per role (editor), max review rounds. Saves `flows.toml` atomically
  and sends reload.
- Remote mode: a flow belongs to the daemon that holds its tab. The dialog
  pins its destination at open, like the create-pane dialog.

### 4.7 Persistence and restart

`Flow` (minus `TaskID`) is added to the workspace snapshot beside tabs. On
load: every flow whose stage has a live role is set `Paused` with
`PauseWhy = "daemon restarted"`. The task registry is runtime-only and the
old task is gone, so the step cannot be observed; re-sending its prompt
unasked could double-post a PR. The user resumes from the palette.

A flow whose tab no longer exists on load is dropped. A flow still at
`preparing` on load is dropped too, and its tab stays as an ordinary tab: the
existing placeholder restore rule already repairs a half-made worktree pane,
and the user starts the flow again rather than the engine guessing which of
the three creates had finished.

## 5. Error handling summary

| Event | Result |
|---|---|
| Agent reports `blocked` | paused, question in `PauseWhy` |
| Agent idle, no report | paused, "agent stopped without reporting" |
| Report missing required key | paused, names the key |
| Process exit / pane destroyed | paused, task error text |
| Review rounds exhausted | paused, "review round limit reached" |
| Worktree add fails | flow created paused with the git error |
| Agent plugin unavailable | flow refused at start |
| Daemon restart mid-step | paused, "daemon restarted" |
| Tab closed | flow removed |

Every pause lights the step's pane and sends one desktop toast. Nothing is
undone: the worktree, branch and PR stay as they are.

## 6. Testing

- `internal/flow/`: table tests over the transition function — every row of
  the table, every guard, resume after each pause reason, round counting.
- `internal/daemon/`: flow engine driven through `handleMessage` with the
  `recordingLiveSession` / `agentPane` helpers from `task_test.go` and the
  request helpers in `mcpreq_helpers_test.go`: start → hook idle edges →
  report → next dispatch; idle-without-report pauses; report from the wrong
  pane refused; hookless pane finishes on the report alone; a `delegateTask`
  refusal pauses; restart-load marks paused, drops a `preparing` flow; tab
  destroy removes the flow. Locking: a wedged writer (`wedgedSession`) must
  not block `flowOnTaskEnd`.
- `cmd/quil/`: `report_step` is gated by `requireDaemon`
  (`mcp_version_test.go` pattern) and lists in the tool catalog.
- `cmd/quil/`: `report_step` resolves the caller's live task; refuses when
  none; refuses a foreign `task_id`.
- `internal/tui/`: sidebar label and palette enablement through `Update`
  (per the "test through the ON switch" rule), F1 save writes `flows.toml`
  and sends reload.
- Manual: one full run in dev mode with three real agents, per
  `.claude/rules/dev-environment.md`.

## 7. Out of scope (v1)

- User-defined step graphs, parallel steps, per-ticket cycles.
- Automatic retry of a step.
- Recreating a destroyed role pane.
- Sandbox role panes.
- Flows spanning hosts.
- Quil reading or writing GitHub itself.

## 7a. Addendum, 2026-09-11: first manual run

The first hands-on run of the built flow found four gaps in this design, all
fixed on the same branch:

- **Repository choice.** 4.6 said "project (the active one)" and let the daemon
  pick the project root. The dialog now has a Repository row (daemon git
  discovery on ←/→, or a typed path) and `StartFlowReqPayload.CWD` carries it.
  The daemon refuses an unusable directory instead of falling back.
- **Layout.** Role panes stacked into three full-width rows. The TUI now places
  them analyst | developer / reviewer when it adds panes it did not create.
- **Model per role.** `FlowRole.Model` (F1 row, `flows.toml` key) reaches the
  agent's own `--model` / `-m` flag at spawn. Charset-validated only.
- **Prompts.** The two-line defaults in 4.4 were placeholders. The shipped
  `flows.toml` now carries full role briefs; 4.4's listing is superseded by
  that file.

## 8. Open items for the plan

1. Exact per-spawn MCP registration flags for claude-code, codex, opencode
   (4.5) — verify against current agent versions before writing code. PR #212
   did not add any; AI panes still get only hooks per spawn.
2. The default toggle names in `flows.toml` (4.4) — read them off
   `internal/plugin/defaults/*.toml` before embedding the default file.
3. `task.report` lands beside `result` on the merged `task` struct; every
   read and write is under `taskRegistry.mu`, the same lock `finishTask`
   already takes. No new lock.

Resolved on 2026-09-10 by reading the merged code: `create_tab_req` cannot
create three panes in one frame, and it answers before the worktree exists,
so `startFlow` uses the daemon's own helpers and a `preparing` stage (4.2).
