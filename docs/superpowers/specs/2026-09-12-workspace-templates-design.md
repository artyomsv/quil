# Workspace templates — design

Date: 2026-09-12
Status: approved design. Supersedes `2026-09-10-agent-flow-design.md`, whose
machinery this removes.
Branch: `feat/orchestration` (PR #214 — the repository squash-merges, so the
flow feature and its removal collapse into one commit)

## 1. What this is

A template describes a tab: a list of panes, their layout, and an optional
starting prompt for each. Quil recreates that setup on demand. Templates live
in a file the user edits, so anyone can define their own and keep several.

That is the whole feature. There is no orchestration engine, no roles, no
stages, no supervision. An agent team is one shipped template, not a thing
quil knows about.

## 2. Why, briefly

The flow feature hard-coded one pipeline — analyst, developer, reviewer, five
stages, a daemon state machine — and it did not fit the job it was built for.
The same user then ran that job by hand: one tab, four panes, an orchestrator
agent delegating to the others, 13 tasks, a PR with three review rounds. What
quil actually had to supply was the SETUP. Everything else was prompts.

So quil supplies the setup, and the prompts become the user's to write.

Two facts from that run shaped the shipped template, not the mechanism:

- **A codex pane's output cannot be read** — its scrollback and its task result
  are both spinner animation. So a template that wants an answer back tells the
  pane to write a file.
- **The orchestrator had to be told its teammates' pane ids by hand.** So a
  prompt can ask for them with a placeholder.

## 3. `templates.toml`

`$QUIL_HOME/templates.toml`. Embedded default, `LoadTemplates`,
`WriteTemplates` (atomic temp-file + rename), daemon reload on save, F1 editor
— all exactly as `flows.toml` did, and mostly the same code.

```toml
[[templates]]
name = "agent-team"
description = "An orchestrator delegating to an analyst and a developer"
layout = "main-left"

  [[templates.panes]]
  type = "claude-code"
  name = "analyst"
  toggles = ["dangerously_skip_permissions"]
  muted = true
  prompt = """…"""

  [[templates.panes]]
  type = "codex"
  name = "developer"
  toggles = ["auto_workspace_write"]
  muted = true
  prompt = """…"""

  [[templates.panes]]
  type = "claude-code"
  name = "orchestrator"
  toggles = ["dangerously_skip_permissions"]
  main = true
  quil_mcp = true
  prompt = """…"""
```

### Template fields

| Field | Meaning |
|---|---|
| `name` | required, unique, `^[a-z][a-z0-9-]{0,31}$` — the palette shows it |
| `description` | one line, shown beside the name |
| `layout` | `rows` (default), `columns`, `main-left`, `main-top`, `grid` |
| `panes` | 1 to 8 entries, created in listed order |

### Pane fields

| Field | Meaning |
|---|---|
| `type` | required — any plugin name, not only AI ones |
| `name` | pane name; empty means the plugin's own |
| `model` | optional model id, appended as the agent's own flag (`--model`, or `-m` for codex). Ignored for a non-AI plugin |
| `toggles` | plugin toggle NAMES, resolved by `resolveToggles` |
| `cwd` | optional, relative to the chosen directory; must stay inside it. With a branch it also applies to the FIRST pane, through `WorktreeSpec.Subdir` — see below |
| `prompt` | optional starting prompt, typed after every pane exists |
| `muted` | sets the existing per-pane mute, so this pane's turns raise no notification |
| `main` | marks the layout anchor for `main-left` / `main-top`; defaults to the first pane |
| `quil_mcp` | registers the Quil MCP server for this pane at spawn |

Validation, mostly carried over from `flows.toml` because it was right: the
plugin must exist and be available; toggle names must resolve, with no two from
one mutual-exclusion group; an AI pane whose plugin exposes a `permission_mode`
group must select one, or it stops on a prompt nobody answers; the model must
match the existing charset; `prompt`, `name` and `cwd` reject terminal controls
through `config.UnsafeTemplateText`; at most one pane may set `main`; `quil_mcp`
requires a plugin the spawn adapters support. A template that fails validation
is refused at save and named at load.

### Placeholders in `prompt`

Single-pass substitution (`strings.NewReplacer`), so a placeholder inside the
user's own text stays literal.

| Placeholder | Value |
|---|---|
| `{{task}}` | the free text typed in the create dialog; empty if none |
| `{{dir}}` | the working directory, absolute |
| `{{branch}}` | the branch, empty when no worktree was asked for |
| `{{panes}}` | one line per pane in this tab: `name  pane-id  type` |

`{{panes}}` is what removes the "type the pane names in by hand" problem: a
prompt that drives other panes gets their ids without the user supplying them.

## 4. Creating a tab from a template

Palette: **New from template**. Four rows:

