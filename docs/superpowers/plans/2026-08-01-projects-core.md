# Projects Core — Implementation Plan

> **Status: SHIPPED in v1.47.0 (PR #123), follow-ups in v1.47.1 (PR #124). This is a
> historical record — do NOT execute it.** The task boxes below were never ticked off
> in the file; the work landed and the decisions worth keeping live in
> `.claude/rules/projects.md`. Read this for how the feature was arrived at, not for
> what to do next.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an explicit project layer above tabs, a reserved left sidebar showing project and per-pane agent state, and a routing layer that lets one client hold projects from several daemons.

**Architecture:** Projects are daemon-owned and persisted; each project owns its own tab slice and its own active-tab index, so nothing is ever filtered. Client-side, a `router` implements the existing two-method `tuiClient` interface and dispatches by an `Origin` field that never touches the wire. Agent state comes from hook events that already distinguish blocked from done.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2, `google/uuid`, standard `testing`.

**Source spec:** `docs/superpowers/specs/2026-08-01-projects-sidebar-design.md`

## Global Constraints

- Go and make are NOT installed on the host. Everything runs through Docker: `./scripts/dev.sh test`, `test-race`, `vet`, `build`.
- **Production isolation:** never touch `~/.quil/`, never run `kill-daemon.sh` / `reset-daemon.sh`, never run bare `./quil`. Dev mode only. Confirm `[dev]` in the status bar before any manual test.
- `./scripts/dev.sh build` refuses to run while any binary it writes is held. Close the dev TUI and dev daemon first.
- Commit messages: imperative, ≤72 chars on line one, Conventional Commits prefix. **No AI/agent attribution of any kind.**
- Before each commit run `git status` and stage by explicit path. Never `git add -A`.
- Platform-specific files use `//go:build`, never `// +build`.
- Bubble Tea v2: `View()` returns `tea.View`, keys are `tea.KeyPressMsg`, quit is `tea.Quit` (value).
- Do not add package detail to `.claude/CLAUDE.md` — it is size-capped and enforced at build time. Scoped rules under `.claude/rules/` take it.

## Verified Codebase Facts

Every task below depends on these. They were checked against the source; do not assume otherwise.

| Fact | Location |
|---|---|
| `Daemon.session *SessionManager` — **not** `d.sm` | `daemon.go:50` |
| `CreatePane(tabID, cwd string) (*Pane, error)` — two returns | `session.go:303` |
| `releasePanes(panes []*Pane)` — package-level func, closes each PTY in its own goroutine | `session.go:224` |
| `LayoutNode{Pane, Split, Ratio, Left, Right}` — **no `Children` field**; `NewLeaf(pane)` builds a leaf | `layout.go:25,34` |
| `PaneModel.working`, `.unseen`, `.subagents` are **unexported** | `pane.go:79,81,82` |
| `applyWorkTransition(paneID, eventType string, data map[string]string)` | `workstate.go:84` |
| Turn-start edge is `hook.claude.UserPromptSubmit` / `hook.opencode.chat.message`. There is **no** `SessionStart` case | `hookevents/workstate.go:33` |
| `fuzzyScore(query, target string) (int, bool)` | `palette.go:109` |
| `reconnectState` carries `active/attempt/lastErr/nextAt/lastUpAt/settledAttempt/parked` plus flap window and decay | `reconnect.go:32` |
| `SnapshotState() (activeTab string, tabs []*Tab, panesByTab map[string][]*Pane)` | `session.go:506` |
| `buildWorkspaceState()` and `workspaceStateFromSnapshot(activeTab, tabs, panesByTab, includeOverlays)` — the map builder shared by disk snapshot **and** live broadcast | `daemon.go:1960,1978` |
| `restoreWorkspace` returns early when `state == nil` | `daemon.go:559` |
| `persist.Save(path, map[string]any)` / `Load(path) (map[string]any, error)` — schema-free by design | `internal/persist` |
| `newTestDaemon(t)` test helper exists | `internal/daemon` tests |
| `activeTabModel() *TabModel` already exists and is used **67 times** — reimplement it, do not add a duplicate | `model.go:3515` |
| Raw `m.activeTab` is only **36** non-test uses; raw `m.tabs` is 81 | measured |
| `d.handleMessage(conn *ipc.Conn, msg *ipc.Message)`, `d.broadcastState()` | `daemon.go:850,1951` |
| `sm.ActiveTabID()` — **not** `ActiveTab()` | `session.go:430` |
| Auto-recovery for the last destroyed tab: `d.session.CreateTab("Shell")` | `daemon.go:988,1145` |
| Confirm dialogs use **fields, not closures**: `m.confirmKind` (string), `m.confirmID`, `m.confirmName`, with `m.dialog = dialogConfirm` | `model.go:256-258`, pattern at `model.go:2060` |
| `renderStatusBar() string` — **not** `statusBar()` | `model.go:3867` |
| `AttachPayload{Cols int, Rows int, CWD string}` — Cols/Rows are **required**, not just CWD | `protocol.go:155` |
| `freezeInput` is a **METHOD** — `func (m Model) freezeInput(msg tea.Msg) (tea.Cmd, bool)`, called at `model.go:597`. It is not a bool field | `reconnect.go:204` |
| `tea.NewProgram(model)` takes the Model **by value** — a closure over the local `model` variable sees frozen startup state forever | `main.go:526`, `NewModel` returns `Model` |
| `SwitchTab` sets only `sm.activeTab`; nothing writes `Project.ActiveTab` | `session.go:436` |
| `handleAttach` creates the default Shell tab when the session has no tabs — on a fresh install this runs while `activeProject` is still `""` | `daemon.go:988` |
| Attach already has two owners: `m.attachToDaemon()` on first `WindowSizeMsg`, and reattach after redial | `model.go:623`, `reconnect.go:625` |
| `closeClientFn` / `SetClientCloser` is the existing conn-release mechanism | `model.go:238` |

**Keybindings.** `alt+w` is `CloseTab`, `alt+a` is `QuickActions`, `alt+shift+p` is `CommandPalette` (all `config.go:271,296,315`). This plan therefore uses only verified-free keys:

| Key | Action |
|---|---|
| `alt+p` | project picker |
| `alt+o` | toggle last project |
| `alt+shift+s` | sidebar collapse |
| `alt+shift+a` | attention queue |
| `alt+shift+n` | new project |

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/ipc/protocol.go` (modify) | Project messages, `Message.Origin` |
| `internal/daemon/project.go` (new) | `Project`, `SessionManager` project CRUD, TabIDs bookkeeping |
| `internal/daemon/migrate_project.go` (new) | Legacy-state upgrade to a "Default" project |
| `internal/daemon/session.go` (modify) | `Tab.ProjectID`, project fields, `DestroyTab` bookkeeping, `SnapshotState` |
| `internal/daemon/daemon.go` (modify) | Project handlers; projects in `workspaceStateFromSnapshot` + restore |
| `internal/tui/project.go` (new) | `ProjectModel`, accessors |
| `internal/tui/router.go` (new) | Fan-in, Origin routing, offline drop |
| `internal/tui/sidebar.go` (new) | Sidebar render + hit testing |
| `internal/tui/projectdialog.go` (new) | Create / rename / destroy project UI |
| `internal/tui/projectpicker.go` (new) | Picker + last-project toggle |
| `internal/tui/attention.go` (new) | Attention queue |
| `internal/tui/model.go` (modify) | `projects`/`activeProject`; scoped `applyWorkspaceState` |
| `internal/tui/workstate.go` (modify) | Cross-project resolve; blocked state |
| `internal/tui/reconnect.go` (modify) | Per-dest `reconnectState` instances |
| `internal/hookevents/workstate.go` (modify) | `WorkEventPark` |

`internal/persist` is **not** modified — it is deliberately schema-free.

---

### Task 1: Protocol — `Origin` and project messages

**Files:** Modify `internal/ipc/protocol.go`; test `internal/ipc/protocol_test.go`

**Interfaces:**
- Produces: `Message.Origin string` (`json:"-"`); `MsgCreateProject`, `MsgDestroyProject`, `MsgUpdateProject`, `MsgSwitchProject`, `MsgReorderProject`, `MsgLinkLost`; payloads `CreateProjectPayload{Name, RootDir}`, `DestroyProjectPayload{ProjectID}`, `UpdateProjectPayload{ProjectID, Name, RootDir}`, `SwitchProjectPayload{ProjectID}`, `ReorderProjectPayload{ProjectID, NewIndex}`.

- [ ] **Step 1: Write the failing test**

```go
func TestMessageOriginIsNeverSerialized(t *testing.T) {
	msg, err := NewMessage(MsgResizePane, ResizePanePayload{PaneID: "pane-abc", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.Origin = "gpu01"

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(data, []byte("gpu01")) {
		t.Fatalf("Origin leaked onto the wire: %s", data)
	}

	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Origin != "" {
		t.Fatalf("Origin survived a round trip: %q", back.Origin)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `msg.Origin undefined`.

- [ ] **Step 3: Write minimal implementation**

On `Message`:

```go
// Origin names the daemon a message came from (set by the router on receive)
// or is destined for (set by the Model on send). Client-side routing state
// only: `json:"-"` keeps it off the wire, so adding it needs no protocol
// version bump. Empty on receive means the local daemon; empty on send means
// "resolve it" — see router.Send.
Origin string `json:"-"`
```

Beside the tab constants:

```go
// Project lifecycle (mirrors the tab message set).
MsgCreateProject  = "create_project"
MsgDestroyProject = "destroy_project"
MsgUpdateProject  = "update_project"
MsgSwitchProject  = "switch_project"
MsgReorderProject = "reorder_project"

// MsgLinkLost is synthesised CLIENT-SIDE by the router when a connection
// fails. It is never written to a socket.
MsgLinkLost = "link_lost"
```

```go
type CreateProjectPayload struct {
	Name    string `json:"name"`
	RootDir string `json:"root_dir"`
}
type DestroyProjectPayload struct {
	ProjectID string `json:"project_id"`
}
type UpdateProjectPayload struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	RootDir   string `json:"root_dir"`
}
type SwitchProjectPayload struct {
	ProjectID string `json:"project_id"`
}
type ReorderProjectPayload struct {
	ProjectID string `json:"project_id"`
	NewIndex  int    `json:"new_index"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh vet`
Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/protocol.go internal/ipc/protocol_test.go
git commit -m "feat(ipc): add project messages and client-side Origin"
```

---

### Task 2: Daemon — `Project`, CRUD, and TabIDs bookkeeping

**Files:** Create `internal/daemon/project.go`, `internal/daemon/project_test.go`; modify `internal/daemon/session.go`

**Interfaces:**
- Produces: `Project{ID, Name, RootDir string; TabIDs []string; ActiveTab string}`; `(*SessionManager)` methods `CreateProject(name, rootDir string) *Project`, `DestroyProject(id string) []*Pane`, `UpdateProject(id, name, rootDir string) bool`, `SwitchProject(id string) bool`, `ReorderProject(id string, newIndex int) bool`, `Projects() []Project` (**values, not pointers**), `ActiveProject() string`, `CreateTabInProject(projectID, name string) *Tab`.

Four traps this task must not fall into:

1. **`sm.mu` is not reentrant.** `CreateTab` delegating to `CreateTabInProject` self-deadlocks if both take the lock. Put the logic in an unlocked `createTabLocked` core and have both exported wrappers take the lock once.
2. **`DestroyTab` must un-register the tab from its project.** Closing a tab is the most common operation in the product; leaving a dangling ID in `TabIDs` gets persisted and broadcast, and the client then looks up a `TabInfo` that does not exist. (The auto-recovery paths at `daemon.go:988` and `daemon.go:1145` call `CreateTab("Shell")`, which registers automatically once `createTabLocked` exists — they need no separate change.)
3. **Fresh install has no project at all.** There is no snapshot, so `migrateToDefaultProject` no-ops, nothing calls `CreateProject`, and `handleAttach` (`daemon.go:988`) creates the default Shell tab while `sm.activeProject` is `""`. That tab would register to no project — and Task 7's client builds tabs **only** from a project's `TabIDs`, so a brand-new user gets an empty screen. `createTabLocked` must bootstrap a "Default" project when `projectID` is empty.
4. **`Project.ActiveTab` must actually be written.** `SwitchTab` (`session.go:436`) sets only `sm.activeTab`. If nothing writes the per-project field, Task 7's `proj.activeTab = indexOfTab(proj.tabs, info.ActiveTab)` reads an always-empty value, `indexOfTab` returns 0, and **the user's tab selection snaps back to tab 1 on every broadcast** — which fires on every tab create/destroy/switch and mouse-mode change.

`Projects()` returns **copies** so a caller holding the slice past the unlock cannot race `UpdateProject` mutating `Name`/`RootDir`.

- [ ] **Step 1: Write the failing test**

```go
func TestCreateProjectAppendsAndActivatesFirst(t *testing.T) {
	sm := NewSessionManager(1024)
	a := sm.CreateProject("alpha", "/tmp/a")
	b := sm.CreateProject("beta", "/tmp/b")

	got := sm.Projects()
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("order = %v, want [alpha beta]", got)
	}
	if sm.ActiveProject() != a.ID {
		t.Fatalf("first project should become active, got %q", sm.ActiveProject())
	}
}

func TestDestroyProjectReturnsPanesAndClearsActiveTab(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	tab := sm.CreateTabInProject(p.ID, "Shell")
	pane, err := sm.CreatePane(tab.ID, "/tmp/a")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	panes := sm.DestroyProject(p.ID)

	if len(panes) != 1 || panes[0].ID != pane.ID {
		t.Fatalf("DestroyProject = %v, want the one pane", panes)
	}
	if len(sm.Projects()) != 0 {
		t.Fatal("project survived destroy")
	}
	if at := sm.ActiveTabID(); at == tab.ID {
		t.Fatalf("activeTab still points at a destroyed tab (%s)", at)
	}
}

func TestDestroyTabDeregistersFromItsProject(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	keep := sm.CreateTabInProject(p.ID, "Keep")
	drop := sm.CreateTabInProject(p.ID, "Drop")

	sm.DestroyTab(drop.ID)

	projects := sm.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}
	ids := projects[0].TabIDs
	if len(ids) != 1 || ids[0] != keep.ID {
		t.Fatalf("TabIDs = %v, want only %s — a dangling ID gets persisted and broadcast",
			ids, keep.ID)
	}
}

func TestFreshSessionBootstrapsADefaultProject(t *testing.T) {
	// Fresh install: no snapshot, no migration, nobody has called
	// CreateProject. handleAttach creates the default Shell tab in exactly
	// this state.
	sm := NewSessionManager(1024)

	tab := sm.CreateTab("Shell")

	projects := sm.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1 — a tab with no project is invisible "+
			"to the client, which builds tabs only from project TabIDs", len(projects))
	}
	if tab.ProjectID != projects[0].ID {
		t.Fatalf("tab.ProjectID = %q, want %q", tab.ProjectID, projects[0].ID)
	}
	if len(projects[0].TabIDs) != 1 || projects[0].TabIDs[0] != tab.ID {
		t.Fatalf("TabIDs = %v, want [%s]", projects[0].TabIDs, tab.ID)
	}
}

func TestSwitchTabRecordsPerProjectActiveTab(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	first := sm.CreateTabInProject(p.ID, "One")
	second := sm.CreateTabInProject(p.ID, "Two")

	sm.SwitchTab(second.ID)

	got := sm.Projects()[0]
	if got.ActiveTab != second.ID {
		t.Fatalf("Project.ActiveTab = %q, want %q — without this the client's "+
			"tab selection snaps back to tab 1 on every broadcast",
			got.ActiveTab, second.ID)
	}
	_ = first
}

func TestCreateTabInProjectSeedsActiveTabWhenEmpty(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	tab := sm.CreateTabInProject(p.ID, "One")

	if sm.Projects()[0].ActiveTab != tab.ID {
		t.Fatal("the first tab in a project must become its active tab")
	}
}

func TestCreateTabDoesNotDeadlock(t *testing.T) {
	sm := NewSessionManager(1024)
	sm.CreateProject("alpha", "/tmp/a")

	done := make(chan struct{})
	go func() {
		sm.CreateTab("Shell") // delegates to the active project
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateTab deadlocked: sm.mu is not reentrant")
	}
}
```

The active-tab accessor is `sm.ActiveTabID()` (`session.go:430`); the backing field is `sm.activeTab`.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `sm.CreateProject undefined`.

- [ ] **Step 3: Write minimal implementation**

On `Tab` in `session.go`: `ProjectID string`. On `SessionManager`: `projects map[string]*Project`, `projectOrder []string`, `activeProject string` — initialise the map in `NewSessionManager`.

Create `internal/daemon/project.go`:

```go
package daemon

import "github.com/google/uuid"

// Project groups tabs under one named piece of work rooted at one directory.
// Daemon-owned and persisted: a client-side-only grouping would be lost on a
// fresh client, invisible to a second client, and unusable for MCP scoping.
//
// Project has NO Dest field. The daemon does not know it is remote — Dest is
// the CLIENT's label for the connection a project arrived on.
type Project struct {
	ID        string
	Name      string
	RootDir   string
	TabIDs    []string
	ActiveTab string
}

func (sm *SessionManager) CreateProject(name, rootDir string) *Project {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	p := &Project{
		ID:      "proj-" + uuid.New().String()[:8],
		Name:    name,
		RootDir: rootDir,
	}
	sm.projects[p.ID] = p
	sm.projectOrder = append(sm.projectOrder, p.ID)
	if sm.activeProject == "" {
		sm.activeProject = p.ID
	}
	return p
}

// DestroyProject removes a project with every tab and pane under it and
// returns the detached panes. Callers MUST hand them to releasePanes OFF-LOCK:
// PTY.Close() blocks until the child is reaped, and doing that under sm.mu
// starves every reader behind the RWMutex writer.
func (sm *SessionManager) DestroyProject(id string) []*Pane {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	p, ok := sm.projects[id]
	if !ok {
		return nil
	}

	var detached []*Pane
	for _, tabID := range p.TabIDs {
		tab, ok := sm.tabs[tabID]
		if !ok {
			continue
		}
		for _, paneID := range tab.Panes {
			if pane, ok := sm.panes[paneID]; ok {
				detached = append(detached, pane)
				delete(sm.panes, paneID)
			}
		}
		delete(sm.tabs, tabID)
		sm.tabOrder = removeString(sm.tabOrder, tabID)
		if sm.activeTab == tabID {
			sm.activeTab = ""
		}
	}

	delete(sm.projects, id)
	sm.projectOrder = removeString(sm.projectOrder, id)

	// Repair activeProject FIRST, then derive activeTab from it. Repairing the
	// two independently — activeTab from global tabOrder, activeProject from
	// projectOrder — lets them disagree, and the client scopes its visible tab
	// list to the active project's TabIDs, so a mismatch renders as a
	// highlighted tab that is not in the list.
	if sm.activeProject == id {
		sm.activeProject = ""
		if len(sm.projectOrder) > 0 {
			sm.activeProject = sm.projectOrder[0]
		}
	}
	if sm.activeTab == "" {
		sm.activeTab = ""
		if p, ok := sm.projects[sm.activeProject]; ok {
			if p.ActiveTab != "" {
				sm.activeTab = p.ActiveTab
			} else if len(p.TabIDs) > 0 {
				sm.activeTab = p.TabIDs[0]
			}
		}
	}
	return detached
}

func (sm *SessionManager) UpdateProject(id, name, rootDir string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	p, ok := sm.projects[id]
	if !ok {
		return false
	}
	p.Name, p.RootDir = name, rootDir
	return true
}

func (sm *SessionManager) SwitchProject(id string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.projects[id]; !ok {
		return false
	}
	sm.activeProject = id
	return true
}

func (sm *SessionManager) ReorderProject(id string, newIndex int) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.projects[id]; !ok {
		return false
	}
	order := removeString(sm.projectOrder, id)
	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex > len(order) {
		newIndex = len(order)
	}
	rest := append([]string{id}, order[newIndex:]...)
	sm.projectOrder = append(order[:newIndex:newIndex], rest...)
	return true
}

// Projects returns COPIES. Returning live pointers would let a caller holding
// the slice past the unlock race UpdateProject mutating Name/RootDir.
func (sm *SessionManager) Projects() []Project {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]Project, 0, len(sm.projectOrder))
	for _, id := range sm.projectOrder {
		p, ok := sm.projects[id]
		if !ok {
			continue
		}
		cp := *p
		cp.TabIDs = append([]string(nil), p.TabIDs...)
		out = append(out, cp)
	}
	return out
}

func (sm *SessionManager) ActiveProject() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeProject
}

func (sm *SessionManager) CreateTabInProject(projectID, name string) *Tab {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.createTabLocked(projectID, name)
}

func removeString(s []string, v string) []string {
	for i, x := range s {
		if x == v {
			return append(s[:i:i], s[i+1:]...)
		}
	}
	return s
}
```

In `session.go`, extract the body of the existing `CreateTab` into `createTabLocked` (no locking) and reduce `CreateTab` to a locking wrapper:

```go
func (sm *SessionManager) CreateTab(name string) *Tab {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.createTabLocked(sm.activeProject, name)
}

// createTabLocked builds a tab and registers it with its project. Caller holds
// sm.mu — sm.mu is not reentrant, so this must never take it.
//
// An empty projectID bootstraps a "Default" project. That is not a defensive
// nicety: on a fresh install there is no snapshot to migrate and nobody has
// called CreateProject, so handleAttach (daemon.go:988) creates the default
// Shell tab with activeProject still "". A tab registered to no project is
// invisible to the client, which builds tabs only from project TabIDs.
// An UNKNOWN non-empty projectID is treated exactly like an empty one. A
// client can race a DestroyProject on another connection against its own
// CreateTabInProject, and honouring the dead ID would set tab.ProjectID to
// something no project owns while appending the tab to nothing — the same
// invisible-tab failure the empty case exists to prevent, reached by a
// different input.
func (sm *SessionManager) createTabLocked(projectID, name string) *Tab {
	if _, known := sm.projects[projectID]; !known {
		projectID = ""
	}
	if projectID == "" {
		if len(sm.projectOrder) > 0 {
			projectID = sm.projectOrder[0]
		} else {
			cwd, _ := os.Getwd()
			p := &Project{
				ID:      "proj-" + uuid.New().String()[:8],
				Name:    "Default",
				RootDir: cwd,
			}
			sm.projects[p.ID] = p
			sm.projectOrder = append(sm.projectOrder, p.ID)
			projectID = p.ID
		}
		sm.activeProject = projectID
	}

	tab := /* ... existing tab construction, unchanged ... */
	tab.ProjectID = projectID

	if p, ok := sm.projects[projectID]; ok {
		p.TabIDs = append(p.TabIDs, tab.ID)
		if p.ActiveTab == "" {
			p.ActiveTab = tab.ID
		}
	}
	return tab
}
```

Extend `SwitchTab` so the per-project field tracks the selection:

```go
func (sm *SessionManager) SwitchTab(tabID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	tab, ok := sm.tabs[tabID]
	if !ok {
		return
	}
	sm.activeTab = tabID
	// Without this the client re-derives activeTab from an always-empty
	// Project.ActiveTab on every broadcast and snaps back to tab 1.
	if p, ok := sm.projects[tab.ProjectID]; ok {
		p.ActiveTab = tabID
	}
}
```

In `DestroyTab`, before removing the tab, de-register it:

```go
if p, ok := sm.projects[tab.ProjectID]; ok {
	p.TabIDs = removeString(p.TabIDs, tabID)
	if p.ActiveTab == tabID {
		p.ActiveTab = ""
	}
}
```

Apply the same de-registration on the auto-recovery path that replaces the last tab.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/project.go internal/daemon/project_test.go internal/daemon/session.go
git commit -m "feat(daemon): add project grouping above tabs"
```

---

### Task 3: Daemon — project IPC handlers

**Files:** Modify `internal/daemon/daemon.go`; test `internal/daemon/project_test.go`

**Interfaces:** Consumes Tasks 1–2. Produces handler cases; each ends by broadcasting workspace state as the tab handlers do.

- [ ] **Step 1: Write the failing test**

The off-lock property cannot be proved with a nil-PTY pane (`releasePanes` already closes each PTY in its own goroutine), so this test proves dispatch and state, which is what it can honestly assert.

```go
func TestHandleProjectMessagesMutateState(t *testing.T) {
	d := newTestDaemon(t)
	p := d.session.CreateProject("alpha", t.TempDir())

	rename, _ := ipc.NewMessage(ipc.MsgUpdateProject, ipc.UpdateProjectPayload{
		ProjectID: p.ID, Name: "renamed", RootDir: "/tmp",
	})
	d.handleMessage(nil, rename)

	got := d.session.Projects()
	if len(got) != 1 || got[0].Name != "renamed" {
		t.Fatalf("projects = %v, want one named renamed", got)
	}

	destroy, _ := ipc.NewMessage(ipc.MsgDestroyProject, ipc.DestroyProjectPayload{ProjectID: p.ID})
	d.handleMessage(nil, destroy)

	if len(d.session.Projects()) != 0 {
		t.Fatal("project survived destroy")
	}
}
```

The dispatch entry point is `d.handleMessage(conn *ipc.Conn, msg *ipc.Message)` (`daemon.go:850`); passing `nil` for `conn` is fine here because these handlers broadcast rather than reply.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — the rename does not take effect; the type is unhandled.

- [ ] **Step 3: Write minimal implementation**

```go
case ipc.MsgCreateProject:
	var p ipc.CreateProjectPayload
	msg.DecodePayload(&p)
	d.session.CreateProject(p.Name, p.RootDir)
	d.broadcastState()

case ipc.MsgDestroyProject:
	var p ipc.DestroyProjectPayload
	msg.DecodePayload(&p)
	detached := d.session.DestroyProject(p.ProjectID)
	releasePanes(detached) // package-level func, closes each PTY off-lock
	d.broadcastState()

case ipc.MsgUpdateProject:
	var p ipc.UpdateProjectPayload
	msg.DecodePayload(&p)
	d.session.UpdateProject(p.ProjectID, p.Name, p.RootDir)
	d.broadcastState()

case ipc.MsgSwitchProject:
	var p ipc.SwitchProjectPayload
	msg.DecodePayload(&p)
	d.session.SwitchProject(p.ProjectID)
	d.broadcastState()

case ipc.MsgReorderProject:
	var p ipc.ReorderProjectPayload
	msg.DecodePayload(&p)
	d.session.ReorderProject(p.ProjectID, p.NewIndex)
	d.broadcastState()
```

`d.broadcastState()` is the real helper (`daemon.go:1951`), the same one the tab cases use.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/project_test.go
git commit -m "feat(daemon): handle project lifecycle messages"
```

---

### Task 4: Daemon — projects in state, snapshot, and migration

**Files:** Create `internal/daemon/migrate_project.go`, `internal/daemon/migrate_project_test.go`; modify `internal/daemon/session.go` (`SnapshotState`), `internal/daemon/daemon.go`

**Interfaces:**
- Produces: `migrateToDefaultProject(state map[string]any)`; `SnapshotState()` gains two returns — `(activeTab string, tabs []*Tab, panesByTab map[string][]*Pane, projects []Project, activeProject string)`; `workspaceStateFromSnapshot` gains matching parameters and writes `"projects"`, `"active_project"`, and `"project_id"` per tab.

**Aim at the right seam.** `workspaceStateFromSnapshot` (`daemon.go:1978`) is shared by the disk snapshot *and* the live broadcast (`buildWorkspaceState`, `daemon.go:1960`). Adding keys only at the `persist.Save` call site leaves every broadcast project-less, and Task 7 would have nothing to parse. Projects must also ride `SnapshotState`'s single consistent view rather than a second lock acquisition — the oscillation hazard documented at `daemon.go:437`.

- [ ] **Step 1: Write the failing test**

```go
func TestMigrateCreatesDefaultProjectForLegacyState(t *testing.T) {
	state := map[string]any{
		"tabs": []any{
			map[string]any{"id": "tab-aaa", "name": "Shell", "panes": []any{"pane-1"}},
			map[string]any{"id": "tab-bbb", "name": "Logs", "panes": []any{"pane-2"}},
		},
		"active_tab": "tab-aaa",
	}

	migrateToDefaultProject(state)

	projects, _ := state["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1 (Default)", len(projects))
	}
	def, _ := projects[0].(map[string]any)
	if def["name"] != "Default" {
		t.Fatalf("name = %v, want Default", def["name"])
	}
	tabIDs, _ := def["tab_ids"].([]any)
	if len(tabIDs) != 2 || tabIDs[0] != "tab-aaa" || tabIDs[1] != "tab-bbb" {
		t.Fatalf("tab_ids = %v, want both tabs in original order", tabIDs)
	}
	if def["active_tab"] != "tab-aaa" {
		t.Fatalf("active_tab = %v", def["active_tab"])
	}
	if state["active_project"] != def["id"] {
		t.Fatalf("active_project = %v, want %v", state["active_project"], def["id"])
	}
	for _, raw := range state["tabs"].([]any) {
		tab, _ := raw.(map[string]any)
		if tab["project_id"] != def["id"] {
			t.Fatalf("tab %v not stamped", tab["id"])
		}
	}
}

func TestMigrateIsNoopWhenProjectsExist(t *testing.T) {
	state := map[string]any{
		"projects": []any{map[string]any{"id": "proj-x", "name": "quil", "tab_ids": []any{"tab-aaa"}}},
		"tabs":     []any{map[string]any{"id": "tab-aaa", "project_id": "proj-x"}},
	}
	migrateToDefaultProject(state)
	projects, _ := state["projects"].([]any)
	first, _ := projects[0].(map[string]any)
	if len(projects) != 1 || first["name"] != "quil" {
		t.Fatalf("migration ran on state that already had projects: %v", projects)
	}
}

func TestMigrateToleratesEmptyState(t *testing.T) {
	state := map[string]any{}
	migrateToDefaultProject(state)
	if _, ok := state["projects"]; ok {
		t.Fatal("a workspace with no tabs needs no Default project")
	}
}

func TestBroadcastStateCarriesProjects(t *testing.T) {
	d := newTestDaemon(t)
	p := d.session.CreateProject("alpha", t.TempDir())
	d.session.CreateTabInProject(p.ID, "Shell")

	state := d.buildWorkspaceState()

	projects, _ := state["projects"].([]any)
	if len(projects) != 1 {
		t.Fatal("the LIVE broadcast must carry projects, not just the disk snapshot")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `migrateToDefaultProject undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/daemon/migrate_project.go`:

```go
package daemon

import "github.com/google/uuid"

// migrateToDefaultProject upgrades a pre-projects workspace state in place.
// State written before this feature has no "projects" key; every tab in it
// belonged to one implicit workspace, so it becomes one project named
// "Default" with tab order preserved exactly.
//
// It operates on map[string]any because internal/persist is deliberately
// schema-free — the workspace schema lives here. RootDir is left empty; the
// caller fills it from its own os.Getwd(), which this function must not guess.
func migrateToDefaultProject(state map[string]any) {
	if existing, ok := state["projects"].([]any); ok && len(existing) > 0 {
		return
	}
	tabs, ok := state["tabs"].([]any)
	if !ok || len(tabs) == 0 {
		return
	}

	id := "proj-" + uuid.New().String()[:8]
	tabIDs := make([]any, 0, len(tabs))
	for _, raw := range tabs {
		tab, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tab["project_id"] = id
		if tabID, ok := tab["id"].(string); ok {
			tabIDs = append(tabIDs, tabID)
		}
	}

	activeTab, _ := state["active_tab"].(string)
	if activeTab == "" && len(tabIDs) > 0 {
		activeTab, _ = tabIDs[0].(string)
	}

	state["projects"] = []any{map[string]any{
		"id": id, "name": "Default", "root_dir": "",
		"tab_ids": tabIDs, "active_tab": activeTab,
	}}
	state["active_project"] = id
}
```

Extend `SnapshotState` to also return `projects []Project, activeProject string` from the same locked read, update its callers, and thread both into `workspaceStateFromSnapshot` so the keys appear in **both** the broadcast and the disk snapshot. Stamp `"project_id"` on each tab entry there too.

In `restoreWorkspace`, call `migrateToDefaultProject(state)` **after** the `state == nil` early return (`daemon.go:559`), then rebuild `sm.projects`/`projectOrder`/`activeProject`, filling an empty `root_dir` with the daemon's own `os.Getwd()`.

**The restore loop must also set `Tab.ProjectID` on each rebuilt `*Tab`** from that tab's `"project_id"` key. Rebuilding the projects map alone is not enough: a restored tab with an empty `ProjectID` makes `DestroyTab`'s de-registration a silent no-op, which reintroduces exactly the dangling-`TabIDs` drift Task 2 exists to prevent — and it only shows up after a restart, when someone closes a tab.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/migrate_project.go internal/daemon/migrate_project_test.go internal/daemon/session.go internal/daemon/daemon.go
git commit -m "feat(daemon): persist projects, migrate legacy tabs to Default"
```

---

### Task 5: Client — `ProjectModel` and the accessor migration

**Files:** Create `internal/tui/project.go`, `internal/tui/project_test.go`; modify `internal/tui/model.go` and every file the compiler names

**Interfaces:**
- Produces: `ProjectModel{ID, Name, RootDir, Dest string; tabs []*TabModel; activeTab int}`; `(*Model)` `cur()`, `curTabs()`, `allTabs()`, `projectOf(tabID string) *ProjectModel`. **`activeTabModel()` is reimplemented, not replaced** — do not add a `curTab()`.

**The compiler is the migration tool.** Deleting `m.tabs`/`m.activeTab` turns every direct site into a compile error — nothing is silently missed.

**Most of the blast radius is already abstracted.** `activeTabModel()` (`model.go:3515`) is the existing "get the active tab" accessor and has 67 non-test call sites. Reimplementing its two-line body over projects leaves all 67 untouched. Only the 36 raw `m.activeTab` uses and the 81 raw `m.tabs` uses need per-site decisions. Introducing a parallel `curTab()` would strand those 67 sites on the old concept and guarantee drift.

**The interim shape, stated explicitly.** `WorkspaceStateMsg` gains no `Projects` field until Task 7. Until then `applyWorkspaceState` must put every tab it builds into **one** synthetic project: reuse `m.projects[0]` if present, else create `&ProjectModel{ID: "proj-interim", Name: "Default"}`. Writer sites (`m.tabs = nil`, `m.tabs = append(...)`, `m.activeTab = x`) become writes to that project's `tabs`/`activeTab`. Task 7 replaces the synthetic project with parsed ones.

- [ ] **Step 1: Write the failing test**

```go
func TestCurTabsReturnsOnlyActiveProject(t *testing.T) {
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{NewTabModel("tab-1", "One"), NewTabModel("tab-2", "Two")}},
			{ID: "proj-b", tabs: []*TabModel{NewTabModel("tab-3", "Three")}},
		},
		activeProject: 1,
	}
	got := m.curTabs()
	if len(got) != 1 || got[0].ID != "tab-3" {
		t.Fatalf("curTabs = %v, want only proj-b's tab", got)
	}
	if len(m.allTabs()) != 3 {
		t.Fatalf("allTabs = %d, want 3", len(m.allTabs()))
	}
}

func TestActiveTabModelRestoresPerProjectTab(t *testing.T) {
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{NewTabModel("tab-1", "One"), NewTabModel("tab-2", "Two")}, activeTab: 1},
			{ID: "proj-b", tabs: []*TabModel{NewTabModel("tab-3", "Three")}},
		},
		activeProject: 0,
	}
	if m.activeTabModel().ID != "tab-2" {
		t.Fatalf("activeTabModel = %s, want tab-2 (the tab proj-a was left on)",
			m.activeTabModel().ID)
	}
	m.activeProject = 1
	if m.activeTabModel().ID != "tab-3" {
		t.Fatalf("activeTabModel = %s, want tab-3", m.activeTabModel().ID)
	}
}

func TestAccessorsAreNilSafeOnEmptyModel(t *testing.T) {
	var m Model
	if m.cur() != nil || m.activeTabModel() != nil {
		t.Fatal("accessors must tolerate a Model with no projects")
	}
	if len(m.curTabs()) != 0 {
		t.Fatal("curTabs on an empty Model must be empty, not panic")
	}
}
```

The nil-safety case matters: ~46 existing tests build a `Model` directly, and startup runs before the first broadcast.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.projects undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

// ProjectModel is the client's view of one daemon-side project plus the
// destination it arrived on. Each project owns its OWN tab slice and its own
// activeTab index — nothing is ever filtered, so no index can be invalidated
// from under a caller.
//
// Dest is client-side only: the daemon does not know it is remote. Empty means
// the local daemon.
type ProjectModel struct {
	ID      string
	Name    string
	RootDir string
	Dest    string

	tabs      []*TabModel
	activeTab int
}

// cur returns the active project, or nil when the Model has no projects yet.
func (m *Model) cur() *ProjectModel {
	if m.activeProject < 0 || m.activeProject >= len(m.projects) {
		return nil
	}
	return m.projects[m.activeProject]
}

func (m *Model) curTabs() []*TabModel {
	if p := m.cur(); p != nil {
		return p.tabs
	}
	return nil
}

// activeTabModel REPLACES the existing two-line body at model.go:3515. Its 67
// call sites are unchanged — they already asked the right question ("the tab
// the user is looking at"); only the answer's derivation moves.
func (m Model) activeTabModel() *TabModel {
	p := m.cur()
	if p == nil || p.activeTab < 0 || p.activeTab >= len(p.tabs) {
		return nil
	}
	return p.tabs[p.activeTab]
}

// allTabs iterates EVERY project. Use it only where the operation genuinely
// spans projects — resolving an incoming pane event, unified search, the
// memory report. Everywhere else wants curTabs().
func (m *Model) allTabs() []*TabModel {
	var out []*TabModel
	for _, p := range m.projects {
		out = append(out, p.tabs...)
	}
	return out
}

func (m *Model) projectOf(tabID string) *ProjectModel {
	for _, p := range m.projects {
		for _, t := range p.tabs {
			if t.ID == tabID {
				return p
			}
		}
	}
	return nil
}
```

On `Model`: delete `tabs`/`activeTab`, add `projects []*ProjectModel` and `activeProject int`. `TabModel` gains `Dest string`.

Work the compiler's error list. **Read sites default to `curTabs()`**, and anything asking for the active tab should call the reimplemented `activeTabModel()` rather than reaching for an index. Writer sites go through the interim project described above.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS — **including every pre-existing test unchanged**. A pre-existing failure means a site took the wrong accessor. Do not edit an old test to make it pass.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/project.go internal/tui/project_test.go internal/tui/model.go
git status   # stage every other file the compiler forced, by explicit path
git commit -m "refactor(tui): give each project its own tabs and active index"
```

---

### Task 6: Client — the cross-project minority

**Files:** Modify `internal/tui/workstate.go`, `palette.go`, `palette_search.go`, `model.go`, `history.go`; test `internal/tui/workstate_test.go`, `internal/tui/history_test.go`

**Interfaces:** Produces `findPaneAndTab(paneID string) (*PaneModel, *ProjectModel, int)`; `PaneRef` gains `ProjectID string`; `(*Model) jumpToPane(paneID string) bool`.

A correctness fix: these sites search "all tabs" today and are accidentally right because only one project exists.

**`memory.go` is already done.** Task 5 moved `tabOrderAndNames` to `allTabs()` — correctly, since it returns tab IDs and a name map with no index, and the memory report is daemon-wide. Expect no change there; do not "restore" it to `curTabs()`.

**Three operations Task 5 had to scope to the active project, which must now span projects.** Each is a real capability that currently drops work:

| Site | Today's behaviour | Required |
|---|---|---|
| `model.go` `setActivePaneMsg` | MCP `set_active_pane` targets any pane daemon-wide; a pane in a background project silently no-ops | Switch project, then tab, then focus |
| `model.go` `handleNotificationKey` "navigate" | The sidebar carries events from every pane. Navigating to a background project's pane no-ops **after** `pushPaneHistory()` already pushed, so history grows an entry for a jump that never happened | Jump across projects; push history only on a jump that succeeds |
| `history.go` `popPaneHistory` | `PaneRef` carries no project ID, so an entry pushed under another project is re-interpreted against the active project's tabs. The `PaneIDs()` guard makes it pop-and-skip rather than jump wrong, so it degrades safely — but cross-project back-navigation is unreachable | `PaneRef` gains `ProjectID`; pop restores that project first |

Factor the shared "switch project, then tab, then focus" move into one `jumpToPane(paneID string) bool` and use it at all three sites plus the palette's `goToPane` — four hand-rolled copies of that sequence is how they drift.

- [ ] **Step 1: Write the failing test**

`working` is unexported (`pane.go:79`) and these are same-package tests, so assert on it directly. The turn-start edge is `hook.claude.UserPromptSubmit` — there is no `SessionStart` case in `ClassifyWorkEvent`.

```go
func TestPaneEventFromBackgroundProjectUpdatesState(t *testing.T) {
	bgPane := &PaneModel{ID: "pane-bg"}
	bg := NewTabModel("tab-bg", "Agent")
	bg.Root = NewLeaf(bgPane)

	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-fg", tabs: []*TabModel{NewTabModel("tab-fg", "Shell")}},
			{ID: "proj-bg", tabs: []*TabModel{bg}},
		},
		activeProject: 0, // the event's pane is NOT in the active project
	}

	m.applyWorkTransition("pane-bg", "hook.claude.UserPromptSubmit", nil)

	if !bgPane.working {
		t.Fatal("a pane event for a background project must still update it — " +
			"this is the whole point of the sidebar")
	}
}

func TestFindPaneAndTabReportsOwningProject(t *testing.T) {
	tab := NewTabModel("tab-bg", "Agent")
	tab.Root = NewLeaf(&PaneModel{ID: "pane-bg"})
	m := Model{projects: []*ProjectModel{
		{ID: "proj-fg", tabs: []*TabModel{NewTabModel("tab-fg", "Shell")}},
		{ID: "proj-bg", tabs: []*TabModel{tab}},
	}}

	pane, proj, idx := m.findPaneAndTab("pane-bg")
	if pane == nil || proj == nil || proj.ID != "proj-bg" || idx != 0 {
		t.Fatalf("findPaneAndTab = (%v, %v, %d), want proj-bg tab 0", pane, proj, idx)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — the background pane is not found, so `working` stays false.

- [ ] **Step 3: Write minimal implementation**

```go
// findPaneAndTab locates a pane by ID across EVERY project and reports the
// owning project and the tab's index within it.
//
// Cross-project is a correctness requirement, not a convenience: agents in
// background projects keep firing hook events, and scoping this to the active
// project would leave a blocked background agent invisible.
func (m *Model) findPaneAndTab(paneID string) (*PaneModel, *ProjectModel, int) {
	for _, proj := range m.projects {
		for i, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			if leaf := tab.Root.FindLeaf(paneID); leaf != nil {
				return leaf.Pane, proj, i
			}
		}
	}
	return nil, nil, -1
}
```

Update callers for the third return. In `palette.go`/`palette_search.go`, swap `curTabs()` for `allTabs()`. (`memory.go` is already on `allTabs()` from Task 5 — leave it.) Palette entries for tabs outside the active project carry the project name so two tabs called "Shell" stay distinguishable.

**`buildPaletteCommands`, `paneNavLabel`, and `findPaneAndTab` must index the SAME slice.** A label built against one and an action resolved against another names the wrong tab. Task 5 deliberately left all three on `curTabs()` so they stayed consistent — flip all three together, and carry the owning project through to the action rather than a bare tab index.

Add the shared jump helper and route the three table sites plus `goToPane` through it:

```go
// jumpToPane moves project, tab, and focus to reach a pane anywhere in the
// workspace. Returns false when the pane no longer exists, so callers can skip
// recording history for a jump that did not happen.
func (m *Model) jumpToPane(paneID string) bool {
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return false
	}
	for i, p := range m.projects {
		if p == proj {
			m.activeProject = i
			break
		}
	}
	proj.activeTab = tabIdx
	proj.tabs[tabIdx].ActivePane = paneID
	return true
}
```

Give `PaneRef` a `ProjectID string`, set it at every `pushPaneHistory` call site, and have `popPaneHistory` restore that project before resolving the tab.

**Add an `allTabs()` fast path while you are here.** `handlePaneOutput` calls it twice per PTY-output message and both spinner loops call it per frame; the pre-Task-5 code ranged a slice with zero allocation. Every caller only reads, so returning the single project's own slice is safe:

```go
func (m *Model) allTabs() []*TabModel {
	if len(m.projects) == 1 {
		return m.projects[0].tabs
	}
	var out []*TabModel
	for _, p := range m.projects {
		out = append(out, p.tabs...)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/workstate.go internal/tui/palette.go internal/tui/palette_search.go internal/tui/memory.go internal/tui/workstate_test.go
git commit -m "fix(tui): resolve pane events across all projects"
```

---

### Task 7: Client — parse projects, scoped merge

**Files:** Modify `internal/tui/model.go`; test `internal/tui/project_test.go`

**Interfaces:**
- Produces: `applyWorkspaceState(state WorkspaceStateMsg, dest string) ([]string, []tea.Cmd)`; `WorkspaceStateMsg` gains `Projects []ProjectInfo`, `ActiveProject string`, **and `Dest string`**; `ProjectInfo{ID, Name, RootDir string; TabIDs []string; ActiveTab string}`; `TabInfo` gains `ProjectID string`; `rebuildTabs(info ProjectInfo, state WorkspaceStateMsg, existingTabs map[string]*TabModel, existingPanes map[string]*PaneModel, paneMap map[string]*PaneInfo, dest string) []*TabModel`.

`WorkspaceStateMsg` needs the `Dest` field because `listenForMessages` returns `parseWorkspaceState(raw)` directly as the `tea.Msg` (`model.go:4084`) — the Update handler is where `applyWorkspaceState` is called, so the dest has to ride the message. Set it from `msg.Origin` at the parse site.

- [ ] **Step 1: Write the failing test**

```go
func TestApplyWorkspaceStateReplacesOnlyItsOwnDest(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-local", Name: "quil", Dest: "", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
		{ID: "proj-gpu", Name: "api", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-9", "Nine")}},
	}}

	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-gpu", Name: "api-renamed", TabIDs: []string{"tab-9"}, ActiveTab: "tab-9"}},
		Tabs:     []TabInfo{{ID: "tab-9", Name: "Nine", ProjectID: "proj-gpu"}},
	}, "gpu01")

	if len(m.projects) != 2 {
		t.Fatalf("projects = %d, want 2 — the local project was clobbered", len(m.projects))
	}
	for _, p := range m.projects {
		switch p.Dest {
		case "":
			if p.Name != "quil" || len(p.tabs) != 1 {
				t.Fatalf("local project changed: %+v", p)
			}
		case "gpu01":
			if p.Name != "api-renamed" {
				t.Fatalf("gpu project not updated: %+v", p)
			}
		}
	}
}

func TestApplyWorkspaceStateDropsProjectsRemovedOnThatDest(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
		{ID: "proj-b", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-2", "Two")}},
	}}
	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-a", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "One", ProjectID: "proj-a"}},
	}, "gpu01")

	if len(m.projects) != 1 || m.projects[0].ID != "proj-a" {
		t.Fatalf("projects = %+v, want only proj-a", m.projects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `applyWorkspaceState` takes one argument.

- [ ] **Step 3: Write minimal implementation**

**Correcting a rationale recorded during Task 5, because you are replacing exactly the code it describes.** Task 5's report claims that indexing `existingPanes` from `curTabs()` instead of `allTabs()` would *dispose live panes*. That is wrong, and believing it will mislead you. The dispose loop (`model.go:3291`) iterates `existingPanes`, so a pane absent from that map is never visited and cannot be disposed. The real consequence of narrowing the index side is a **leak plus a duplicate rebuild**: a background project's panes would miss `existingPanes` *and* `existingTabs`, so their VT emulators are never disposed when the daemon does drop them, and `state.Tabs` entries for them would build fresh `TabModel`s into the active project while the originals survive in theirs. `allTabs()` on both the index side and the `surviving` side remains correct — keep it.

Add the fields, parse them in `parseWorkspaceState` (including `Dest` from `msg.Origin`), and replace the `m.tabs = nil` clobber (`model.go:3110`) plus the interim synthetic project from Task 5 with:

```go
// Preserve every project that did NOT come from this dest. A broadcast is the
// full state of ONE daemon, so it may only replace that daemon's projects —
// clearing all of them lets two daemons clobber each other on every tick.
kept := make([]*ProjectModel, 0, len(m.projects))
existingProjects := make(map[string]*ProjectModel, len(m.projects))
for _, p := range m.projects {
	if p.Dest != dest {
		kept = append(kept, p)
		continue
	}
	existingProjects[p.ID] = p
}

activeID := ""
if p := m.cur(); p != nil {
	activeID = p.ID
}

// A broadcast carrying tabs but NO projects must not empty m.projects — that
// blanks the client. Every pre-task call site sends exactly that shape, and a
// daemon mid-upgrade can too. Fold those into one synthetic project matching
// the pre-projects shape. No tabs AND no projects creates nothing.
incoming := broadcastProjects(state)

rebuilt := make([]*ProjectModel, 0, len(incoming))
for _, info := range incoming {
	proj, ok := existingProjects[info.ID]
	if !ok {
		proj = &ProjectModel{ID: info.ID, Dest: dest}
	}
	proj.Name, proj.RootDir = info.Name, info.RootDir
	proj.tabs = m.rebuildTabs(info, state, existingTabs, existingPanes, paneMap, dest)
	proj.activeTab = indexOfTab(proj.tabs, info.ActiveTab)
	rebuilt = append(rebuilt, proj)
}

m.projects = append(kept, rebuilt...)
m.activeProject = indexOfProject(m.projects, activeID)
```

`rebuildTabs` is the existing per-tab reuse-or-restore loop lifted verbatim out of the old function body, scoped to `info.TabIDs`, stamping `tab.Dest = dest`, and skipping a `TabIDs` entry with no matching `TabInfo`. **Its return must widen to `([]*TabModel, []string, []tea.Cmd)`** — the lifted loop genuinely produces `newPaneIDs` (the caller arms one spinner per ID) and `overlayResizeCmds`, and a single return has nowhere to put them. Hiding them in accumulator state on `Model` is the wrong trade.

Also skip a `TabIDs` entry whose `TabInfo.ProjectID` names a DIFFERENT project — otherwise one `TabModel` lands in two tab bars driving one layout tree. An **empty** `ProjectID` must never trigger that skip: migrated and legacy states carry empty values, and dropping those tabs loses the user's workspace. `indexOfTab`/`indexOfProject` return `0` when the ID is absent so the active pointer always lands somewhere valid. Build `existingTabs`, `existingPanes`, and `paneMap` before this block exactly as the current code does.

Existing one-arg `applyWorkspaceState` tests must gain a `""` second argument — a signature change, not a behaviour edit, so it does not violate Task 5's rule.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/project_test.go
git commit -m "feat(tui): scope workspace state merge to its origin daemon"
```

---

### Task 8: Client — the router

**Files:** Create `internal/tui/router.go`, `internal/tui/router_test.go`; modify `internal/tui/model.go`

**Interfaces:**
- Produces: `NewRouter(conns map[string]Client) *Router` (**exported** — `cmd/quil` constructs it); `(*Router)` `Send`, `Receive`, `Add(dest string, c Client)`, `Remove(dest string)`, `SetActiveDest(dest string)`; interface `destRouter { SetActiveDest(string) }`; `(*Model) syncActiveDest()`, `(*Model) destOfPane(paneID string) string`, `(*Model) sendForPane(paneID string, msg *ipc.Message) error`. Test helper `newFakeConn()` is defined here and reused by Tasks 12, 14, 15 (same package only — Task 17 needs its own).

**`""` is the LOCAL daemon's dest, so it cannot also mean "unstamped".** This is a sentinel collision, and getting it wrong misroutes in the direction the obvious test does not cover. `sendForPane`/`sendForDest` faithfully stamp `""` for every local pane and local tab; if `Send` resolves an empty `Origin` to the active dest, then with any REMOTE project active every local pane's resize, every local tab's layout, and the local lazygit overlay are re-aimed at the remote daemon. Use an explicit `destUnset` sentinel that the `sendFor*` helpers never emit and that `Send` alone resolves — and key the sole-conn fallback off that sentinel too, not off `""`. Test BOTH directions: active-local/target-remote and active-remote/target-local.

**`NewRouter` deliberately takes no `activeDest func() string`.** `tea.NewProgram(model)` receives the Model **by value** (`main.go:526`), and the program mutates only its own copy — so a closure over the `model` variable in `main` reads frozen startup state forever, meaning zero projects, meaning `activeDest()` returns `""` for the entire session. Every unstamped send would then resolve to the local daemon, and **keystrokes are unstamped**: typing into a remote pane would go to the wrong machine. The router therefore holds the value in an atomic that the Model pushes to.

**Pane- and tab-scoped sends must carry an explicit Origin**, because the active project is the wrong answer for any message about a pane in a different one. `sendForPane` is the seam.

**The pump must return after one failure.** Looping means `Receive()` errors instantly on a dead conn, so the loop floods `MsgLinkLost` at CPU speed. The existing single-conn contract is the model: `listenForMessages` (`model.go:4063`) returns `linkLostMsg` **once** and stops.

- [ ] **Step 1: Write the failing test**

```go
type fakeConn struct {
	mu   sync.Mutex
	sent []*ipc.Message
	recv chan *ipc.Message
	err  error
}

func newFakeConn() *fakeConn { return &fakeConn{recv: make(chan *ipc.Message, 8)} }

func (f *fakeConn) Send(m *ipc.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeConn) Receive() (*ipc.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	m, ok := <-f.recv
	if !ok {
		return nil, io.EOF
	}
	return m, nil
}

func (f *fakeConn) sentCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func TestRouterEmitsExactlyOneLinkLostPerFailure(t *testing.T) {
	dead := newFakeConn()
	dead.err = errors.New("connection reset")
	r := NewRouter(map[string]Client{"gpu01": dead})

	first, err := r.Receive()
	if err != nil || first.Type != ipc.MsgLinkLost || first.Origin != "gpu01" {
		t.Fatalf("first = %+v, err = %v", first, err)
	}

	// A pump that loops would have flooded the channel by now.
	time.Sleep(50 * time.Millisecond)
	select {
	case extra := <-r.in:
		t.Fatalf("pump busy-looped: got a second %s — it must return after one", extra.Type)
	default:
	}
}

func TestRouterSendsToDestNamedByOrigin(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: "pane-x"})
	msg.Origin = "gpu01"
	r.Send(msg)

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d, want 1/0", gpu.sentCount(), local.sentCount())
	}
}

