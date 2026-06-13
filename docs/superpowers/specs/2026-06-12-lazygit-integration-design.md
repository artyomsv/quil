# Lazygit Integration — Design Spec

**Date:** 2026-06-12
**Status:** Approved for planning
**Scope:** Three deliverables — lazygit built-in plugin (A), git-aware pane setup dialog (B), per-tab toggle overlay (C)

## Problem

Panes usually live inside git-managed workspaces: AI plugin panes open in project
folders, terminal panes navigate into repos. Quil has no fast path from "this pane
is in a git repo" to "show me a git UI for it". Lazygit is the obvious vehicle —
it is a mature git TUI with a CLI built for embedding (`lazygit --path <repo>`,
upward `.git` discovery from CWD identical to git itself).

The feature must be inert when the lazygit binary is not installed.

## Decisions (made during brainstorming)

| Question | Decision |
|---|---|
| Scope | All three approaches: plugin + dialog discovery + overlay |
| Overlay presentation | True toggle overlay (not split, not tab, not focus-mode wrap) |
| Hidden overlay process | Keeps running (instant re-show, lazygit UI state preserved) |
| Overlay cardinality | One per tab, keyed to repo resolved at open time |
| Overlay persistence | Ephemeral — excluded from workspace snapshots, gone on daemon restart |
| MCP exposure | `create_pane` does NOT expose the overlay flag in v1 |

## 1. Built-in plugin (A)

New `internal/plugin/defaults/lazygit.toml`, registered in `EnsureDefaultPlugins`:

```toml
[plugin]
name = "lazygit"
display_name = "Lazygit"
category = "tools"
description = "Git TUI for the current workspace"
schema_version = 1

[command]
cmd = "lazygit"
detect = "lazygit --version"
prompts_cwd = true
discover = "git"          # new opt-in, see §3

[[command.toggles]]
name = "screen_mode_full"
label = "Open focused panel full-screen"
args_when_on = ["--screen-mode", "full"]
default = false

[persistence]
strategy = "rerun"        # respawn lazygit in saved CWD on daemon restart
ghost_buffer = false      # full-screen TUI — replay is useless
```

- Binary gating is free: existing 3-tier `DetectAvailability()` (path override →
  `exec.LookPath` → `searchBinary`) greys the plugin out of Ctrl+N when lazygit
  is missing.
- No repo args needed for Ctrl+N panes — spawning with the chosen CWD suffices
  (lazygit walks upward for `.git`). `--path` is used only by the overlay (§4)
  where the repo is resolved explicitly.

## 2. New package: `internal/gitdiscover`

Pure, dependency-free, table-testable:

```go
// EnclosingRepo walks up from dir; returns repo root, true if found.
// Accepts .git as directory OR file (worktrees/submodules use a .git file).
// Walk-up terminates at the volume root; capped at 32 levels as a
// symlink-loop guard.
func EnclosingRepo(dir string) (string, bool)

// SubRepos returns immediate (1-level) subdirectories containing .git.
func SubRepos(dir string) []string

// Candidates: enclosing repo first, then sub-repos, deduped, absolute paths.
func Candidates(dir string) []string
```

- I/O errors degrade to "no candidates" — discovery never blocks pane creation.
- Symlinks resolved via `EvalSymlinks`, matching the daemon's `defaultCWD()`
  discipline.
- Shared by §3 and §4.

## 3. Git-aware setup dialog (B)

New `[command]` field `discover = "git"` (parsed in `registry.go`, stored on
`PanePlugin`). When set, the directory step of `dialogCreatePaneSetup` changes:

- Shows `gitdiscover.Candidates(activePaneCWD)` as a pick-list with one extra
  row — **"Browse…"** — falling through to the existing directory browser
  (escape hatch for repos elsewhere).
- Zero candidates → straight to the browser, exactly today's behavior.
- Discovery base is the active pane's OSC 7 CWD **directly** — it must NOT
  inherit `lastSelectedCWD` (that memory belongs to the generic browser; a
  stale last-choice from another project would seed wrong candidates). The
  "Browse…" fallback keeps the existing pre-fill chain
  (lastSelectedCWD → active pane CWD → home).
- No new dialog screen; it is a mode of the existing setup step.

## 4. Toggle overlay (C)

### Daemon side

- `Pane.Overlay bool`, set at creation via new `CreatePanePayload.Overlay`
  field (`omitempty`, backward compatible).
- **The flag also travels on the pane state payload** (alongside
  `Muted`/`Name`/`CWD`) in every `workspace_state` broadcast. Without this the
  TUI cannot distinguish overlay panes during reconciliation and would insert
  them into the split tree as visible splits.
- Overlay panes are **excluded from workspace snapshots** (`internal/persist`
  writer filters them). Ephemeral: daemon restart drops them; recreation is one
  keypress. The `rerun` persistence strategy never applies to overlay panes.
- Overlay panes do **not** count toward last-pane accounting: closing the last
  *normal* pane in a tab destroys the tab's overlay too, then the existing
  auto-recovery (fresh pane creation) proceeds normally.
