---
description: Pane plugin schema, registry, instances, discovery opt-ins, and the bundled tool plugins (lazygit, k9s, lazysql). Load when touching the plugin package or any defaults/*.toml.
paths:
  - "**/internal/plugin/**"
  - "**/internal/gitdiscover/**"
  - "**/internal/kubediscover/**"
  - "**/defaults/*.toml"
  - "**/internal/tui/instances.go"
  - "**/internal/tui/overlay.go"
---

# Plugins

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## Plugin system

### Plugin system

`internal/plugin/` — pane types defined via `PanePlugin` struct. Terminal and `terminal-wide` ("Terminal (keeps content on squeeze)" — same shell detection with `Display.WideCanvas = true`, so pane squeezes never cut content at the cost of window-width formatting while narrow) are Go built-ins in `builtin.go`; claude-code, ssh, stripe are embedded TOML defaults in `defaults/*.toml` (written to `~/.quil/plugins/` on first run via `EnsureDefaultPlugins`). User TOML plugins override defaults. `schema_version` field in `[plugin]` section tracks TOML schema — `EnsureDefaultPlugins` detects stale files (user version < embedded) and returns `[]StalePlugin` for the TUI migration dialog instead of silently overwriting. Bump `schema_version` in embedded defaults when adding new fields or changing defaults. `Registry` manages loading, detection, and lookup. Plugins define `FormFields` + `ArgTemplate` for instance creation forms. `GhostBuffer` bool controls per-plugin ghost buffer persistence. `[[idle_handlers]]` TOML section for context-aware idle notifications (parallel to `[[error_handlers]]`). Optional `path` field in `[command]` for explicit binary location (bypasses PATH lookup). 3-tier detection: path override → `exec.LookPath` → `searchBinary` fallback.