func TestRouterEmptyOriginResolvesToActiveDest(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	r.SetActiveDest("gpu01")

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	r.Send(msg)

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatal("an unstamped message must go to the ACTIVE dest, not to local")
	}
}

func TestRouterActiveDestIsMutableAfterConstruction(t *testing.T) {
	// The Model is copied by tea.NewProgram, so a closure captured in main
	// would freeze at startup. The router must read a value that the running
	// program can still update.
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	first, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	r.Send(first)
	if local.sentCount() != 1 {
		t.Fatal("before any SetActiveDest, an unstamped send goes to local")
	}

	r.SetActiveDest("gpu01")
	second, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	r.Send(second)
	if gpu.sentCount() != 1 {
		t.Fatal("SetActiveDest must take effect on later sends")
	}
}

func TestRouterEmptyOriginFallsBackToSoleConn(t *testing.T) {
	// Remote-only startup: no projects yet, so the active dest is still "",
	// and there is no "" conn. The message must still reach the one daemon.
	gpu := newFakeConn()
	r := NewRouter(map[string]Client{"gpu01": gpu})

	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, nil)
	r.Send(msg)

	if gpu.sentCount() != 1 {
		t.Fatal("with exactly one connection, an unresolvable send must still reach it")
	}
}

func TestPaneInputRoutesToThePanesOwnDaemon(t *testing.T) {
	// The regression this whole mechanism exists to prevent: keystrokes are
	// not stamped by default, and the pane being typed into may not belong to
	// the active project.
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	r.SetActiveDest("")

	m := Model{
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-gpu")}},
		},
		activeProject: 0,
	}

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-gpu", Data: "x"})
	m.sendForPane("pane-gpu", msg)

	if gpu.sentCount() != 1 || local.sentCount() != 0 {
		t.Fatalf("gpu=%d local=%d — input for a remote pane must not reach the local daemon",
			gpu.sentCount(), local.sentCount())
	}
}

