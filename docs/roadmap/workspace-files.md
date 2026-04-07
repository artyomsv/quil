# Project Workspace Files (`.quil.toml`)

| Field | Value |
|-------|-------|
| Priority | 3 |
| Effort | Medium |
| Impact | Very High |
| Status | Proposed |
| Depends on | — |

## Problem

**Layer 3: Project fragmentation** — 5 terminals + 3 tools + 2 SSH sessions = no single "project view." New team members face onboarding friction: "How do I run this project?" → README with 8 terminal commands. Every developer sets up their own ad-hoc arrangement.

This is the single highest-impact feature for adoption. It transforms Quil from a personal tool into a team infrastructure tool.

## Proposed Solution

Define a workspace blueprint as a `.quil.toml` file checked into the repository. When a developer runs `quil` in a directory containing this file, the entire workspace materializes automatically.

```toml
# .quil.toml — checked into the repo
[workspace]
name = "my-saas-app"

[[tabs]]
name = "AI + Code"
[[tabs.panes]]
plugin = "claude-code"
cwd = "."
[[tabs.panes]]
plugin = "terminal"
cwd = "."
split = "vertical"

[[tabs]]
name = "Backend"
[[tabs.panes]]
plugin = "terminal"
cmd = "npm run dev"
[[tabs.panes]]
plugin = "terminal"
cmd = "npm test -- --watch"
split = "horizontal"

[[tabs]]
name = "Infra"
[[tabs.panes]]
plugin = "stripe"
args = ["listen", "--forward-to", "localhost:3000/webhooks"]
```

Then: `cd my-project && quil` → entire workspace materializes.

**Why this drives adoption:** Every team member who clones a repo gets the *exact same* dev environment. It becomes like `docker-compose.yml` — once one person adds it, everyone uses it. **Network effect within teams.**

## User Experience

### Creating a Workspace File

```bash
# Option 1: Manual — create .quil.toml in project root
# Option 2: Snapshot current workspace
quil workspace export > .quil.toml

# Option 3: Interactive
quil workspace init
```

### Using a Workspace File

```bash
cd my-project
quil                    # auto-detects .quil.toml, materializes workspace
quil --workspace alt.toml  # use a different workspace file
```

### Behavior

- If daemon is running with existing workspace, detect `.quil.toml` and offer to load it
- If starting fresh, create tabs/panes/splits from the file
- CWD paths are relative to the `.quil.toml` location
- `cmd` fields are executed in the pane after creation
- Plugin references must exist (built-in or installed in `~/.quil/plugins/`)

## Technical Approach

### 1. TOML Schema

```go
type WorkspaceFile struct {
    Workspace WorkspaceMeta `toml:"workspace"`
    Tabs      []TabDef      `toml:"tabs"`
}

type WorkspaceMeta struct {
    Name    string `toml:"name"`
    Version string `toml:"version,omitempty"` // schema version
}

type TabDef struct {
    Name  string    `toml:"name"`
    Color string    `toml:"color,omitempty"`
    Panes []PaneDef `toml:"panes"`
}

type PaneDef struct {
    Plugin string   `toml:"plugin"`           // plugin name
    CWD    string   `toml:"cwd,omitempty"`    // relative to .quil.toml
    CMD    string   `toml:"cmd,omitempty"`    // command to run after spawn
    Args   []string `toml:"args,omitempty"`   // plugin args
    Split  string   `toml:"split,omitempty"`  // "horizontal" or "vertical"
    Name   string   `toml:"name,omitempty"`   // pane display name
}
```

### 2. Loading Flow

```
quil starts
  → check CWD for .quil.toml
  → parse workspace file
  → connect to daemon (auto-start if needed)
  → for each tab:
      → create tab via IPC
      → for each pane:
          → resolve plugin
          → resolve CWD (relative → absolute)
          → create pane via IPC (with plugin type, args)
          → if split specified, send split command
          → if cmd specified, send input to PTY
```

### 3. Export Command

Snapshot current daemon state into `.quil.toml` format:
- Walk tabs/panes, extract plugin type, CWD, instance args
- Relativize CWD paths to project root
- Output valid TOML

### 4. File Location

- `internal/workspace/` — new package for workspace file parsing and materialization
- `cmd/quil/workspace.go` — CLI subcommands (`export`, `init`)

## Success Criteria

- [ ] `quil` in a directory with `.quil.toml` creates the described workspace
- [ ] `quil workspace export` snapshots current workspace to valid TOML
- [ ] Relative CWD paths resolve correctly
- [ ] Missing plugins produce clear error messages
- [ ] `.quil.toml` works across team members (no machine-specific paths)

## Open Questions

- Should workspace files support environment variables / templating?
- Merge vs replace behavior when daemon already has an active workspace?
- Should `cmd` support shell operators (pipes, &&)?
- Version field for schema evolution?