**Pane setup dialog opt-ins**: `prompts_cwd = true` under `[command]` opens a directory browser at pane creation (pre-filled from active pane's OSC 7 CWD); `[[command.toggles]]` array-of-tables (fields: `name`, `label`, `args_when_on`, `default`, `group`) renders one control per toggle — enabled toggles' args are appended to `InstanceArgs`. Toggles with the same non-empty `group` value are mutually exclusive (rendered as radio buttons; enabling one disables the other group members; "none selected" is also valid). Empty `group` = independent checkbox; `raw_keys = [...]` declares keys that bypass Quil's global shortcut layer and are forwarded directly to the PTY (no built-in plugin currently opts in — Tab and Shift+Tab reach the PTY naturally because pane navigation moved to `Alt+Arrow`. The mechanism stays available for future plugins that need to override some other global shortcut.). claude-code uses `prompts_cwd` + `[[command.toggles]]` with a `permission_mode` group to offer a mutually-exclusive choice between `--dangerously-skip-permissions` and the safer `--enable-auto-mode` (with "neither" as a valid option). The TUI side lives in `internal/tui/dialog.go` (`enterSetupOrSplit`, `loadBrowseDir`, `handleCreatePaneSetupKey`, `renderCreatePaneSetupDialog`, `validateAndNormalizeCWD`) and `internal/tui/model.go` (`tryPluginRawKey`). See `docs/plugin-reference.md` for the full reference

### Plugin instances

`internal/tui/instances.go` — `InstanceStore` (map[pluginName][]SavedInstance) persisted to `~/.quil/instances.json`. `BuildArgs` expands `{placeholder}` templates from form field values

### Pane type fields

`Pane.Type` (plugin name, default "terminal"), `Pane.PluginState` (scraped key-values), `Pane.InstanceName`, `Pane.InstanceArgs`. All persisted in workspace JSON, backward compatible (missing `type` → "terminal"). `Pane.Muted` (persisted, `Alt+M` via `mute_pane`) suppresses notification events. `Pane.Eager` (persisted, `Alt+Shift+E` via `toggle_eager`) makes the pane respawn immediately on daemon restart instead of being deferred; tabs containing an eager pane show a `●` marker in the tab bar (rendered via `tabLabel`/`eagerTabMarker` constant)

### Plugin schema migration

`dialogPluginMigration` shown on startup when `EnsureDefaultPlugins` detects plugins whose `schema_version` < embedded default. Full-screen split view (`internal/tui/migration.go`): editable user config on the left, read-only new default on the right (both `TextEditor` with TOML highlighting). Tab bar for multiple stale plugins. Blocks until all resolved — Esc is a no-op, only Enter (save merged), A (accept default), or Ctrl+Q (quit) exit. On save: validates TOML syntax + `schema_version >= required`, writes to disk, reloads plugin registry. `StalePlugin` struct in `internal/plugin/plugin.go` carries user data + default data from `EnsureDefaultPlugins`. `ParseSchemaVersion` (exported) extracts `[plugin].schema_version` from TOML bytes

## Tool plugins

### Lazygit integration

`internal/gitdiscover/` — pure repo discovery (enclosing repo + one-level sub-repos, canonical paths via `filepath.EvalSymlinks`, cap 10). `discover = "git"` plugin opt-in switches the setup-dialog CWD step from the plain browser to a candidate list + "Browse…" fallback (only meaningful with `prompts_cwd = true`; unknown values fail plugin load). Per-tab Alt+G overlay (`toggle_lazygit` keybinding): `Pane.Overlay` bool (PluginMu-guarded) marks the pane as ephemeral — excluded from disk snapshots and ghost saves; TUI `TabModel.overlayPane` sits outside the layout tree; state machine in `internal/tui/overlay.go` (visible→hide; candidates from active pane CWD; match→show instantly with state preserved; multiple→`dialogGitRepoPick` picker; create/replace gated on binary availability). Overlay panes are muted at creation, keyboard-only (mouse swallowed), and auto-destroyed by the daemon on process exit (spec: `q` quits → `onPaneExit` checks `Overlay` → `cleanupPaneArtifacts` + `DestroyPane` + `broadcastState`; TUI reconciliation clears the slot; next Alt+G creates fresh). Allow-list while overlay visible: Alt+G hide, Ctrl+Q quit, redraw, Alt+1..9 tab switch — everything else (including Esc) reaches lazygit.

### k9s integration

`internal/kubediscover/` — pure kube-context discovery (parses kubeconfig YAML from `KUBECONFIG` (OS-list-separated) → `~/.kube/config`; reads only context names + default namespaces + current-context, never credentials; follows symlinks; sanitizes control chars from names/namespaces; every failure degrades to empty). New dependency `gopkg.in/yaml.v3`. `discover = "kube"` plugin opt-in (third valid value alongside `""`/`"git"`) adds a kube-context pick field to the setup dialog (CWD-independent, works with `prompts_cwd = false`): row 0 = "Default context" (no flag), rows 1..N = contexts (current marked `●`), capped at `maxKubeContexts = 50`; selecting a named context injects `--context <name>` into `InstanceArgs` via `submitSetupDialog`. Field plumbing in `internal/tui/dialog.go` (`setupFieldKind`/`setupFieldCount` insert a "kube" field, `handleSetupKubeKey`, render branch). The `k9s` built-in plugin (`defaults/k9s.toml`) opens k9s as a normal pane (not an overlay), `rerun` strategy, ghost buffer off. Uninstalled tool plugins are now shown greyed in Ctrl+N (not hidden) with a `homepage` link (`[plugin] homepage` field) and blocked from selection; the create-pane dialog auto-sizes its width to its content (`maxContentLineWidth`).

### lazysql integration

`defaults/lazysql.toml` — database TUI (MySQL/PostgreSQL/SQLite/MSSQL) as a normal pane. Connection-scoped, not directory-scoped: `prompts_cwd = false`, no `discover`, `rerun` strategy, ghost buffer off, one `--read-only` toggle.

**Deliberately no Quil-side connection picker** — lazysql's only launch arg is a full DSN (with credentials), which must never reach argv/`workspace.json`/MCP logs; connection selection + credentials stay in lazysql's own manager (`${env:}` substitution). TOML-only, no Go code; binary gating + greyed-when-missing come free.

