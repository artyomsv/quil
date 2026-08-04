# Projects, Sidebar, and Multi-Daemon Routing — Design

| Field | Value |
|---|---|
| Date | 2026-08-01 |
| Status | Implemented and shipped in v1.47.0 — historical record |
| Plan | `docs/superpowers/plans/2026-08-01-projects-core.md` |
| Supersedes | `docs/roadmap/workspace-files.md` (M9) is folded in as a later layer, not implemented here |

## Problem

Quil's tabs are a flat list. Six tabs across three repositories are visually
indistinguishable, so identifying which tab belongs to which piece of work costs
a read of the tab name and a guess. Running several AI agents at once makes this
sharply worse: an agent parked on a permission prompt in a background tab is
invisible until you happen to look at it.

Separately, `quil --remote <host>` binds a whole TUI process to one daemon. Working
against a laptop and a build host at the same time means two terminals and two
mental contexts.

Both are the same problem: there is no grouping layer, so there is nowhere to hang
"which work is this" or "which machine is this."

## Solution overview

Add **projects** as an explicit layer above tabs, and a reserved left sidebar that
renders project state, per-pane agent state, and per-pane git state. A project is
bound to exactly one daemon, so a remote host's projects appear as siblings of
local ones in the same sidebar, in the same client process.

```
┌──────────────────┬────────────────────────────┐
│ PROJECTS         │ AI+Code │ Backend          │
│▸quil        ●2 ⚠1├─────────┴──────────────────│
│ api@gpu01   ●1 ⟳ │                            │
│ infra@prod  ○  ⚡│                            │
│                  │                            │
│ PANES            │                            │
│ AI+Code          │                            │
│ ◐claude ⋯2  main │      ↑1                    │
│ ○shell      main │                            │
│ Backend          │                            │
│ ⚠claude Bash?    │ feat/x ⎇wt ↓3              │
└──────────────────┴────────────────────────────┘
```

## Decisions taken

| Decision | Choice | Rationale |
|---|---|---|
| How a project is created | **Explicit** — user creates it with a name and root dir; tabs are created inside it | Derived-from-CWD grouping is ambiguous for a tab whose panes span repos, and cannot express a non-git project |
| Project ↔ host | **One project = one host** | A `RootDir` is a filesystem path, and a filesystem lives on exactly one machine. Same repo on two machines is two projects |
| Sidebar layout | **Reserved left column**, collapsible | An ambient dashboard must not occlude pane content. The notification sidebar is a right-side overlay, so the two do not collide |
| Git depth | Branch + linked-worktree + ahead/behind | Three cheap plumbing calls. `git status --porcelain` is excluded: it is the one call that can park for seconds on a large repo |
| Sequencing | **One spec** covering projects, sidebar, git, and routing | The scoped-merge edit to `applyWorkspaceState` is identical for one daemon or five; doing it once is cheaper than twice |
| Tab ownership | Each project owns its own `[]*TabModel` and its own `activeTab` index | Isolation is the point of the feature. A flat slice with a filtered render reintroduces exactly the ambiguity being removed |
| Destroying a project | Destroys its tabs and panes | Mirrors the existing tab→pane precedent |
| Tab bar scope | Shows only the active project's tabs | Otherwise the sidebar is redundant |

## Architecture

### 1. Data model

Projects are **daemon-owned and persisted**. A client-side-only grouping would be
lost on a fresh client, would not be visible to a second client, and could not be
used for MCP scoping.

```go
// internal/daemon/session.go
type Project struct {
    ID        string   // "proj-" + uuid.New().String()[:8]
    Name      string
    RootDir   string
    TabIDs    []string
    ActiveTab string   // restores the tab you left when you switch back
}

type SessionManager struct {
    projects      map[string]*Project
    projectOrder  []string
    activeProject string
    tabs          map[string]*Tab   // Tab gains ProjectID
    panes         map[string]*Pane
    // ... existing fields
}
```

`Project` has **no `Dest` field**. The daemon does not know it is remote. `Dest` is
the client's label for the connection a project arrived on:

```go
// internal/tui/
type ProjectModel struct {
    ID, Name, RootDir string
    Dest      string        // "" = local daemon
    tabs      []*TabModel
    activeTab int           // index into THIS project's tabs
}

type Model struct {
    projects      []*ProjectModel
    activeProject int
    // m.tabs and m.activeTab are removed
}
```

New IPC messages mirror the existing tab set exactly: `MsgCreateProject`,
`MsgDestroyProject`, `MsgUpdateProject`, `MsgSwitchProject`, `MsgReorderProject`.

Client config gains a list of destinations to connect to. Projects come *from*
daemons; the client never invents one.

### 2. Multi-daemon routing

`internal/tui/model.go:215` already defines the seam this needs:

```go
type tuiClient interface {
    Send(*ipc.Message) error
    Receive() (*ipc.Message, error)
}
```

A router is a third implementation alongside `*ipc.Client` and the test fake:

```go
type router struct {
    conns map[string]*ipc.Client  // dest -> client, "" = local
    in    chan *ipc.Message       // fan-in from N Receive goroutines
}
```

`ipc.Message` gains exactly one field:

```go
Origin string `json:"-"`   // never serialized: no wire change, no version bump
```

**Origin is bidirectional.** The router stamps it on receive. The Model stamps it on
send — `resizeAllPanes` (`model.go:5050`) and `sendAllLayouts` (`model.go:5156`)
set `msg.Origin = project.Dest` as they iterate. The zero value resolves to **the
active project's dest**, not to local, so a missed stamp fails toward the daemon
you are looking at rather than toward the wrong machine.

Sends to an offline dest are dropped with a log, never an error that would break a
broadcast loop mid-iteration.

**When the active project's daemon drops**, the client stays on that project and
renders it in a parked state — panes keep their last content, the sidebar shows `⚡`,
and reconnect runs for that dest alone. It does not auto-switch to a live project:
silently moving the user to different work is worse than showing stale work honestly
labelled, and the existing remote-mode parking behaviour already establishes this.

**Scoped merge.** `applyWorkspaceState` currently does `m.tabs = nil` and rebuilds
from one daemon's full state (`model.go:3110`). With several daemons broadcasting
`MsgWorkspaceState` into one Model, each would clobber the others. It becomes
`applyWorkspaceState(state, dest)`: drop only the projects owned by `dest`, rebuild
those, leave the rest untouched.

**Reconnect** becomes one instance per dest rather than the current singleton
(`reconnect.go`, one `m.reconnect.attempt`, one `m.remoteDest` at line 327).
`clientGen` already exists for client-swap identity and generalizes unchanged.

**`remoteDest` global.** Of 53 call sites, 15 are CLI guards (`daemonctl.go`,
`update_apply.go`, `remote_setup.go`, `mcp.go`, `version_gate.go`) that describe the
*process's* default daemon and stay correct as written. Roughly 10 are TUI cosmetic
or gating sites that read the active project's dest instead. Three are structural
and are covered above.

### 3. Accessor migration

81 `m.tabs` and 102 `m.activeTab` sites, 57 of them in `model.go`. The majority want
the **active project** and are only written against "all tabs" because today all
tabs *is* the active project. Accessors preserve their shape:

```go
func (m *Model) cur() *ProjectModel  { return m.projects[m.activeProject] }
func (m *Model) curTabs() []*TabModel
func (m *Model) curTab() *TabModel
```

The minority that must become genuinely cross-project is a **correctness fix**, not
new work:

| Site | Uses | Why it must span projects |
|---|---|---|
| `workstate.go` `findPaneAndTab` | 12 | Resolves incoming pane events by ID. Background agents in other projects keep firing events; scoping this to the active project would make a blocked background agent invisible — the headline feature |
| `palette_search.go`, `palette.go` | 6 | Unified search across everything is the point of the palette |
| `memory.go` | 3 | The memory report covers the whole daemon |
| `overlay.go`, `discover_client.go`, `reconnect.go` | 3 | Reviewed individually during implementation |

### 4. Sidebar

Reserved left column. Width from config, collapse toggle on `Alt+W`, auto-collapse
below a width threshold.

Open/closed state lives in **config**, not `workspace.json` — the sidebar is a
property of your screen, not of the session. A workspace saved with the sidebar open
and restored with it closed must not fight itself over pane geometry.

Toggling resizes every pane in the active project. The daemon's existing
`appliedCols`/`appliedRows` guard in `handleResizePane` turns duplicates into no-ops,
so a toggle produces exactly one real resize per pane.

| Glyph | Meaning |
|---|---|
| `◐` | working (spinner, `workSpinnerInterval`) |
| `⋯N` | N outstanding subagents |
| `⚠` | blocked, with the reason |
| `○` | idle |
| `✓` | done, unseen |
| `✗` | exited nonzero |
| `⎇wt` | linked git worktree |
| `↑N` / `↓N` | ahead / behind upstream |
| `⟳` / `⚡` | project link status: linked / offline |

Keys: `Alt+P` fuzzy project picker (reuses the `palette.go` matcher), `Alt+Shift+P`
last-project toggle, `Alt+A` attention queue, `Alt+W` sidebar collapse.

Every remote-sourced string rendered in the sidebar — project names, branch names,
pane names from another host — passes through `sanitizeRemoteText`
(`internal/tui/remotetext.go`), consistent with the rest of the remote-correct
dialog work.

### 5. Agent state

The hook event types already distinguish "waiting for you" from "done":
`hook.claude.Notification`, `hook.claude.PermissionRequest`, and
`hook.opencode.permission.ask` arrive as distinct types.
`ClassifyWorkEvent` (`internal/hookevents/workstate.go:66`) currently collapses them
into `WorkEventStop` because the existing UI only needed "mark unseen."