- Created with `Pane.Muted = true` — a hidden lazygit refreshing must not ping
  the notification sidebar.
- Otherwise a completely normal pane: PTY, ring buffer, memory reporting,
  `list_panes` visibility all unchanged.

### TUI side

- New keybinding `toggle_lazygit = "alt+g"` (currently unbound; standard
  multi-binding syntax applies).
- State: `TabModel.overlayPaneID` + `overlayVisible` (per-tab).
- **Reattach:** overlay panes survive TUI disconnect (daemon owns them — only
  disk snapshots exclude them). On attach, the TUI scans incoming panes for
  `Overlay == true`, repopulates `overlayPaneID` per tab, defaults visibility
  to **hidden**.
- **Layout reconciliation in `applyWorkspaceState` skips overlay panes** — they
  are never inserted into the layout tree. Same exclusion anywhere the TUI
  picks a fallback `ActivePane`, and in spatial navigation's `CollectRects`.

### Alt+G state machine (precedence order)

1. Overlay **visible** (it has focus) → hide it. Full stop — never replaces
   from the overlay itself.
2. Overlay hidden or absent → resolve
   `gitdiscover.Candidates(activeNormalPane.CWD)`.
3. No candidates: if an overlay exists, show it anyway (Alt+G must never be a
   silent no-op when an overlay exists); else status-bar "no git repo here".
4. Any candidate == existing overlay's repo → show existing (instant, lazygit
   state preserved). Showing never requires a binary check — the process is
   already running.
5. Multiple candidates, none matching → minimal picker (`dialogGitRepoPick`,
   plain list, reuses the dialog system) to choose the target repo.
6. Create (or destroy + recreate, if a non-matching overlay exists) for the
   resolved/chosen repo (`--path <repo>`), then show. Silent replace — lazygit
   holds no unsaved state worth a confirm. The lazygit plugin availability
   check gates **this step only**; unavailable → status-bar message.

### Rendering, keys, resize

- Visible overlay renders over the full tab area — reuses the focus-mode render
  path (`TabModel` already draws exactly one pane full-size).
- All keys route to the overlay PTY except the toggle key, `Ctrl+Q`, and the
  global-exempt set (`notesKeyExempt` pattern — includes Alt+1..9 tab switch;
  the overlay is per-tab so switching tabs simply shows the other tab's normal
  layout). **Esc must reach lazygit** (core lazygit key) — Alt+G is the exit,
  plus `q` (lazygit quit → `process_exit` → pane destroyed → overlay state
  cleared via the existing exit event path).
- **Resize:** hidden overlays are not in the layout, so nothing resizes their
  PTY. On every show, and on `WindowSizeMsg` while visible, the TUI sends
  `MsgResizePane` with full-tab dimensions before rendering (focus mode already
  computes that rect).

### Lifecycle edges

- Tab close destroys the overlay pane with the tab (normal ownership).
- Hidden overlay's PTY output keeps buffering; the ring buffer caps it.
- lazygit crash or exit → existing `process_exit` path clears overlay state.

## 5. Error handling

| Failure | Behavior |
|---|---|
| lazygit binary missing | Plugin greyed in Ctrl+N; Alt+G → status-bar message |
| No repo found | Alt+G → status message (or show existing overlay); Ctrl+N → plain browser |
| Discovery I/O error | Treated as no candidates |
| lazygit exits/crashes | `process_exit` clears overlay state |
| Daemon restart | Overlay gone (ephemeral by design); Ctrl+N panes respawn via `rerun` |

## 6. Testing

- `gitdiscover`: table-driven with `t.TempDir()` fixtures — repo root, nested
  dir inside repo, `.git` file (worktree), no repo, sub-repos, unreadable dir.
- Plugin TOML: parse + toggle test alongside existing `plugin_test.go` patterns.
- TUI overlay state machine: `fakeSender` injection (pattern from the
  shutdown-confirm tests) covering toggle/show/replace/exit/reattach
  transitions and the §4 precedence order.
- Daemon: snapshot-exclusion test (overlay pane absent from `workspace.json`),
  last-pane accounting test (closing last normal pane destroys overlay +
  triggers auto-recovery).
- Phase 3 plan must include an explicit grep for "every pane in a tab is in the
  layout" assumptions (reconciliation, auto-recovery, resize, active-pane
  fallback, `CollectRects`) — there may be paths beyond the four identified.

## 7. Phasing

1. **Phase 1:** plugin TOML + `internal/gitdiscover` (A — shippable alone).
2. **Phase 2:** `discover = "git"` setup-dialog mode (B).
3. **Phase 3:** overlay (C) — IPC fields, daemon flag + snapshot exclusion +
   last-pane accounting, TUI state machine, picker dialog, key routing, resize.

Each phase ships independently; B and C share `gitdiscover`.

## Out of scope (v1)

- MCP-created overlays (`create_pane` keeps its current schema).
- Persisting overlay panes or their visibility across daemon restarts.
- Multiple overlays per tab / per-repo overlay registry.
- SQLite/worktree-aware repo metadata (branch names in the picker, etc.).