1. **Template** — ←/→ through `templates.toml`, with the description shown.
2. **Task** — free text, becomes `{{task}}`. Optional. Multi-line editor.
3. **Directory** — where the panes open. Starts at the active project's root;
   ←/→ cycles the daemon's git discovery; any path may be typed, spaces
   included. **An existing directory is used as it is.**
4. **New branch** — optional. Empty means "work in that directory". Non-empty
   adds a worktree on that branch off the repository containing the directory,
   and the panes open there instead.

Also available as an MCP tool, `create_from_template`, taking the same four
values, because an agent setting up its own workspace is the obvious next use.

Order of work daemon-side, all validated before anything is created:

1. Resolve the template, the directory, and every pane's spawn payload.
   Refuse and create nothing on any error.
2. Create the tab. Name it after the task, or the template when there is none.
3. When a branch was asked for: the existing preparing-placeholder path, then
   the worktree add, then the panes. Otherwise the panes directly.
4. Create every pane in listed order with `constructPaneAt`, which does not
   publish, so N panes never cost N frames.
5. Render the prompts — now that every pane exists, so `{{panes}}` is complete
   — and deliver each with `deliverPrompt`, in listed order.

**Broadcast budget.** The rule being honoured is "no frame PER PANE", not "one
frame ever" — the 64-slot overflow this guards against came from per-pane and
per-keystroke frames at 33 tabs, not from a constant handful. So:

- **no branch: exactly one frame**, after every pane exists;
- **with a branch: three**, and no more. One preparing frame (the tab and its
  placeholder, so the spinner is visible while git runs), one from the existing
  `worktreeAddAndCreate` → `replacePaneAt` swap, which publishes on its own,
  and one final frame after the remaining panes are constructed.

Do not suppress the preparing frame to save one: it is the only thing on screen
for as long as a monorepo checkout takes.

**The response answers immediately**, like `create_tab` does today: the tab id,
the pane ids that exist at that moment, and the branch when one is preparing.
On the branch path the remaining pane ids reach clients in the broadcast, not
in the response.

**A per-pane `cwd` survives the worktree path.** `createPaneInWorktree` assigns
the worktree root to the pane's CWD unconditionally, which would silently drop
the `cwd` of the FIRST pane — and only the first, since the rest are built with
`constructPaneAt` after the checkout exists and join their own. Refusing the
combination would leave an arbitrary rule rather than a clean limitation, so
`ipc.WorktreeSpec` gains `Subdir string` instead: empty keeps today's behaviour
exactly, and a value is joined onto the root. The join is re-validated
daemon-side even though `config.Templates.Validate` already refused an
escaping relative path, because this is a different machine's filesystem and a
symlink inside the checkout can escape after the join. A missing or escaping subdirectory refuses
and leaves no pane; it never falls back to the root, because a pane in the
wrong directory is the failure this feature exists to remove.

Listed order is the user's control over who is briefed first. The shipped
agent-team template puts the orchestrator last so its teammates are already
waiting. This is why `main` exists separately: layout must not force order.

### Toggles and the model are frozen into the pane

`resolveToggles` already turns toggle names into args that land in
`InstanceArgs`. The model flag is appended there too, at creation. So a pane
carries its own arguments for life and respawns identically after a restart,
with no config lookup at spawn and no way for a later edit of the template to
reach a pane that already exists.

## 5. Layout

The daemon does not build layout trees; it stores `Tab.Layout` opaquely and the
TUI owns the tree. So the tab gains TWO persisted, broadcast fields, both set
at creation:

- `TemplateLayout string` — the keyword.
- `TemplateMain string` — the PANE ID of the layout anchor, not an index.
  An index would couple the layout to creation order, and order is what decides
  who is prompted first; the shipped `agent-team` marks its third pane as main
  precisely so the two can differ.

The TUI builds the tree the first time it sees a tab carrying a keyword with no
layout yet, then reports it back with the existing `MsgUpdateLayout`. After
that the tab is an ordinary tab and both fields are spent.

| Keyword | Shape |
|---|---|
| `rows` | every pane stacked top to bottom — today's behaviour, the default |
| `columns` | every pane side by side |
| `main-left` | the `main` pane full height on the left; the rest stacked in a right column |
| `main-top` | the `main` pane full width on top; the rest side by side below |
| `grid` | two columns, filled top to bottom, last pane spanning when odd |

One pure function, `templateLayout(keyword string, panes []*PaneModel, main int) *LayoutNode`,
in `internal/tui`. It is table-testable without a `Model`.

## 6. What is removed

