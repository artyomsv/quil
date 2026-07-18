# Auto-Update — Design

Date: 2026-07-17
Status: Approved

## Goal

Quil detects new GitHub releases, surfaces "new version available" in the TUI,
downloads and stages the update in the background, and applies it at the next
`quil` launch with a single confirmation. Zero-disruption default: the running
daemon and its panes are never restarted behind the user's back.

## Decisions (locked with user)

1. **Staged model.** Auto mode = download + verify + stage in background;
   binary swap happens only at the next TUI launch (or explicit "Update now").
   Never a surprise daemon restart.
2. **Surfaces: status bar + About dialog + startup dialog.** Notification
   sidebar deliberately not used.
3. **Check on by default, daily.** `[update] check = true` opt-out. One
   unauthenticated GET per day (+ one shortly after daemon start).
4. **Auto-stage on by default.** `[update] auto = true`. `auto = false`
   degrades to notify-only.
5. **Startup dialog once per version.** Remembered via TUI-owned
   `last_notified_version`; the status bar remains the persistent reminder.
6. **Architecture A: daemon stages, TUI applies.** Daemon is the long-lived
   singleton (checker/downloader); the TUI launch moment is the safe apply
   point. Reuses the existing `restartDaemonForUpgrade` machinery.

Rejected alternatives: TUI-owned checking (N attached TUIs race, nothing
happens while detached); notify-only + manual `quil update` subcommand
(conflicts with auto-stage requirement); fully-automatic daemon restart
(hostile for a persistent multiplexer — kills pane children unprompted).

## Architecture

Three independent pieces matching process lifetimes:

- **Check + stage** — daemon background goroutine (pattern: `idleChecker`,
  memreport collector). New package `internal/update/`, stdlib-only.
- **Notify** — update state rides the existing workspace-state broadcast as an
  optional field; TUI renders status bar segment, About dialog lines, and a
  once-per-version startup dialog.
- **Apply** — `cmd/quil/` swap-and-respawn at launch (or via About → Update
  now). The existing version gate finishes the job: new TUI > old daemon →
  auto-confirmed graceful daemon restart via `restartDaemonForUpgrade`.

## Components

### 1. `internal/update/` — checker + stager (daemon side)

- `Check(ctx)` — GET
  `https://api.github.com/repos/artyomsv/quil/releases/latest`, 10 s timeout,
  no retry (next tick is the retry). Parses `tag_name`, `html_url`, asset
  list. Compares via `version.Compare`. Entirely skipped when
  `!version.IsRelease()` (dev builds) or `[update] check = false`.
- `Stage(release)` — selects asset `quil_<ver>_<GOOS>_<GOARCH>.tar.gz`
  (`.zip` on windows; naming per `.goreleaser.yml` `name_template`), downloads
  to a temp file under `$QUIL_HOME/update/`, fetches `checksums.txt`, verifies
  sha256 **before** extraction, unpacks `quil` + `quild` into
  `$QUIL_HOME/update/staged/<version>/`, then writes `manifest.json`
  (version, per-file sha256, staged_at) **last** — the manifest is the atomic
  "staging complete" marker. Crash mid-download leaves no manifest; the next
  tick re-stages. Older staged dirs are pruned when a newer one lands.
- Daemon goroutine: first check ~1 min after the IPC server starts listening
  (never slows restore), then every 24 h. `auto = false` → check only, no
  download. Clear shutdown path via the daemon's existing stop channel.
- State file `$QUIL_HOME/update/state.json` (atomic temp+rename, same idiom
  as `internal/persist/`): `last_check_ms`, `latest_version`,
  `staged_version`, `install_writable`, `release_url`. Daemon is the sole
  writer.
- Writability probe: at stage time the daemon probes its own executable's
  directory for write access. Not writable (system-managed install) →
  `install_writable = false`, skip download, UI degrades to notify-only with
  the releases URL.

### 2. IPC + TUI surfaces

- **IPC:** workspace-state broadcast gains optional `Update` object
  (omitempty; old clients ignore it): `{latest_version, staged_version,
  release_url, install_writable}`. Daemon triggers one extra
  `broadcastState()` when check results change (≤ once/day). TUI receives it
  on attach for free.
- **Status bar segment** (next to `[dev]`/`mem`): `↑ v1.37.0` (known, not
  staged) / `↑ v1.37.0 ready` (staged, applies next launch) / nothing when up
  to date.
- **F1 About dialog:** version line gains `Latest: v1.37.0 (staged — applies
  on next launch)` or `Up to date`. New item **Update now** (visible only when
  a newer version is known) → confirm dialog → apply flow.