Add `WorkEventPark`, split out of that arm. `WorkEventStop` keeps meaning "turn
completed"; `WorkEventPark` means "blocked on the user." This is a classifier change
plus a render — no new hook plumbing.

`PaneModel` gains `BlockedSince time.Time` and `BlockedReason string`. The reason is
already captured as a string on the event (`internal/hookevents/types.go:108`).

**Attention queue.** `Alt+A` collects blocked panes across all projects, sorts by
`BlockedSince` ascending, and jumps project, tab, and focus in one key. Order is
oldest-blocked-first, deliberately not sidebar order.

### 6. Git subsystem

Daemon-side cache keyed by **resolved git-common-dir**, so N panes in one repository
cost one invocation. Refreshed on a ticker plus a `.git/HEAD` mtime check. Results
ride the existing workspace broadcast in `PaneInfo` — no new request/response pair,
no staleness key, no single-flight slot, no added latency on the broadcast path.

| Datum | Command |
|---|---|
| Branch | `git rev-parse --abbrev-ref HEAD` |
| Linked worktree | `git rev-parse --git-dir --git-common-dir` (unequal ⇒ linked) |
| Ahead / behind | `git rev-list --left-right --count @{u}...HEAD` |

Each call gets a hard timeout and runs under the existing `maxBlockingFSCalls` permit
pool — a git invocation on a dead network mount is the same hazard `browse.go`
already bounds, and a daemon runs for weeks.

Non-repository CWDs cache a negative result so they are not re-probed. A pane whose
branch lookup times out renders its last known value marked stale, never a guess.

`git status --porcelain` is deliberately excluded. It is the one call that can take
seconds on a large repository without fsmonitor, and it would require a second
cadence, a config gate, and its own timeout budget.

### 7. MCP scoping

Scope is derived from the bridge's parent pane → its tab → its project. All 18 tools
filter to the caller's project by default.

This is a **behavior change to shipped tools**, so `scope: "all"` is an explicit
opt-out. A bridge whose parent pane no longer exists cannot derive a project and
falls back to unscoped — returning nothing would be indistinguishable from a broken
daemon.

### 8. Listening ports

Per-pane listening ports, sourced from the process-tree walk that memory reporting
already performs. Three platform implementations behind build tags, following the
existing `internal/pty` pattern:

| Platform | Source |
|---|---|
| Linux | `/proc/net/tcp` + inode→pid mapping |
| macOS | `lsof -i -P -n` |
| Windows | `GetExtendedTcpTable` |

Slower refresh cadence than git. This is the largest net-new subsystem in the spec
and is the natural first cut if the implementation plan runs long.

### 9. Persistence and migration

`workspace.json` gains a `projects` array and `Tab.ProjectID`. Existing atomic write
plus `.bak` rotation covers rollback unchanged.

On load, a snapshot with no `projects` key puts every existing tab into a single
project named **"Default"**, rooted at the daemon's CWD. No user action, no data loss,
no version prompt.

## Testing

| Area | Approach |
|---|---|
| Router | Fake `tuiClient` per dest; assert `Send` reaches the dest named by `Origin`, and that an offline dest drops without breaking the loop |
| Scoped merge | Two synthetic workspace states from two dests; assert neither clears the other's projects |
| Accessor migration | Existing tab and pane tests move under a single "Default" project and must pass unchanged — that is the migration's correctness proof |
| Cross-project events | A pane event for a background project must produce a sidebar state change, asserted explicitly (this is the regression the `findPaneAndTab` change exists to prevent) |
| `WorkEventPark` | Table test over real hook event type strings, asserting `Park` and `Stop` stay distinct |
| Git cache | Injected command runner; assert one invocation for N panes sharing a git dir, timeout renders stale rather than empty, negative results are not re-probed |
| Ports | Per-platform parser tests over captured fixture output; the enumerator is a package var seam so the non-native platforms are reachable in CI |
| Migration | A `workspace.json` fixture with no `projects` key must load into one "Default" project with every tab intact |

## Risks

**Accessor migration breadth.** 183 call sites. Mechanical, but a missed site that
should have gone cross-project fails silently — the sidebar simply never lights up
for a background project. The cross-project event test above is the guard.

**Sidebar width.** Panes lose columns. Auto-collapse below a threshold, and the
collapse state stays client-side so a restored workspace never fights the screen.

**MCP behavior change.** Existing agent workflows that enumerate every pane will see
fewer panes. The `scope: "all"` opt-out must ship in the same release, documented in
`docs/mcp.md`.

**v1 breadth.** Four extras were accepted alongside the core. Listening ports is the
most separable and the most platform-specific; cutting it changes nothing else in
this design.

## Explicitly out of scope

- `git status --porcelain` dirty counts (§6)
- `.quil.toml` project definitions — `docs/roadmap/workspace-files.md` layers onto
  this model later; a project becomes either ad-hoc or declared
- Project templates, per-project environment variables, per-project plugin sets
- Sharing or collaboration on a project