func TestRouterDropsSendToUnknownDest(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: "pane-x"})
	msg.Origin = "offline-host"

	if err := r.Send(msg); err != nil {
		t.Fatalf("send to an offline dest must drop, not error: %v", err)
	}
	if local.sentCount() != 0 || gpu.sentCount() != 0 {
		t.Fatal("a message for an offline dest must not fall through to another daemon")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `NewRouter undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"log"
	"sync"

	"github.com/artyomsv/quil/internal/ipc"
)

// Router multiplexes several daemon connections behind the single tuiClient
// the Model consumes — a third implementation of that interface beside
// *ipc.Client and the test fake, which is why the Model needs no transport
// change to gain multi-daemon support.
type Router struct {
	mu    sync.RWMutex
	conns map[string]Client
	stop  map[string]chan struct{}
	in    chan *ipc.Message

	// activeDest is an atomic rather than a func() string closure. A closure
	// built in main would capture the Model VALUE that tea.NewProgram copies
	// (main.go:526), so it would report zero projects forever and route every
	// unstamped send — keystrokes included — to the local daemon.
	activeDest atomic.Value // string
}

func NewRouter(conns map[string]Client) *Router {
	r := &Router{
		conns: make(map[string]Client, len(conns)),
		stop:  make(map[string]chan struct{}),
		in:    make(chan *ipc.Message, 64),
	}
	r.activeDest.Store("")
	for dest, c := range conns {
		r.Add(dest, c)
	}
	return r
}

// SetActiveDest is called by the running program whenever the active project
// changes, so the router's default routing target tracks what the user is
// looking at. Safe from any goroutine.
func (r *Router) SetActiveDest(dest string) { r.activeDest.Store(dest) }

func (r *Router) currentDest() string {
	d, _ := r.activeDest.Load().(string)
	return d
}

func (r *Router) Add(dest string, c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.conns[dest]; exists {
		return
	}
	stop := make(chan struct{})
	r.conns[dest] = c
	r.stop[dest] = stop
	go r.pump(dest, c, stop)
}

func (r *Router) Remove(dest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.stop[dest]; ok {
		close(stop)
		delete(r.stop, dest)
	}
	delete(r.conns, dest)
}

// pump reads one connection, stamping every message with its origin so
// applyWorkspaceState knows whose state it is replacing.
//
// On error it emits ONE MsgLinkLost and RETURNS. It must not loop: a dead
// conn fails Receive instantly, so looping floods the channel at CPU speed.
// This mirrors listenForMessages, which returns linkLostMsg once and stops.
// Reconnect installs a fresh conn via Add, which starts a new pump.
func (r *Router) pump(dest string, c Client, stop <-chan struct{}) {
	for {
		msg, err := c.Receive()
		if err != nil {
			select {
			case <-stop:
			case r.in <- &ipc.Message{Type: ipc.MsgLinkLost, Origin: dest}:
			}
			return
		}
		msg.Origin = dest
		select {
		case r.in <- msg:
		case <-stop:
			return
		}
	}
}

// Send routes on Origin. An empty Origin resolves to the active project's
// dest — NOT to local — so a missed stamp fails toward the daemon the user is
// looking at. During startup there are no projects yet, so a single-connection
// client falls back to its sole conn; that keeps remote-only mode, where no ""
// conn exists, from dropping its own first sends.
func (r *Router) Send(m *ipc.Message) error {
	dest := m.Origin
	if dest == "" {
		dest = r.currentDest()
	}

	r.mu.RLock()
	c, ok := r.conns[dest]
	if !ok && m.Origin == "" && len(r.conns) == 1 {
		for _, only := range r.conns {
			c, ok = only, true
		}
	}
	r.mu.RUnlock()

	if !ok {
		// Drop with a log. Returning an error would break resizeAllPanes and
		// sendAllLayouts mid-iteration and leave other daemons unsynced.
		log.Printf("router: dropping %s for unreachable dest %q", m.Type, dest)
		return nil
	}
	return c.Send(m)
}

func (r *Router) Receive() (*ipc.Message, error) { return <-r.in, nil }
```

In `model.go`, stamp `Origin` in both broadcast loops:

```go
for _, proj := range m.projects {
	for _, tab := range proj.tabs {
		if tab.Root == nil {
			continue
		}
		for _, pane := range tab.Leaves() {
			cols, rows := paneVTSize(pane.WideCanvas, pane.MinNativeCols, pane.Width, pane.Height, tab.CanvasW, tab.CanvasH)
			msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
				PaneID: pane.ID, Cols: uint16(cols), Rows: uint16(rows),
			})
			msg.Origin = proj.Dest
			m.client.Send(msg)
		}
	}
}
```

Same shape in `sendAllLayouts`. Dispatch `MsgLinkLost` in `listenForMessages` to a `linkLostMsg{dest: msg.Origin}`.

Add the Model-side seam that keeps the router's default in step and stamps pane-scoped sends:

```go
// destRouter is the optional capability a multi-daemon client has. Kept as a
// narrow interface so *ipc.Client and the test fake stay two-method.
type destRouter interface{ SetActiveDest(string) }

// syncActiveDest pushes the active project's dest into the router. Call it
// after ANY change to m.activeProject or m.projects — the router cannot read
// the Model, because tea.NewProgram holds its own copy.
func (m *Model) syncActiveDest() {
	if r, ok := m.client.(destRouter); ok {
		r.SetActiveDest(m.activeDest())
	}
}

// destOfPane resolves which daemon owns a pane. Falls back to the active dest
// for a pane the Model has not seen yet.
func (m *Model) destOfPane(paneID string) string {
	for _, proj := range m.projects {
		for _, tab := range proj.tabs {
			if tab.Root != nil && tab.Root.FindLeaf(paneID) != nil {
				return proj.Dest
			}
		}
	}
	return m.activeDest()
}

// sendForPane stamps a pane-scoped message with its OWNING daemon. Every send
// that names a PaneID must go through here: the active project is the wrong
// answer whenever the pane lives in a different one, and for MsgPaneInput that
// wrong answer means keystrokes on the wrong machine.
func (m *Model) sendForPane(paneID string, msg *ipc.Message) error {
	msg.Origin = m.destOfPane(paneID)
	return m.client.Send(msg)
}
```

Route every existing `m.client.Send` that carries a `PaneID` through `sendForPane` — `MsgPaneInput` above all, plus `MsgResizePane`, `MsgDestroyPane`, `MsgUpdatePane`, `MsgRestartPane`, and the scrollback/history/search requests. Tab-scoped sends (`MsgSwitchTab`, `MsgUpdateTab`, `MsgDestroyTab`, `MsgUpdateLayout`) stamp `tab.Dest` directly. Call `syncActiveDest()` at the end of `applyWorkspaceState` and in `switchProject` (Task 12).

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/router.go internal/tui/router_test.go internal/tui/model.go
git commit -m "feat(tui): route IPC across several daemons by origin"
```

---

### Task 9: Client — per-dest reconnect

**Files:** Modify `internal/tui/reconnect.go`, `internal/tui/model.go`; test `internal/tui/reconnect_test.go`

**Interfaces:**
- Produces: `Model.links map[string]*reconnectState`; `(*Model) linkFor(dest string) *reconnectState`; `Model.redialFns map[string]RedialFunc`; `(*Model) SetRedialFunc(dest string, fn RedialFunc)`.

**Instance the existing struct — do not reinvent it.** `reconnectState` (`reconnect.go:32`) already carries `active/attempt/lastErr/nextAt/lastUpAt/settledAttempt/parked`, and the flap window and slow-curve decay depend on those fields. This task keys the existing struct by dest and leaves its logic alone. Two things must additionally become per-dest: `redialFn` (today one `RedialFunc` set by `SetRedialFunc`, `reconnect.go:153`) and the banner, which renders for the **active project's** dest.

`freezeInput` must be scoped to the active project's dest, or a background daemon dropping freezes typing into local panes.

- [ ] **Step 1: Write the failing test**

```go
func TestOneDestDroppingDoesNotParkAnother(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Dest: "gpu01"}, {ID: "proj-b", Dest: "prod"},
	}}

	m.handleLinkLost("gpu01", errors.New("connection reset"))

	if !m.linkFor("gpu01").active {
		t.Fatal("gpu01 should be reconnecting")
	}
	if m.linkFor("prod").active {
		t.Fatal("prod must be unaffected by gpu01 dropping")
	}
}