| Removed | Why |
|---|---|
| `internal/flow/` — the whole package | No stages, no transitions, no rounds |
| `report_step`: tool, IPC pair, version-gate bump, the `--toolset flow` bridge mode | Nothing reports to a daemon loop; `quil_mcp` registers the ordinary server |
| The flow registry, dispatch, pause/resume, `flowOnTaskEnd`, `task.flowStep` | No daemon-side loop |
| `flow_paused` / `flow_ready` events and their toasts | A prompt notifies the user itself |
| Palette **New flow** / **Resume flow** / **Cancel flow**; the stage and round sidebar label | Replaced by **New from template**; a tab closes the ordinary way |
| Flow persistence in the workspace snapshot | A created tab keeps no template state |
| `flows.toml`, its config type and its F1 page | Replaced by `templates.toml` |
| `Pane.FlowRole` | Replaced by `Pane.QuilMCP bool` |

`reportStepMinVersion` is removed with `report_step`. The shared
`mcpDaemonMinVersion` stays at 1.72.0 so already-released tools remain available
against that daemon version. Template creation uses its own
`createFromTemplateMinVersion` floor of 1.73.0 because its request type is new
in this release.

## 7. What is kept

- **The per-spawn MCP adapters** for Claude Code, Codex and OpenCode, including
  `--strict-mcp-config`, the Codex inherited-server probe and whole-table
  override, and the documented OpenCode limit. They gain a `toolset` parameter
  rather than having their arguments rewritten: the flow path keeps passing
  `flow` until phase 4 deletes it, and a `Pane.QuilMCP` pane passes the empty
  string for the ordinary tool set. A pane carrying both fields — which the
  template path never creates — takes the restricted flow one, because the
  narrower grant is the safe way to resolve a state that should not exist.
- **The directory picker**, with git discovery pinned to the dialog's
  destination, and the typed-path fixes from the review of PR #214
  (focus-routed paste, `msg.Text` for characters).
- **The optional worktree path**, including the preparing placeholder.
- **The toggle and model plumbing**, now frozen into `InstanceArgs`.
- **`.gitattributes` discipline**: `internal/config/templates.toml text eol=lf`
  in the same change that embeds it. An embedded file without it is checked out
  CRLF on a fresh Windows clone; that already broke this branch once.

## 8. Shipped templates

Three, so the feature is legible from the file itself:

1. **`agent-team`** — `main-left`. Analyst (claude-code), developer (codex),
   orchestrator (claude-code, `main`, `quil_mcp`, briefed last). The prompts
   carry what the successful manual run carried: the orchestrator owns git,
   commits, the PR and GitHub issues and is the only one who talks to the user;
   workers never commit; every answer is written to a file because a codex
   pane's screen cannot be read; review findings are posted to the PR with `gh`
   rather than left in a terminal; review rounds end on judgement, not a count.
   It uses `{{panes}}` for the roster and `{{task}}` for the job.
2. **`pair`** — `columns`. One claude-code and one codex pane in the same
   directory, no prompts. The two-pane habit the user already repeats by hand
   in six tabs.
3. **`review`** — `rows`. A lazygit pane and a claude-code pane, prompted to
   review the working tree.

## 9. Error handling

| Event | Result |
|---|---|
| Unknown template, plugin, or toggle name | Refused; nothing created |
| An AI pane with a `permission_mode` group and none selected | Refused; named |
| Directory missing or unusable | Refused; named. Never silently replaced by the project root |
| `git worktree add` fails | The placeholder pane carries the error, as today |
| A pane fails to spawn | The tab keeps its other panes; the failed pane shows `SpawnError` |
| A prompt cannot be delivered (queue full, no process) | Logged, named in the pane; the other prompts still go |
| `templates.toml` invalid at load | The daemon keeps the embedded default and reports the error in the F1 page |

## 10. Testing

- `internal/config`: parse, validate every rule one case each, round-trip,
  single-pass placeholder substitution, the embedded default has no `\r`, and
  its toggle names are read from the shipped plugin TOMLs rather than retyped.
- `internal/daemon`: creating from a template through `handleMessage` makes the
  tab, the panes, their names, their frozen args and the mute flags; prompts
  are delivered after every pane exists and in listed order; `{{panes}}` holds
  every pane id; `quil_mcp` reaches only the panes that asked for it (driven
  through `spawnPane`, the on-switch); a refusal creates nothing; the worktree
  path still works.
- `internal/tui`: `templateLayout` as a table test for all five keywords and
  one to eight panes; the dialog's four rows through `Update`; a tab carrying a
  layout keyword gets its tree built once and reported back.
- Delete the tests of the removed machinery rather than adapting them.

## 11. Out of scope

- Templates spanning hosts or projects.
- Sandbox panes in a template.
- Nested or ratio-controlled layouts. Five keywords, then drag the borders.
- Quil reading or writing GitHub.
- Any daemon-side supervision of what the panes do.
