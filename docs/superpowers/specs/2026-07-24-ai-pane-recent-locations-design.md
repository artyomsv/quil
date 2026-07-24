# AI Pane Setup Dialog — Recent Locations + Label Fit

**Date:** 2026-07-24
**Status:** Design approved
**Area:** `internal/tui` (create-pane setup dialog), `internal/plugin/defaults`, `internal/config`

## Problem

Two friction points in the pane setup dialog (`dialogCreatePaneSetup`), most visible when creating a Claude Code pane:

1. **Toggle labels wrap mid-word.** The dialog box auto-grows to fit the longest toggle label (`setupDialogWidth()`, floor 70), but is then clamped to terminal width (`m.width-2`). Claude Code's longest label is 65 chars → the box wants 75 cols; on a ~72-col terminal it clamps and the label wraps mid-word (`… no\nconfirmations)`), which reads badly.

2. **CWD always starts from the last-used location.** The browser pre-fills from a single in-memory value (`lastSelectedCWD`), lost on restart. A user juggling several projects must re-navigate the directory tree on nearly every new AI pane.

## Goals

- Toggle labels render on one line on any reasonably sized terminal (≥72 cols), without relying on the user widening their window.
- Offer the last 5 distinct working directories as a one-keystroke quick pick when creating a pane, surviving TUI/daemon restart.
- Zero regression to the existing directory browser and git-repo pick-list flows.

## Non-Goals

- No fuzzy search / filtering of recent locations (YAGNI; 5 entries fit on screen).
- No per-plugin recent lists — one global list shared by every `prompts_cwd` plugin.
- No change to how the daemon spawns panes or resolves CWD.

---

## Part 1 — Labels fit on one line

**Approach: shorten the labels** (chosen over widening the box or wrapping, because the box is fundamentally terminal-width-bound — a universal fix must reduce label length).

Inside the floor-70 box the usable label area is `70 - 6 (row chrome: "> " + "[x] ") - 4 (padding) = 60` cells. Trim the three Claude Code toggle labels in `internal/plugin/defaults/claude-code.toml` to ≤ ~50 chars:

| Old (chars) | New (chars) |
|---|---|
| `Dangerously skip permissions (unattended mode — no confirmations)` (65) | `Dangerously skip permissions (no confirmations)` (48) |
| `Enable auto mode (safer alternative to skipping permissions)` (60) | `Enable auto mode (safer than skipping permissions)` (50) |
| `Chrome support (connect to the Claude in Chrome browser extension)` (63) | `Chrome support (Claude in Chrome extension)` (43) |

All three now fit the floor-70 box on any terminal ≥ 72 cols; `setupDialogWidth()`'s auto-grow is retained unchanged for future longer labels / wider terminals.

Bump `[plugin] schema_version` **7 → 8**. Per project convention, changing an embedded default triggers `EnsureDefaultPlugins` to flag the on-disk file as stale, surfacing the one-time plugin-migration dialog so existing users can accept the new labels (or keep their own). This is the intended mechanism, not a side effect.

No Go code change is required for Part 1; the semantic args (`--dangerously-skip-permissions`, `--enable-auto-mode`, `--chrome`) and toggle names are untouched, so behavior is identical.

---

## Part 2 — Recent CWDs (last 5, persisted)

### Storage

- New config path `config.RecentCWDsPath()` → `QuilDir()/recent-cwds.json`.
- New file `internal/tui/recentcwd.go`, mirroring `instances.go`:
  - `LoadRecentCWDs(path string) []string` — reads JSON array; empty slice on missing/corrupt file.
  - `SaveRecentCWDs(path string, list []string) error` — atomic `.tmp` + rename, `0600`.
  - `pushRecentCWD(list []string, dir string, max int) []string` — **pure function**: cleans `dir` (`filepath.Clean`), removes any existing equal entry (case-insensitive compare on Windows, exact elsewhere), prepends it, truncates to `max`. Returns a new slice. Unit-tested for dedup / order / cap / empty-dir (no-op).

`recentCWDs` is TUI-owned state — a single writer (the TUI), consistent with `window.json` / `instances.json`. The daemon never reads it.

### Model wiring