func TestActiveProjectStaysPutWhenItsDaemonDrops(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-a", Dest: "gpu01"}, {ID: "proj-b", Dest: ""}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	if m.activeProject != 0 {
		t.Fatal("must not auto-switch away from the project the user is on — " +
			"stale work honestly labelled beats being teleported into different work")
	}
}

func TestBackgroundDestDropDoesNotFreezeInput(t *testing.T) {
	// freezeInput is a METHOD (reconnect.go:204), not a flag: it reports
	// whether the message should be dropped. Assert the behaviour.
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	if _, frozen := m.freezeInput(key); frozen {
		t.Fatal("a background daemon dropping must not freeze typing into local panes")
	}
}

func TestActiveDestDropDoesFreezeInput(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0,
	}
	m.handleLinkLost("gpu01", errors.New("connection reset"))

	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	if _, frozen := m.freezeInput(key); !frozen {
		t.Fatal("input to a daemon that is reconnecting must still be dropped")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.linkFor undefined`.

- [ ] **Step 3: Write minimal implementation**

Replace the single `m.reconnect reconnectState` with `m.links map[string]*reconnectState`, add:

```go
// linkFor returns the reconnect state for one destination, creating it on
// first use. reconnectState itself is unchanged — the flap window, backoff
// ladder and parked handling all still apply, now once per daemon.
func (m *Model) linkFor(dest string) *reconnectState {
	if m.links == nil {
		m.links = map[string]*reconnectState{}
	}
	ls, ok := m.links[dest]
	if !ok {
		ls = &reconnectState{}
		m.links[dest] = ls
	}
	return ls
}

// activeDest is the destination of the project the user is looking at. It
// drives the banner, freezeInput, and the router's empty-Origin fallback.
func (m *Model) activeDest() string {
	if p := m.cur(); p != nil {
		return p.Dest
	}
	return ""
}

// handleLinkLost marks ONE destination as reconnecting. It deliberately does
// not move the active project: the client stays put and renders the parked
// project with its last content.
func (m *Model) handleLinkLost(dest string, err error) {
	ls := m.linkFor(dest)
	ls.active = true
	ls.lastErr = err
}
```

`freezeInput` (`reconnect.go:204`) is an existing **method** that reports whether a message should be dropped. Change its gate from the singleton state to the active destination only:

```go
// Only the daemon the user is currently typing into can freeze input. A
// background project's daemon dropping must not stop typing into local panes.
if ls, ok := m.links[m.activeDest()]; !ok || !ls.active {
	return nil, false
}
```

Rewrite every other `m.reconnect.X` reference as `m.linkFor(dest).X`, threading dest from `linkLostMsg`. Change `SetRedialFunc` to take a dest and store into `m.redialFns`. `canReconnect` becomes `canReconnect(dest)`, checking `m.redialFns[dest] != nil`. The banner and resume key act on `m.activeDest()`.

**Hand the fresh conn back to the router.** `finishReconnect` today swaps `m.client` wholesale and bumps `clientGen`. With a router there is no single client to swap — the successful redial replaces one entry:

```go
if r, ok := m.client.(*Router); ok {
	r.Remove(dest)
	r.Add(dest, fresh)
} else {
	m.client = fresh // single-daemon path, unchanged
}
m.clientGen++
```

`Remove` cannot interrupt a pump already parked inside `Receive()` — `Client` has no Close — so route the release through the existing `closeClientFn` (`model.go:238`) when one is set, exactly as the single-client path does today.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race`
Expected: PASS, including the existing reconnect suite with dests threaded through.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/reconnect.go internal/tui/reconnect_test.go internal/tui/model.go internal/tui/reconnect_test.go
git commit -m "feat(tui): track reconnect state per destination"
```

---

### Task 10: `WorkEventPark` — blocked is not done

**Files:** Modify `internal/hookevents/workstate.go`, `internal/tui/workstate.go`, `internal/tui/pane.go`; test `internal/hookevents/workstate_test.go`

**Interfaces:** Produces `hookevents.WorkEventPark`; `PaneModel.blockedSince time.Time`, `PaneModel.blockedReason string` (unexported, matching `working`/`unseen`/`subagents`).

- [ ] **Step 1: Write the failing test**

```go
func TestParkEventsAreDistinctFromStop(t *testing.T) {
	for _, evt := range []string{
		"hook.claude.Notification",
		"hook.claude.PermissionRequest",
		"hook.opencode.permission.ask",
	} {
		if got := ClassifyWorkEvent(evt); got != WorkEventPark {
			t.Errorf("ClassifyWorkEvent(%q) = %v, want WorkEventPark", evt, got)
		}
	}
	if got := ClassifyWorkEvent("hook.claude.Stop"); got != WorkEventStop {
		t.Errorf("a turn completing is Stop, not Park: got %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `WorkEventPark undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// WorkEventPark: the agent is blocked waiting on the USER — a permission
// prompt or an idle wait. Distinct from WorkEventStop, which means the turn
// finished. Both clear the spinner and mark the pane unseen; only Park means
// "this needs you", which is what the sidebar's ⚠ renders.
WorkEventPark
```

Split the arm at `hookevents/workstate.go:66` to return `WorkEventPark`.

In `internal/tui/workstate.go`, add `workPark = hookevents.WorkEventPark` to the alias block. Handle it exactly as `workStop` for the derived `working` recomputation and the unseen mark, plus:

```go
case workPark:
	pane.blockedSince = time.Now()
	// Data["tool"] is set by the claude hook only for PermissionRequest and
	// PostToolUse. Notification and opencode's permission.ask may carry no
	// tool, so the reason is genuinely optional — the sidebar renders a bare
	// ⚠ rather than inventing one.
	pane.blockedReason = data["tool"]
```

Clear both on `workStart`, `workAbort`, `workStopFinal`, **and plain `workStop`**. The last one is not obvious and matters: approving a permission prompt fires no hook of its own, so the pane's next event is the turn's `hook.claude.Stop`. If `workStop` does not clear, the sidebar keeps showing ⚠ on a pane that has finished its turn, until the user submits another prompt. A completed turn is by definition not blocked.

Add the regression test for exactly that sequence:

```go
func TestTurnCompletionClearsTheBlockedMark(t *testing.T) {
	pane := &PaneModel{ID: "pane-1"}
	tab := NewTabModel("tab-1", "AI")
	tab.Root = NewLeaf(pane)
	m := Model{projects: []*ProjectModel{{ID: "proj-a", tabs: []*TabModel{tab}}}}

	m.applyWorkTransition("pane-1", "hook.claude.PermissionRequest", map[string]string{"tool": "Bash"})
	if pane.blockedSince.IsZero() {
		t.Fatal("a permission request must mark the pane blocked")
	}

	// Approving fires no hook; the next event is the turn completing.
	m.applyWorkTransition("pane-1", "hook.claude.Stop", nil)

	if !pane.blockedSince.IsZero() {
		t.Fatal("a completed turn must clear the blocked mark — otherwise ⚠ " +
			"sticks on a pane that is done")
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS, with every existing workstate test still green — Park and Stop share spinner/unseen behaviour, so nothing observable changed for them.

- [ ] **Step 5: Commit**

```bash
git add internal/hookevents/workstate.go internal/hookevents/workstate_test.go internal/tui/workstate.go internal/tui/pane.go
git commit -m "feat(hookevents): distinguish blocked-on-user from turn complete"
```

---

### Task 11: Sidebar render

**Files:** Create `internal/tui/sidebar.go`, `internal/tui/sidebar_test.go`; modify `internal/tui/model.go`, `internal/config/config.go`

**Interfaces:**
- Consumes: Tasks 5, 9 (`linkFor`), 10.
- Produces: `(*Model) renderSidebar(height int) string`; `sidebarWidth(total int, open bool, configured int) int`; `(*Model) linkGlyph(dest string) string`; `(*ProjectModel) counts() (working, blocked int)`; `(*ProjectModel) displayName() string`; `UIConfig.SidebarWidth int`, `UIConfig.SidebarOpen bool`.

**The width must be subtracted in the layout path**, not only in `View()` — the tab canvas and PTY geometry are computed there, and painting a narrower region than the panes were sized for makes rects and PTY sizes disagree.

- [ ] **Step 1: Write the failing test**

```go
func TestSidebarShowsProjectCountsAndBlockedReason(t *testing.T) {
	working := &PaneModel{ID: "pane-1"}
	working.working = true
	blocked := &PaneModel{ID: "pane-2"}
	blocked.blockedSince = time.Now()
	blocked.blockedReason = "Bash"

	tab := NewTabModel("tab-1", "AI")
	tab.Root = &LayoutNode{Left: NewLeaf(working), Right: NewLeaf(blocked), Ratio: 0.5}

	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "quil", tabs: []*TabModel{tab}}},
		sidebarOpen:  true,
		sidebarWidth: 22,
	}

	out := m.renderSidebar(20)
	if !strings.Contains(out, "quil") {
		t.Fatal("project name missing")
	}
	if !strings.Contains(out, "Bash") {
		t.Fatal("blocked reason missing — a bare ⚠ does not say what it wants")
	}
}

func TestSidebarSanitizesRemoteStrings(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "evil‮coffee", Dest: "gpu01"}},
		sidebarOpen:  true,
		sidebarWidth: 22,
	}
	if strings.ContainsRune(m.renderSidebar(10), '‮') {
		t.Fatal("a bidi override from a remote host reached the screen")
	}
}

func TestSidebarWidthZeroWhenClosedOrNarrow(t *testing.T) {
	if got := sidebarWidth(200, false, 22); got != 0 {
		t.Fatalf("closed = %d, want 0", got)
	}
	if got := sidebarWidth(60, true, 22); got != 0 {
		t.Fatalf("narrow terminal must auto-collapse, got %d", got)
	}
	if got := sidebarWidth(200, true, 22); got != 22 {
		t.Fatalf("width = %d, want 22", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.renderSidebar undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
const (
	// minWidthForSidebar auto-collapses rather than squeezing panes into
	// unusability on a narrow terminal.
	minWidthForSidebar  = 100
	defaultSidebarWidth = 22
)

func sidebarWidth(total int, open bool, configured int) int {
	if !open || total < minWidthForSidebar {
		return 0
	}
	if configured <= 0 {
		configured = defaultSidebarWidth
	}
	return configured
}

// displayName appends the destination for a remote project so two projects
// called "api" on different hosts stay distinguishable.
func (p *ProjectModel) displayName() string {
	if p.Dest == "" {
		return p.Name
	}
	return p.Name + "@" + p.Dest
}

// counts reports panes working and panes blocked on the user, for the
// project's summary row.
func (p *ProjectModel) counts() (working, blocked int) {
	for _, tab := range p.tabs {
		if tab.Root == nil {
			continue
		}
		for _, pane := range tab.Leaves() {
			if pane.working {
				working++
			}
			if !pane.blockedSince.IsZero() {
				blocked++
			}
		}
	}
	return working, blocked
}

// linkGlyph reports the connection health of a destination: ⟳ reconnecting,
// ⚡ parked, empty when healthy.
func (m *Model) linkGlyph(dest string) string {
	ls, ok := m.links[dest]
	switch {
	case !ok:
		return ""
	case ls.parked:
		return "⚡"
	case ls.active:
		return "⟳"
	}
	return ""
}

func (m *Model) renderSidebar(height int) string {
	var b strings.Builder
	w := m.sidebarWidth

	b.WriteString(sidebarHeading("PROJECTS", w))
	for i, p := range m.projects {
		working, blocked := p.counts()
		b.WriteString(projectRow(sanitizeRemoteText(p.displayName()),
			working, blocked, m.linkGlyph(p.Dest), i == m.activeProject, w))
	}

	b.WriteString(sidebarHeading("PANES", w))
	for _, tab := range m.curTabs() {
		b.WriteString(sidebarTabHeading(sanitizeRemoteText(tab.Name), w))
		for _, pane := range tab.Leaves() {
			b.WriteString(paneRow(pane, w))
		}
	}
	return b.String()
}
```

`paneRow` renders the spec's glyphs: `◐` working with `⋯N` subagents, `⚠` plus `blockedReason` when present, `○` idle, `✓` unseen, `✗` exited nonzero. Every remote-sourced string goes through `sanitizeRemoteText`.

Add `SidebarWidth`/`SidebarOpen` to `UIConfig` — **client config, not `workspace.json`**: the sidebar belongs to the screen, not the session. Subtract the width in the layout/`WindowSizeMsg` path so tab canvas and PTY sizes agree, then join horizontally in `View()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_test.go internal/tui/model.go internal/config/config.go
git commit -m "feat(tui): add project sidebar with agent state"
```

---

### Task 12: Sidebar interaction and project switching

**Files:** Modify `internal/tui/sidebar.go`, `internal/tui/model.go`, `internal/tui/keymatch.go`, `internal/config/config.go`; test `internal/tui/sidebar_test.go`

**Interfaces:**
- Produces: `(*Model) sidebarHit(x, y int) (kind string, index int)` (`"project"` / `"pane"` / `""`); `(*Model) switchProject(i int) tea.Cmd`; test helpers `tabWith(panes ...*PaneModel) *TabModel` and `tabWithPane(tabID, paneID string) *TabModel` defined in `sidebar_test.go` and reused by Task 15.

`switchProject` **must tell the daemon**. Without `MsgSwitchProject` the daemon's `activeProject` goes stale and `CreateTab` (which delegates with the active project) files every new Ctrl+T tab into whatever project was active at daemon start.

- [ ] **Step 1: Write the failing test**

```go
func tabWith(panes ...*PaneModel) *TabModel {
	t := NewTabModel("tab-"+panes[0].ID, "T")
	node := NewLeaf(panes[0])
	for _, p := range panes[1:] {
		node = &LayoutNode{Left: node, Right: NewLeaf(p), Ratio: 0.5}
	}
	t.Root = node
	t.ActivePane = panes[0].ID
	return t
}

func tabWithPane(tabID, paneID string) *TabModel {
	t := NewTabModel(tabID, "T")
	t.Root = NewLeaf(&PaneModel{ID: paneID})
	t.ActivePane = paneID
	return t
}

func TestClickingProjectRowSwitchesProject(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "alpha"}, {ID: "proj-b", Name: "beta"}},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
	}
	// Row mapping: View() draws the full-width tab bar at screen row 0 and
	// joins the sidebar BELOW it, and renderSidebar's own row 0 is the
	// "PROJECTS" heading — so project i sits at screen row i+2.
	kind, idx := m.sidebarHit(3, 3) // second project row, under the heading
	if kind != "project" || idx != 1 {
		t.Fatalf("sidebarHit = (%q, %d), want (project, 1)", kind, idx)
	}
}

func TestClickBeyondSidebarIsNotSwallowed(t *testing.T) {
	m := Model{sidebarOpen: true, sidebarWidth: 22, width: 200, height: 40}
	if kind, _ := m.sidebarHit(40, 5); kind != "" {
		t.Fatalf("a click in the pane region must not be claimed by the sidebar, got %q", kind)
	}
}

func TestSwitchProjectNotifiesDaemonAndResyncsGeometry(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client: fake,
		projects: []*ProjectModel{
			{ID: "proj-a", Dest: ""},
			{ID: "proj-b", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-9")}},
		},
		activeProject: 0,
	}

	if cmd := m.switchProject(1); cmd != nil {
		cmd()
	}

	var sawSwitch bool
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchProject {
			sawSwitch = true
			if msg.Origin != "gpu01" {
				t.Fatalf("switch Origin = %q, want gpu01", msg.Origin)
			}
		}
	}
	if !sawSwitch {
		t.Fatal("switchProject must send MsgSwitchProject or the daemon's activeProject goes stale")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.sidebarHit undefined`.

- [ ] **Step 3: Write minimal implementation**

**Before adding any keybinding, audit the X-origin. This is the load-bearing part of this task.** Task 11 reserved the sidebar's width in the layout path, so panes are now *narrower* — but every pane-rect function still assumes panes begin at screen column 0. With the sidebar open they actually begin at column `sidebarWidth`. Until that offset is applied, clicks, drag-selection, split-border drag, wheel forwarding and scrollbar hit-testing are all off by the sidebar's width the moment the sidebar can be opened — and this task is what makes it openable, so the latent bug becomes a live one here.

**Also route `renderSidebar`'s own width through `projectSidebarWidth()`.** It currently sizes its box off the raw `m.sidebarWidth` field (`sidebar.go:92`), bypassing the clamp that `sidebarWidth()` applies. With an oversized `sidebar_width` in config the pane area clamps correctly but the sidebar box does not, so `lipgloss.JoinHorizontal` composites a frame wider than the terminal. Inert only because nothing can open the sidebar yet — this task removes that protection.

Task 11 already corrected the four functions that compute rects by mirroring `View()`'s width math — `activePaneRectFocus`, `activePaneRect`, `paneRectAt`, `hitTestScrollbar` — plus `attachMessage`'s spawn-size hint. Those now subtract the width. What remains is the **origin**: add the sidebar offset to the X coordinate wherever a screen column is converted into a pane-local column, and subtract it wherever a pane-local column is converted back. Grep for every consumer of those rect functions rather than assuming the list is closed.

`sidebarHit` maps y to a row and returns `""` for any x at or beyond the sidebar width. Wire it into the mouse dispatch **after** the context-menu and notification-sidebar checks and **before** the pane region, matching the input-priority-must-match-paint-priority rule in `model.go`.

```go
// switchProject moves the client to project i, tells the owning daemon so its
// activeProject stays in step (new tabs are filed against it), and resyncs the
// incoming project's PTY geometry, which was last sized under whatever
// geometry was current when it went to the background.
func (m *Model) switchProject(i int) tea.Cmd {
	if i < 0 || i >= len(m.projects) || i == m.activeProject {
		return nil
	}
	m.prevProject = m.activeProject
	m.activeProject = i
	p := m.projects[i]

	msg, _ := ipc.NewMessage(ipc.MsgSwitchProject, ipc.SwitchProjectPayload{ProjectID: p.ID})
	msg.Origin = p.Dest
	m.client.Send(msg)

	return m.resizeAllPanes()
}
```

Bind `alt+shift+s` to toggle `m.sidebarOpen`; the handler persists via `config.Save` **and** returns `m.resizeAllPanes()` so panes reflow. Add `SidebarToggle`, `ProjectPicker`, `ProjectToggle`, `AttentionQueue`, `NewProject` to the keybindings struct with the verified-free defaults from the header table.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/model.go internal/tui/keymatch.go internal/config/config.go internal/tui/sidebar_test.go
git commit -m "feat(tui): make the sidebar clickable and collapsible"
```

---

### Task 13: Project CRUD UI

**Files:** Create `internal/tui/projectdialog.go`, `internal/tui/projectdialog_test.go`; modify `internal/tui/model.go`, `internal/tui/keymatch.go`, `internal/tui/ctxmenu.go`

**Interfaces:**
- Consumes: Tasks 1, 12.
- Produces: `dialogProjectNew`, `dialogProjectRename` dialog constants; `(*Model) submitNewProject(name, rootDir string) tea.Cmd`; `(*Model) submitRenameProject(id, name, rootDir string) tea.Cmd`; `(*Model) confirmDestroyProject(id string) tea.Cmd`.

Without this task nothing in the product can create a project — the spec's core decision is that the user makes them explicitly. `alt+shift+n` opens the dialog; rename and destroy hang off the sidebar context menu.

- [ ] **Step 1: Write the failing test**

```go
func TestSubmitNewProjectSendsCreateToActiveDest(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client:        fake,
		projects:      []*ProjectModel{{ID: "proj-a", Dest: "gpu01"}},
		activeProject: 0,
	}

	if cmd := m.submitNewProject("beta", "/src/beta"); cmd != nil {
		cmd()
	}

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	got := fake.sent[0]
	if got.Type != ipc.MsgCreateProject {
		t.Fatalf("type = %s, want %s", got.Type, ipc.MsgCreateProject)
	}
	if got.Origin != "gpu01" {
		t.Fatalf("Origin = %q, want gpu01 — a new project belongs to the daemon "+
			"whose filesystem holds its root dir", got.Origin)
	}
	var payload ipc.CreateProjectPayload
	got.DecodePayload(&payload)
	if payload.Name != "beta" || payload.RootDir != "/src/beta" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestSubmitNewProjectRejectsEmptyName(t *testing.T) {
	fake := newFakeConn()
	m := Model{client: fake, projects: []*ProjectModel{{ID: "proj-a"}}}

	if cmd := m.submitNewProject("  ", "/src"); cmd != nil {
		cmd()
	}
	if len(fake.sent) != 0 {
		t.Fatal("an unnamed project must not be created")
	}
}

func TestDestroyProjectIsConfirmedNotImmediate(t *testing.T) {
	fake := newFakeConn()
	m := Model{client: fake, projects: []*ProjectModel{{ID: "proj-a", Name: "alpha"}}}

	m.confirmDestroyProject("proj-a")

	if len(fake.sent) != 0 {
		t.Fatal("destroy takes every tab and pane with it — it must confirm first")
	}
	if m.dialog != dialogConfirm {
		t.Fatalf("dialog = %v, want a confirm dialog", m.dialog)
	}
	if m.confirmID != "proj-a" || m.confirmKind != confirmKindDestroyProject {
		t.Fatalf("confirm = (%q, %q), want the project kind and its ID",
			m.confirmKind, m.confirmID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.submitNewProject undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// submitNewProject creates a project on the ACTIVE project's daemon. A project
// is a name plus a root directory, and a root directory lives on exactly one
// machine — so the new project belongs wherever the user is currently working.
func (m *Model) submitNewProject(name, rootDir string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	msg, _ := ipc.NewMessage(ipc.MsgCreateProject, ipc.CreateProjectPayload{
		Name: name, RootDir: strings.TrimSpace(rootDir),
	})
	msg.Origin = m.activeDest()
	m.client.Send(msg)
	m.dialog = dialogNone
	return nil
}

func (m *Model) submitRenameProject(id, name, rootDir string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	msg, _ := ipc.NewMessage(ipc.MsgUpdateProject, ipc.UpdateProjectPayload{
		ProjectID: id, Name: name, RootDir: strings.TrimSpace(rootDir),
	})
	msg.Origin = m.destOfProject(id)
	m.client.Send(msg)
	m.dialog = dialogNone
	return nil
}

// confirmDestroyProject opens the shared confirm dialog. Destroying a project
// takes every tab and pane under it, so it never fires straight off a
// keystroke.
//
// This follows the EXISTING confirm convention (model.go:256-258, pattern at
// openClosePaneConfirm, model.go:2060): set dialog + confirmKind/ID/Name
// fields, and let the confirm handler dispatch on confirmKind. There are no
// callback fields on Model — do not introduce them.
func (m *Model) confirmDestroyProject(id string) tea.Cmd {
	p := m.projectByID(id)
	if p == nil {
		return nil
	}
	m.dialog = dialogConfirm
	m.confirmKind = confirmKindDestroyProject
	m.confirmID = id
	m.confirmName = p.Name
	return nil
}
```

Existing kinds for reference: `"pane"`, `"tab"`, `"instance"`, `confirmKindShutdown`, `confirmKindRestartPane`, `confirmKindApplyUpdate`.

```go
```

Add `const confirmKindDestroyProject = "destroy-project"` beside the existing kind constants (`dialog.go:270-284`, alongside `confirmKindShutdown`, `confirmKindRestartPane`, `confirmKindApplyUpdate`). The accept path is an **if-chain on `m.confirmKind`**, not a switch (`dialog.go:613-643`) — add a branch there matching the surrounding style:

```go
msg, _ := ipc.NewMessage(ipc.MsgDestroyProject, ipc.DestroyProjectPayload{ProjectID: m.confirmID})
msg.Origin = m.destOfProject(m.confirmID)
m.client.Send(msg)
```

Add `projectByID(id) *ProjectModel` and `destOfProject(id) string` helpers. The new/rename dialog is a two-field form (name, root dir) using the existing dialog chrome; the root-dir field offers the daemon-side browser via the existing `MsgBrowseDirReq` path, because the root lives on the daemon's filesystem. Bind `alt+shift+n`. Add Rename and Destroy entries to the sidebar context menu.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/projectdialog.go internal/tui/projectdialog_test.go internal/tui/model.go internal/tui/keymatch.go internal/tui/ctxmenu.go
git commit -m "feat(tui): add project create, rename, and destroy UI"
```

---

### Task 14: Project picker and last-project toggle

**Files:** Create `internal/tui/projectpicker.go`, `internal/tui/projectpicker_test.go`; modify `internal/tui/model.go`, `internal/tui/keymatch.go`

**Interfaces:** Produces `dialogProjectPick`; `Model.prevProject int`; `(*Model) filterProjects(query string) []*ProjectModel`; `(*Model) toggleLastProject() tea.Cmd`.

- [ ] **Step 1: Write the failing test**

```go
func TestProjectPickerFiltersFuzzily(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Name: "quil"},
		{ID: "proj-b", Name: "quil-docs"},
		{ID: "proj-c", Name: "unrelated"},
	}}
	got := m.filterProjects("qd")
	if len(got) != 1 || got[0].Name != "quil-docs" {
		t.Fatalf("filterProjects(qd) = %v, want [quil-docs]", got)
	}
}

func TestLastProjectToggleReturnsAndBounces(t *testing.T) {
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{{ID: "proj-a"}, {ID: "proj-b"}, {ID: "proj-c"}},
		activeProject: 0,
	}
	m.switchProject(2)
	m.toggleLastProject()
	if m.activeProject != 0 {
		t.Fatalf("toggle should return to 0, got %d", m.activeProject)
	}
	m.toggleLastProject()
	if m.activeProject != 2 {
		t.Fatalf("toggle should bounce back to 2, got %d", m.activeProject)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.filterProjects undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// filterProjects ranks projects with fuzzyScore — the same matcher the command
// palette uses (palette.go:109), so there is one ranking behaviour rather than
// two that drift apart.
func (m *Model) filterProjects(query string) []*ProjectModel {
	if query == "" {
		return m.projects
	}
	type scored struct {
		p     *ProjectModel
		score int
	}
	var hits []scored
	for _, p := range m.projects {
		if score, ok := fuzzyScore(query, p.displayName()); ok {
			hits = append(hits, scored{p, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })

	out := make([]*ProjectModel, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.p)
	}
	return out
}

// toggleLastProject bounces between the two most recent projects, the way
// `sesh pop` does for tmux sessions. switchProject records prevProject.
func (m *Model) toggleLastProject() tea.Cmd {
	if m.prevProject < 0 || m.prevProject >= len(m.projects) {
		return nil
	}
	return m.switchProject(m.prevProject)
}
```

Add `dialogProjectPick` to the dialog enum, render with existing dialog chrome, bind `alt+p` and `alt+o`.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/projectpicker.go internal/tui/projectpicker_test.go internal/tui/model.go internal/tui/keymatch.go
git commit -m "feat(tui): add project picker and last-project toggle"
```

---

### Task 15: Attention queue

**Files:** Create `internal/tui/attention.go`, `internal/tui/attention_test.go`; modify `internal/tui/keymatch.go`

**Interfaces:** Produces `blockedRef{Pane *PaneModel; Project *ProjectModel; TabIndex int}`; `(*Model) blockedPanes() []blockedRef`; `(*Model) jumpToNextBlocked() tea.Cmd`.

- [ ] **Step 1: Write the failing test**

```go
func TestBlockedPanesOrderedOldestFirstAcrossProjects(t *testing.T) {
	now := time.Now()
	recent := &PaneModel{ID: "pane-recent"}
	recent.blockedSince = now.Add(-1 * time.Minute)
	oldest := &PaneModel{ID: "pane-oldest"}
	oldest.blockedSince = now.Add(-9 * time.Minute)
	idle := &PaneModel{ID: "pane-idle"}

	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", tabs: []*TabModel{tabWith(recent), tabWith(idle)}},
		{ID: "proj-b", tabs: []*TabModel{tabWith(oldest)}},
	}}

	got := m.blockedPanes()
	if len(got) != 2 {
		t.Fatalf("blocked = %d, want 2 (the idle pane must not appear)", len(got))
	}
	if got[0].Pane.ID != "pane-oldest" {
		t.Fatalf("first = %s, want pane-oldest — order is blocked-longest-first",
			got[0].Pane.ID)
	}
}

func TestJumpToNextBlockedSwitchesProject(t *testing.T) {
	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()

	m := Model{
		client: newFakeConn(),
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-idle"})}},
			{ID: "proj-b", tabs: []*TabModel{tabWith(blocked)}},
		},
		activeProject: 0,
	}

	m.jumpToNextBlocked()

	if m.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 — the queue must cross project boundaries",
			m.activeProject)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `m.blockedPanes undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
)

type blockedRef struct {
	Pane     *PaneModel
	Project  *ProjectModel
	TabIndex int
}

// blockedPanes collects every pane waiting on the user across ALL projects,
// oldest-blocked first. Deliberately not sidebar order: with six agents
// running, the one waiting longest is the one costing you time.
func (m *Model) blockedPanes() []blockedRef {
	var out []blockedRef
	for _, proj := range m.projects {
		for i, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			for _, pane := range tab.Leaves() {
				if pane.blockedSince.IsZero() {
					continue
				}
				out = append(out, blockedRef{Pane: pane, Project: proj, TabIndex: i})
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].Pane.blockedSince.Before(out[b].Pane.blockedSince)
	})
	return out
}

// jumpToNextBlocked moves project, tab, and focus in one keystroke.
func (m *Model) jumpToNextBlocked() tea.Cmd {
	blocked := m.blockedPanes()
	if len(blocked) == 0 {
		return nil
	}

	// Advance past the pane we are already on so repeated presses cycle.
	target := blocked[0]
	if cur := m.activeTabModel(); cur != nil {
		if active := cur.ActivePaneModel(); active != nil {
			for i, ref := range blocked {
				if ref.Pane.ID == active.ID {
					target = blocked[(i+1)%len(blocked)]
					break
				}
			}
		}
	}

	for i, p := range m.projects {
		if p == target.Project {
			cmd := m.switchProject(i)
			target.Project.activeTab = target.TabIndex
			// TabModel.ActivePane is the pane-ID field; ActivePaneModel()
			// repairs a stale value on next read, so assigning it is the
			// whole focus change.
			target.Project.tabs[target.TabIndex].ActivePane = target.Pane.ID
			return cmd
		}
	}
	return nil
}
```

Bind `alt+shift+a`.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/attention.go internal/tui/attention_test.go internal/tui/keymatch.go
git commit -m "feat(tui): add oldest-first attention queue across projects"
```

---

### Task 16: `remoteDest` → per-project dest

**Files:** Modify `internal/tui/model.go`, `internal/tui/dialog.go`, `internal/tui/plugins_client.go`, `internal/tui/migration.go`, `internal/tui/update.go`; test `internal/tui/remote_test.go`

**Interfaces:** Consumes Task 9's `activeDest()`. Produces `(*Model) remoteModeFor(dest string) bool`; `RemoteMode()` redefined as `m.activeDest() != ""`.

Spec §2 calls for this and no earlier task covers it. The single `m.remoteDest` describes one daemon; with several, each of these sites must ask about the relevant one.

- [ ] **Step 1: Write the failing test**

```go
func TestRemoteModeFollowsActiveProject(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"},
	}}

	m.activeProject = 0
	if m.RemoteMode() {
		t.Fatal("a local project must not report remote mode")
	}
	m.activeProject = 1
	if !m.RemoteMode() {
		t.Fatal("a project on gpu01 must report remote mode")
	}
}

func TestStatusBarNamesTheActiveProjectsHost(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0, width: 120,
	}
	if !strings.Contains(m.renderStatusBar(), "gpu01") {
		t.Fatal("the status bar must name the host the user is actually on")
	}
}

func TestAttachCWDIsEmptyForRemoteProject(t *testing.T) {
	if got := attachCWD("gpu01", "/home/me/src"); got != "" {
		t.Fatalf("attachCWD = %q, want empty — the laptop's path is not the "+
			"remote machine's, and defaultCWD() falls back safely", got)
	}
}
```

The status bar is built by `renderStatusBar()` (`model.go:3867`); the `[remote …]` segment it currently derives from `m.remoteDest` sits around `model.go:3943`.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `RemoteMode` still reads the removed `m.remoteDest`.

- [ ] **Step 3: Write minimal implementation**

Delete `Model.remoteDest` and `SetRemoteDest`. Redefine:

```go
// RemoteMode reports whether the project the user is looking at lives on
// another machine. It is per-project now: one client can hold local and remote
// projects at once, so "is this remote" has no process-wide answer.
func (m *Model) RemoteMode() bool { return m.activeDest() != "" }

func (m *Model) remoteModeFor(dest string) bool { return dest != "" }
```

Update each site: the `[remote …]` status segment uses `m.activeDest()`; `attachCWD` is called per-dest at attach time (Task 17); the plugin-availability gate in `plugins_client.go` and `migration.go` and the two dialog buttons use `remoteModeFor(dest)` for the daemon they are about; `canReconnect` already took a dest in Task 9. `SetRecentCWDs` keys off `m.activeDest()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/dialog.go internal/tui/plugins_client.go internal/tui/migration.go internal/tui/update.go internal/tui/remote_test.go
git commit -m "refactor(tui): derive remote mode from the active project"
```

---

### Task 17: Multi-daemon wiring and end-to-end verification

**Files:** Modify `cmd/quil/main.go`, `internal/tui/model.go` (`NewModel`), `internal/config/config.go`; test `cmd/quil/remote_test.go`

**Interfaces:**
- Consumes: Tasks 8, 9, 16.
- Produces: `NewModel(client tui.Client, ...)` — signature widened from `*ipc.Client` to the `Client` interface so a `*Router` can be passed; `[[destinations]]` config (`name`, `dest`); `dialAll(cfg config.Config) map[string]tui.Client`.

### Blockers carried into this task from Tasks 8 and 9

These were deliberately left open because closing them earlier would have changed a contract the earlier task owned. **All six must be closed here**; the first four are load-bearing for the feature working at all.

1. **The listen loop must re-arm on `linkLostMsg`, and the quit decision must count daemons.** Today `Update` quits when `!canReconnect(msg.dest)`, and the loop is not re-armed — so with a Router, one BACKGROUND daemon dying takes the whole session down, and while any ladder is climbing the other daemons' messages sit unread in `r.in`. Distinguish "the only daemon" from "one of several".

2. **The startup attach never reaches a second daemon.** `Router.Send`'s sole-conn fallback is gated on `len(r.conns) == 1`. A router holding local + remote therefore delivers the deliberately-unstamped startup attach only to `conns[""]`, and the remote is never attached — no workspace state, no projects, feature silently dead. `attachAllDests` (below) replaces that path; make sure nothing still relies on the fallback for attach.

3. **Dead connections are never released (resource leak).** `Router.pump` retires its registration BEFORE publishing the loss, so by redial time `Conn(dest)` is nil and nothing closes the dead ssh child or its remote `quil --stdio` — one leak per reconnect, in a repo that has already paid for leaked Windows children. `CloseClient()` compounds it: it hands the `*Router` to `closeClientFn`, whose assertion to `*ipc.Client` misses, so exit closes no per-dest conn either. Fix requires changing Task 8's `retire`/`Add` liveness key (e.g. key liveness off `r.stop[dest]` and leave the conn reachable) — that is why it waited for this task.

4. **`clientGen` is global and will kill a second ladder.** `finishReconnect` bumps it for whatever dest completed, so once two ladders can climb at once (which is exactly what blocker 1 enables), the other dest's already-armed `redialTickMsg{gen: old}` and in-flight `redialResultMsg{gen: old}` are dropped, its `active` stays `true` forever with no timer, and its banner sticks. Make the generation per-dest, or skip the gen check when `msg.dest` names a different destination.

5. **`canReconnect` still requires `RemoteMode()`.** In a mixed session launched WITHOUT `--remote`, `remoteDest == ""`, so every remote dest's drop is fatal. `redialFns[dest] != nil` is already sufficient — drop the `RemoteMode()` conjunct.

6. **Delete `Model.remoteDest` and collapse `RemoteMode()` to `activeDest() != ""`.** Task 16 could not retire the field: until this task constructs the router, nothing stamps `Dest` on a project for a `--remote` session, so `remoteDest` is still the only source of truth for "this session is remote" — deleting it there would have forced either breaking `--remote` or rekeying the reconnect link table that this task reworks anyway. Task 16 therefore left `RemoteMode()` as the union `activeDest() != "" || m.remoteDest != ""`, which has a **known expiry**: in a mixed session (a LOCAL project active while `remoteDest` is set) it answers `true` for a project that is local. That state is unreachable today and becomes reachable the moment this task builds a multi-conn router. Deleting the field is what collapses the union back to correct. `SetRemoteDest` has ~54 call sites across five test files, most of them in `reconnect_test.go`, and roughly 15 depend on `oneLink` keying reconnect state by `""` — budget for that rekeying as part of this task, not as an afterthought.

7. **`linkHost("")` names the wrong host in a mixed session.** It falls back to `m.remoteDest`, which is correct only while a single conn's `""` IS the remote. With local + `--remote gpu01`, a loss on the LOCAL daemon renders a banner naming `gpu01`.

Two smaller items to fix while here: `requestPluginList` (`plugins_client.go:49`) sends unstamped, so a background daemon's reconnect asks the FOREGROUND daemon for its plugin list; and `destOfPane` (`router.go`) dereferences `proj.tabs`/`tab.Root` unguarded while `eachClientPane` twelve lines away nil-checks both — and Task 9 put it on the outage path, the worst moment for a panic.

Finally, refresh `.claude/rules/remote-transport.md`: it still documents `canReconnect() = RemoteMode() && redialFn != nil`, `resetPanesForReattach`, and one reconnect state per session. That file is agent context, so leaving it stale actively misdirects the next implementer.

**Attach stays with the Model, and becomes per-dest.** Attaching at dial time is wrong twice over: `AttachPayload` carries `Cols`/`Rows` which `handleAttach` uses to size the first PTY (`daemon.go:992`), and at dial time Bubble Tea does not yet know the terminal size — a fresh daemon would spawn its default Shell pane at 0×0. And attach already has two owners (`m.attachToDaemon()` on first `WindowSizeMsg` at `model.go:623`, and the reattach after redial at `reconnect.go:625`); adding a third would attach every conn twice, and each attach replays the full ghost buffer — doubled scrollback plus double-counted work-state events, the replay hazard documented at `workstate.go:76`.

So: `dialAll` only dials and version-gates. `attachToDaemon(dest)` gains a dest parameter, stamps `Origin`, and is tracked by a per-dest `attached` flag; the Model attaches every not-yet-attached dest on `WindowSizeMsg`, which is also when a dest added later by reconnect gets picked up.

- [ ] **Step 1: Write the failing test**

`cmd/quil` is package `main` and cannot import `internal/tui`'s test helpers — Go does not export test code across packages. Define a local fake in `cmd/quil/remote_test.go`:

```go
type stubClient struct{ sent []*ipc.Message }

func (s *stubClient) Send(m *ipc.Message) error      { s.sent = append(s.sent, m); return nil }
func (s *stubClient) Receive() (*ipc.Message, error) { select {} }

func TestDialAllKeepsGoingWhenOneDestFails(t *testing.T) {
	dials := map[string]func() (tui.Client, error){
		"":      func() (tui.Client, error) { return &stubClient{}, nil },
		"gpu01": func() (tui.Client, error) { return nil, errors.New("ssh: connection refused") },
	}

	conns := dialAllWith(dials)

	if len(conns) != 1 {
		t.Fatalf("conns = %d, want 1 — an unreachable host must not block startup", len(conns))
	}
	if _, ok := conns[""]; !ok {
		t.Fatal("the local daemon should still be connected")
	}
}

func TestDialAllDoesNotAttach(t *testing.T) {
	local := &stubClient{}
	dialAllWith(map[string]func() (tui.Client, error){
		"": func() (tui.Client, error) { return local, nil },
	})

	for _, msg := range local.sent {
		if msg.Type == ipc.MsgAttach {
			t.Fatal("attach must NOT happen at dial time: AttachPayload carries " +
				"Cols/Rows and the terminal size is unknown until WindowSizeMsg, " +
				"and the Model already owns attach")
		}
	}
}
```

And in `internal/tui`, the per-dest attach behaviour:

```go
func TestEveryConnIsAttachedExactlyOnce(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	m := Model{client: r, attached: map[string]bool{}}

	m.attachAllDests(80, 24)
	m.attachAllDests(80, 24) // a second WindowSizeMsg must not re-attach

	for name, c := range map[string]*fakeConn{"local": local, "gpu01": gpu} {
		var n int
		for _, msg := range c.sent {
			if msg.Type == ipc.MsgAttach {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s got %d attaches, want exactly 1 — each attach replays "+
				"the full ghost buffer and re-counts work-state events", name, n)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `dialAllWith undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// dialAllWith dials every destination and keeps whatever succeeds. A
// destination unreachable at launch is left to its per-dest reconnect loop —
// an offline host must not stop the client starting, since its projects are
// only part of the workspace.
//
// It does NOT attach. Attach is the Model's job, on the first WindowSizeMsg,
// because AttachPayload carries the terminal geometry and nothing knows it
// yet here.
func dialAllWith(dials map[string]func() (tui.Client, error)) map[string]tui.Client {
	out := make(map[string]tui.Client, len(dials))
	for dest, dial := range dials {
		c, err := dial()
		if err != nil {
			log.Printf("remote: %s unreachable at launch: %v", dest, err)
			continue
		}
		out[dest] = c
	}
	return out
}
```

Each `dial` closure runs the existing per-destination version gate (`version_gate.go`) before returning its conn, so a version-mismatched daemon fails the dial and lands in the same "unreachable at launch" branch rather than being handed to the router.

On the Model side, make attach per-dest:

```go
// attachAllDests attaches every destination that is not attached yet, using
// the geometry the terminal just reported. Called from WindowSizeMsg, so a
// destination that reconnect adds later is picked up on the next resize.
//
// The attached map is what keeps this idempotent: every attach replays the
// full ghost buffer, and a second replay double-counts work-state events
// (workstate.go:76).
func (m *Model) attachAllDests(cols, rows int) tea.Cmd {
	if m.attached == nil {
		m.attached = map[string]bool{}
	}
	cwd, _ := os.Getwd()
	for _, dest := range m.knownDests() {
		if m.attached[dest] {
			continue
		}
		msg, _ := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{
			Cols: cols, Rows: rows, CWD: attachCWD(dest, cwd),
		})
		msg.Origin = dest
		if err := m.client.Send(msg); err != nil {
			continue // stays unattached; the next resize retries
		}
		m.attached[dest] = true
	}
	return nil
}
```

`knownDests()` returns the router's registered destinations (add `Dests() []string` to `Router`; the single-client path returns `[]string{""}`). Replace the single `m.attachToDaemon()` call at `model.go:623` with `attachAllDests`, and have the post-redial reattach at `reconnect.go:625` clear `m.attached[dest]` so the next resize re-attaches exactly that one.

Widen `NewModel`'s first parameter from `*ipc.Client` to `Client`, and wire it up:

```go
conns := dialAll(cfg)
router := tui.NewRouter(conns)
model := tui.NewModel(router, cfg, version, registry, stalePlugins)
for dest := range conns {
	model.SetRedialFunc(dest, redialerFor(dest, cfg))
}
```

No `SetClient` and no `ActiveDest` closure — the router is constructed first and passed in, and the Model pushes the active dest via `syncActiveDest()` (Task 8). Add `[[destinations]]` to config. `--remote <dest>` becomes a one-entry destination list with no local daemon.

- [ ] **Step 4: Verify end to end**

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race && ./scripts/dev.sh vet`

Then in dev mode only — close any running dev TUI/daemon first, since `build` refuses while binaries are held:

```bash
./scripts/dev.sh build
./scripts/quil-dev.ps1
```

Confirm `[dev]` in the status bar, then check by hand:

1. Existing tabs appear under a project named **Default** — nothing lost.
2. `alt+shift+n` creates a second project; its tab bar is independent; `ctrl+t` files the new tab into the project you are on.
3. Closing a tab with `alt+w` leaves no dangling entry — restart the dev daemon and confirm the tab list is still right.
4. `alt+p` filters; `alt+o` bounces between the last two.
5. Start Claude in a pane, switch to another project, let it hit a permission prompt — the sidebar shows `⚠` **while that project is in the background**.
6. `alt+shift+a` jumps to it across the project boundary.
7. `alt+shift+s` collapses; panes reflow; the setting survives a restart.
8. Add a test VM (`user@testvm`) as a destination — its projects appear as siblings; pulling its network shows `⚡` on that project alone while local panes keep working and typing into a local pane is not frozen.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/main.go internal/tui/model.go internal/config/config.go cmd/quil/remote_test.go
git commit -m "feat(quil): connect to several daemons from one client"
```

---

## Self-Review

**Spec coverage.** §1 data model → Tasks 2, 5. §2 routing → Tasks 1, 7, 8, 9, 16, 17. §3 accessor migration → Tasks 5, 6. §4 sidebar → Tasks 11, 12. §5 agent state → Tasks 10, 15. §9 persistence → Task 4. Project CRUD → Task 13. Picker/toggle → Task 14.

**Deferred to separate plans**, at the spec's own seams: §6 git subsystem, §7 MCP scoping, §8 listening ports.

**Type consistency.** `ProjectModel` fields and `cur()`/`curTabs()`/`allTabs()`/`projectOf()` are used identically from Task 5 on, and `activeTabModel()` keeps its existing name and signature throughout. `findPaneAndTab` returns `(*PaneModel, *ProjectModel, int)` from Task 6 on. `Projects()` returns `[]Project` (values) everywhere. `newFakeConn` is defined in Task 8 and reused by 12, 14, 15, 17; `tabWith`/`tabWithPane` are defined in Task 12 and reused by 15. `Router` is exported so Task 17 can construct it.

**Docs to update in Task 17 or a follow-up:** `docs/keybindings.md` for the five new bindings, `docs/configuration.md` for `[ui] sidebar_width` / `sidebar_open` and `[[destinations]]`, and a new `.claude/rules/` entry for the project and routing invariants — **not** `.claude/CLAUDE.md`, which is size-capped and build-enforced.