- **Startup dialog** `dialogUpdateNotice` (disclaimer pattern): shown after
  attach when `latest > current && latest != last_notified_version`. Shows
  version, one-line state, release URL. Buttons: OK / Update now. On show,
  TUI writes `last_notified_version` to `$QUIL_HOME/update/notified.json`
  (TUI-owned file — single-writer-per-file rule; daemon owns `state.json`).
- **Dialog priority:** migration dialog (blocking) > update notice >
  disclaimer. Never stack two informational modals in one launch; disclaimer
  defers to the next start.

### 3. Apply flow (`cmd/quil/`)

Triggers: (a) `quil` launch with staged manifest present, (b) About → Update
now (TUI quits with an apply-intent marker; `main.go` runs the same path after
program exit). Both funnel into one `applyStagedUpdate()`.

When nothing is staged yet (`auto = false`, or the daily tick hasn't run),
**Update now** first sends `MsgStageUpdateReq` to the daemon, which runs the
same `Stage()` path on demand and replies `MsgStageUpdateResp`
(success/failure) unicast; the TUI shows a "staging…" state in the About
dialog and proceeds to the apply-intent quit only on success.

Sequence (before daemon connect):

1. Read manifest; proceed only if `staged_version > version.Current()`.
   Re-verify sha256 of staged files against the manifest (disk corruption /
   tampering guard).
2. Terminal prompt (version-gate style): `Apply staged update v1.37.0 now?
   Panes respawn (claude sessions resume). [Y/n]` — default **yes** (consent
   given via `auto = true`; unlike the gate's default-no). "n" → run the old
   version, ask again next launch.
3. Swap `quil` and `quild` at their install locations (`os.Executable()` for
   quil; exe-adjacent-then-PATH for quild, mirroring
   `findDaemonBinaryForUpgrade`):
   - **Windows:** rename running `quil.exe` → `quil.exe.old` (renaming a
     locked image is legal — NT locks by open handle, not path), copy staged
     binary into place. Same for `quild.exe`; the running daemon keeps
     executing from its open handle.
   - **Unix:** `rename(2)` over the target — atomic; keep `.old` backup.
   - Any step fails → roll back from `.old`, log, continue on the old
     version.
4. Spawn new `quil` with the original args + `QUIL_UPDATE_RESTART=1`, exit.
5. New TUI's existing version gate sees TUI > daemon; the env marker
   auto-confirms `promptRestartDaemon` (no second prompt).
   `restartDaemonForUpgrade` + the post-restart handshake verify handle the
   rest, including the shadowed-PATH failure message.
6. On success (versions match): delete `.old` files + staged dir.

### 4. Config

New `[update]` section in `internal/config`:

```toml
[update]
check = true   # daily release check (network: one GET to api.github.com)
auto  = true   # background download + stage; false = notify-only
```

Settings dialog gets both toggles (rides the existing `configChanged`
persistence on TUI exit).

## Edge cases

- **Dev builds** (`!version.IsRelease()`): no check, no stage, no apply.
- **Unwritable install** (package manager, system dir): notify-only + URL.
- **Second attached TUI at the old version** after the daemon restarts: hits
  the existing "TUI too old" blocking prompt and exits. Acceptable,
  documented.
- **Crash mid-stage:** no manifest → next daily tick re-stages from scratch.
- **Swap failure midway:** rollback from `.old`; old version keeps running.
- **`quild` not adjacent to `quil`:** resolved via the same
  exe-adjacent-then-PATH order as `findDaemonBinaryForUpgrade`; both paths
  swapped independently.
- **GitHub unreachable / rate-limited:** check fails silently at debug log
  level; state keeps the previous result; next tick retries.

## Testing

- `internal/update` unit tests (table-driven, `httptest.Server`): release
  JSON parsing, asset selection per OS/arch, checksum verification (accept +
  reject), manifest atomicity (no manifest → re-stage). Fixtures use the real
  GitHub response shape with obviously-synthetic versions (`v0.0.0-test`).
- Swap sequence behind a small interface, tested with `t.TempDir()` (rename,
  copy, rollback-on-failure branches).
- Version-gate auto-confirm: unit test on the `QUIL_UPDATE_RESTART` branch.
- End-to-end: dev-mode daemon against a mock release server via a
  test-only base-URL override (`QUIL_UPDATE_URL` env).

## Out of scope (YAGNI)

- Release channels / pre-release opt-in.
- Delta updates; signature verification beyond sha256 checksums.
- Rollback beyond one `.old` generation.
- Updating while the TUI stays running (hot swap).
- OpenCode-style in-place daemon binary reload.