- Add `recentCWDs []string` to `Model`; load it in the constructor via `LoadRecentCWDs(config.RecentCWDsPath())` (next to `instanceStore`).
- In `handleCreatePaneSplit` (where `lastSelectedCWD` is already committed), when `cwd != ""`:
  - keep the existing `m.lastSelectedCWD = cwd`;
  - `m.recentCWDs = pushRecentCWD(m.recentCWDs, cwd, recentCWDMax)` where `recentCWDMax = 5`;
  - persist with `SaveRecentCWDs(config.RecentCWDsPath(), m.recentCWDs)` (log-and-continue on error — a failed write must never block pane creation).

### Quick-pick UI (reuse the git repo pick-list)

The setup dialog already has a pick-list mode for `discover="git"`: `repoCandidates` + a trailing "Browse…" row, rendered in `renderCreatePaneSetupDialog` and driven by `handleSetupRepoKey`. Generalize it to serve recent CWDs too:

- Add `recentCandidates []string` to `Model` (the recent list currently offered; nil = not in recent-pick mode).
- In `enterSetupOrSplit`, for a `prompts_cwd` plugin:
  1. `discover="git"` and repos found → `repoCandidates` (existing, unchanged; takes priority).
  2. else if `recentCWDs` non-empty → set `recentCandidates` to the recent list **filtered to still-existing directories** (`os.Stat`), pre-select row 0. If filtering empties it, fall through to (3).
  3. else → `initSetupBrowser()` (existing directory browser).
- Rendering: compute the active pick list once — `pick := m.repoCandidates; if len(pick)==0 { pick = m.recentCandidates }`. The `len(pick) > 0` branch renders the rows + "Browse…" exactly as today; the footer hint reads `↑↓ navigate  Enter open here  Browse… for another folder` (source-agnostic wording).
- Key handling: `handleSetupCWDKey` enters pick mode when `len(m.repoCandidates) > 0 || len(m.recentCandidates) > 0`. Rename `handleSetupRepoKey` → `handleSetupPickKey`, operating on the active pick list; on the "Browse…" row it clears **both** `repoCandidates` and `recentCandidates`, then `initSetupBrowser()`. Selecting a row sets `cwdBrowseDir` and submits.

Because both sources funnel through the same pick UI and `submitSetupDialog` (which reads `cwdBrowseDir`), the selected-CWD capture, toggle handling, and split step are unchanged.

### Interaction with `lastSelectedCWD`

`lastSelectedCWD` still seeds the *directory browser* pre-fill (via `initSetupBrowser`) when the user picks "Browse…" or has no recent list. The recent-pick list is a faster path layered on top; the two coexist. The most-recent entry naturally equals `lastSelectedCWD`.

---

## Files

**Modify**
- `internal/plugin/defaults/claude-code.toml` — 3 label edits + `schema_version` 7→8.
- `internal/config/config.go` — `RecentCWDsPath()`.
- `internal/tui/model.go` — `recentCWDs`, `recentCandidates` fields; constructor load.
- `internal/tui/dialog.go` — `enterSetupOrSplit` recent-pick seeding; generalize render branch + `handleSetupCWDKey`; rename `handleSetupRepoKey`→`handleSetupPickKey`; push+persist in `handleCreatePaneSplit`.

**Add**
- `internal/tui/recentcwd.go` — load/save/push.
- `internal/tui/recentcwd_test.go` — `pushRecentCWD`, load/save round-trip.
- Extend `internal/tui/setup_dialog_test.go` — recent-pick seeding, Browse… escape, label-fit assertion.

**Docs**
- `docs/features.md`, `docs/configuration.md` (recent-cwds.json in the file map), `docs/plugin-reference.md` (if it quotes the old labels).
- `CHANGELOG.md`.
- `.claude/CLAUDE.md` — extend the pane-setup-dialog bullet.

## Testing / success check

- `./scripts/dev.sh test` green, including new `recentcwd_test.go` and extended setup-dialog tests.
- `./scripts/dev.sh vet` clean.
- Manual (dev build): creating a Claude Code pane shows the 3 toggle labels on one line at 72 cols; after creating panes in ≥2 directories, the next create shows a "recent" quick-pick; "Browse…" still reaches the full directory browser; the list survives a dev-daemon restart.

## Risks

- **Migration dialog friction** for the label change — acceptable, one-time, and the established convention.
- **Stale recent entries** (deleted project dirs) — mitigated by `os.Stat` filtering at seed time.
- **Path case/separators on Windows** — `pushRecentCWD` cleans and case-folds on Windows to avoid near-duplicate entries.
