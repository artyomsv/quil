# Robust Restart: Lazy Pane Restoration, Log Rotation, IPC Backpressure Hardening

**Date:** 2026-06-09
**Status:** Approved design — ready for implementation plan
**Branch target:** single PR, build order B → A → C

## Problem

On daemon restart with a large workspace (observed: 13 tabs / 14 panes, **12 of them `claude-code` panes**), the TUI was force-disconnected mid-restore and auto-closed, appearing to "crash after ~a minute." Root cause, from production logs (`~/.quil/quild.log`, `quil.log`, 2026-06-09 10:40–10:42):

1. The daemon restarted (`quild v1.16.1`) and `respawnPanes` relaunched **all 12 `claude.exe --resume` processes** within ~14 s — a heavy simultaneous Node boot storm. Each reconnects a `quil mcp` IPC client (daemon connection count climbed to 12).
2. The daemon **broadcasts every pane's live PTY output to all connected clients.** 12 booting Claude instances produced a broadcast flood.
3. The TUI's single-threaded Bubble Tea loop blocked for multi-second stretches (`WorkspaceStateMsg` apply = 1.3 s; one `View()` = **9.9 s**; `resizeTabs` = 3 s) and stopped draining its IPC socket.
4. The daemon's per-connection 64-slot send buffer (`internal/ipc/server.go`) overflowed → the slow-client guard fired `go c.Close()` (`dropping slow client (send buffer overflow)`) → the TUI saw `connection forcibly closed` and exited.

It was **not** a panic or OOM (0 `panic`/`fatal` in 864k daemon + 835k client log lines). The daemon's own backpressure guard killed the legitimately-busy primary TUI. Logs were also unbounded (74 MB / 182 MB, accumulated across months).

This design attacks all three layers: reduce the restore *load* (A, lazy restore — the user's primary ask), remove the *crash mechanism* (C, IPC hardening), and bound log growth (B, rotation).

## Scope

Three independent workstreams, one PR:

- **A — Lazy pane restoration.** Defer PTY spawn for all panes except the active tab's and any pane flagged `eager`. Spawn on first access (tab switch or MCP interaction). Per-pane `eager` flag toggled by keybinding, marked on the tab label.
- **B — Log rotation.** Rotate `quild.log` / `quil.log` at 5 MB to a timestamped file; keep the last 10 per log.
- **C — IPC backpressure hardening.** Stop force-closing a busy-but-alive client. Make live-output frames lossy via a dual-queue send path; reserve connection-close for genuinely wedged peers.

Out of scope (noted, not built): fix #2 (gate broadcast during attach) and #4 (TUI render-loop perf) become low-value once A+C land. Fix #3 (stagger respawns) is a no-op — eager respawns are already sequential.

---

## A. Lazy Pane Restoration

### Model changes (`internal/daemon`, `Pane`)

- `Eager bool` — **persisted** in `workspace.json` as `eager`. Default `false`. Mirrors the existing `Muted bool` end-to-end (see §A.5).
- `Pending bool` — **runtime only, not persisted.** `true` ⟺ pane model + ghost loaded but PTY not yet spawned.

### A.1 Selective restore

`restoreWorkspace` (`daemon.go:370`) is unchanged except it reads `eager` from each pane map (alongside `muted` at `:480`). It still builds every pane model and loads every `GhostSnap` — cheap, no process spawn.

**Confirm/ensure:** `restoreWorkspace` sets `session.activeTab` to the persisted `active_tab` so the correct tab is eager-spawned.

`respawnPanes` (`daemon.go:546`) becomes selective. Extract its per-pane body into `spawnRestoredPane(pane *Pane)` (the `newRestoredPTY` + `spawnPane` + fallback-to-terminal logic). Then:

```
active := d.session.ActiveTab()
for each tab, pane:
    if tab.ID == active || pane.Eager:
        spawnRestoredPane(pane)      // eager
    else:
        pane.Pending = true          // deferred
```

### A.2 Spawn-on-first-access

`ensurePaneSpawned(pane *Pane)` — idempotent, race-safe:

```
pane.PluginMu.Lock(); defer Unlock()
if pane.PTY != nil || !pane.Pending { return }   // double-check under lock
spawnRestoredPane(pane)
pane.Pending = false
```

(`PluginMu` already exists on `Pane`, used at `daemon.go:2389`. Holding it across `spawnRestoredPane` is acceptable — contention on a single pending pane is rare.)

Triggers:

- **Tab switch** — `handleSwitchTab` (`daemon.go:831`): call `ensureTabSpawned(tabID)` (loops the tab's panes through `ensurePaneSpawned`) **before** `broadcastState()`, so the freshly-spawned panes' live state is in the broadcast.
- **MCP interactive ops** — the daemon request handlers `handleReadPaneOutputReq`, `handleSendToPane`/input, `handleSendKeys`, `handleScreenshotPaneReq`, `handleRestartPaneReq`, `handleSetActivePane` resolve their target through `ensurePaneSpawned` first.
- **Non-spawning ops** — `list_panes` / `get_pane_status` / memory report do **not** spawn (listing must not boot everything). They report deferred state (see §A.3).

### A.3 nil-PTY handling

Deferred panes have `PTY == nil`. Audit and guard:

- `PaneInfo.Running` (`daemon.go:2390`): change `running := pane.ExitCode == nil` → `running := pane.PTY != nil && pane.ExitCode == nil`. A deferred pane is not "running."
- Add `Pending bool` to `PaneInfo` (`internal/ipc`) so clients/agents distinguish *deferred* from *crashed*.
- `read_pane_output` on a deferred pane: return the ghost buffer (history) rather than spawning, OR spawn-on-access — **decision: spawn-on-access** (consistent with §A.2; an agent reading a pane wants it live).
- Guard `handleResizePane`, `handlePaneInput`, memory report, and any other `pane.PTY.*` call against nil for the deferred window.

### A.4 Ghost preservation (data-loss guard — critical)

**Risk:** the snapshot path persists each pane's live `OutputBuf` to its on-disk ghost file. A deferred pane has an *empty* live buffer but a full on-disk `GhostSnap`. Naively snapshotting would **wipe the saved scrollback of every unopened tab.**

**Requirement:** the snapshot/ghost-write path must **preserve the existing on-disk ghost buffer for any pane with `PTY == nil`** (deferred or never-spawned), instead of overwriting it with the empty live buffer. Verify the ghostbuf write site in `internal/persist` + the daemon snapshot caller and add the `PTY == nil` (or `Pending`) skip. Add a regression test: snapshot a workspace with a deferred pane → its ghost file bytes are unchanged.

### A.5 Eager flag plumbing (mirrors `Muted`)

| Concern | `Muted` reference | `Eager` change |
|---|---|---|
| Pane field | `Pane.Muted bool` | add `Pane.Eager bool` |
| Restore read | `daemon.go:480` `paneData["muted"]` | read `paneData["eager"]` |
| Persist write | `daemon.go:1387` `paneData["muted"]=true` | write `paneData["eager"]=true` when set |
| IPC payload | `UpdatePanePayload.Muted *bool` (`protocol.go:160`) | add `Eager *bool` (tri-state) |
| Daemon handler | `daemon.go:1066` sets `pane.Muted` | set `pane.Eager`, then `requestSnapshot()` |
| State→TUI | `pane.Muted` in workspace-state payload (`daemon.go:1881`) | add `Eager` to the same payload + TUI pane model |
| Keybinding | `MutePane "alt+m"` (`config.go:200`) | add `ToggleEager string toml:"toggle_eager"`, default `"alt+shift+e"` |
| TUI send | `model.go:~3282` `MsgUpdatePane{Muted}` | analogous `MsgUpdatePane{Eager}` on key match |

Toggling does **not** respawn (the pane is already live when you toggle it); the flag only affects the *next* restart. Marker updates immediately.

### A.6 Tab marker (only genuinely-new UI)

`tabLabel(idx)` (`model.go:2162`): when any pane in the tab has `Eager == true`, prepend a marker glyph. Default **`●`** (U+25CF, single-width — deliberately BMP/non-emoji to avoid the conhost wide-char column drift documented in CLAUDE.md). Composes with the existing active-tab `* ` prefix and the index prefix. Keep the marker constant for v1 (no config knob — YAGNI).

### A.7 Behavior notes / accepted tradeoffs

- A deferred tab shows its ghost history instantly on first open (existing `GhostSnap` replay), then live output replaces it — no blank screen.
- Deferred panes emit no idle/bell/hook/notification events until first spawned. Accepted; documented in `docs/`.
- A never-visited pane survives indefinitely with history intact (model persisted from workspace.json, ghost file preserved per §A.4).

---

## B. Log Rotation (`internal/logger`)

### Design

New `rotatingWriter` (`internal/logger/rotate.go`) implementing `io.Writer` + `io.Closer`, mutex-guarded (many goroutines log concurrently):

- Fields: `dir`, `base` (`"quild.log"` / `"quil.log"`), current `*os.File`, `size int64`, `mu sync.Mutex`. Constants `maxSize = 5 << 20` (5 MiB), `maxFiles = 10`.
- `NewRotatingWriter(dir, base)`: opens `dir/base` `O_CREATE|O_WRONLY|O_APPEND`, seeds `size` from `Stat()`. If already `> maxSize`, rotate immediately.
- `Write(p)`: under lock, if `size+len(p) > maxSize` → `rotate()`; then write, `size += n`.
- `rotate()`: close current → rename `base` → `quild-YYYYMMDD-HHMMSS.log` (Go ref `20060102-150405`; on name collision append `-1`, `-2`, …) → open fresh `base`, reset `size=0` → `prune()`.
- `prune()`: glob `quild-*.log`, sort by name (timestamp-sortable), delete all but the newest `maxFiles`.
- `Close()`: close current file.

### Integration

- `cmd/quild/main.go:95` — replace `os.OpenFile(... "quild.log" ...)` with `logger.NewRotatingWriter(logDir, "quild.log")`; return it (caller closes).
- `cmd/quil/main.go:236` — replace `os.OpenFile(... "quil.log" ...)` with the rotating writer; keep `defer w.Close()`.
- Both still call `logger.Init(level, w)` (already takes `io.Writer`). Each writer rotates independently.

---

## C. IPC Backpressure Hardening (`internal/ipc/server.go`)

### Current behavior (the bug)

`Conn` has one `sendCh chan []byte` (cap 64). `sendFrame` (`server.go:102`) is non-blocking; on a full buffer it CAS-sets `overflow` and spawns `go c.Close()`. `Broadcast` (`:224`) and `Send` (`:77`) both feed `sendFrame`. A busy-but-alive client whose buffer fills (e.g. during a 10 s render block) is force-closed.

### Verified classification key

- **Ghost replay** uses `conn.Send` (unicast, `daemon.go:771` via `sendGhostChunked`).
- **Live PTY output** uses `server.Broadcast` (`daemon.go:1247`).
- Both are `MsgPaneOutput`; **only the broadcast path is high-volume and droppable.**

Therefore: `droppable = (msg.Type == MsgPaneOutput)` computed **in `Broadcast` only**. `Send` is **always** must-deliver → ghost replay and MCP responses are never dropped.

### Dual-queue design

Replace the single `sendCh` with two channels on `Conn`:

- `critCh chan []byte` (cap 64) — must-deliver: state broadcasts, responses, notifications, ghost replay, lifecycle.
- `outCh  chan []byte` (cap 64) — droppable: live `MsgPaneOutput` broadcast frames.

`enqueue(frame []byte, droppable bool)`:

- `droppable` → non-blocking send to `outCh`; on full, **drop** (bump a per-conn `dropped` counter; debug-log once). No overflow, no close.
- `!droppable` → non-blocking send to `critCh`; on full → existing overflow path (CAS `overflow` + `logger.Warn` + `go c.Close()`). A peer that cannot drain 64 *low-volume* critical frames is genuinely wedged.

`sendLoop` — priority select (critical first, so a flood of output never starves state):

```go
for {
    select {
    case <-c.done: return
    case f := <-c.critCh:
        if !c.write(f) { return }
    default:
        select {
        case <-c.done: return
        case f := <-c.critCh: if !c.write(f) { return }
        case f := <-c.outCh:  if !c.write(f) { return }
        }
    }
}
```

`write(f)` keeps the existing `SetWriteDeadline(30s)` + `raw.Write`, returns `false` on error (sendLoop exits → read side cleans up). The 30 s deadline remains the real "wedged kernel buffer" backstop.

Call-site changes:
- `Send(msg)` → `enqueue(frame, false)`.
- `Broadcast(msg)` → compute `droppable := msg.Type == ipc.MsgPaneOutput` once, then `enqueue(frame, droppable)` per conn.

Cross-class reordering (a state frame overtaking queued output) is harmless: output stays FIFO among itself (per-pane byte order preserved); state/responses are independent of the terminal byte stream.

### Effect

A 10 s-blocked TUI sheds live output frames (cosmetic — superseded by the next frame) while `critCh` keeps accepting low-volume state/responses and drains when the TUI unblocks. The connection is **never** closed for an output flood — only for a truly wedged peer (critical backlog ≥ 64 or 30 s write stall). Directly removes the crash mechanism, independent of A.

---

## Testing & Success Criteria

### A
- Pure selection logic (active-tab + eager → spawn; rest → pending) unit-tested.
- Daemon restore integration (fakes, per existing `daemon_test.go` / `spawn_args_test.go` patterns): assert only active-tab + eager panes have `PTY != nil`; rest `Pending`. Tab switch and MCP access spawn on demand and clear `Pending`.
- `handleUpdatePane` Eager toggle test (mirror `TestHandleUpdatePane_MutedFieldToggle` in `event_mute_test.go`): `Eager=&true` flips bit, `Name=""` preserved.
- **§A.4 regression:** snapshot a workspace containing a deferred pane → its on-disk ghost file bytes are byte-identical afterward.

### B
- `rotatingWriter` table tests under `t.TempDir()`: write past 5 MB → rotation; rotated file has timestamp name; collision suffix `-1`; prune keeps newest 10; `size` seeded from pre-existing file; oversized-on-open rotates immediately.

### C
- `Conn` tests (extend `conn_internal_test.go` / `broadcast_resilience_test.go`): fill `outCh`, send droppable → conn stays open, frame dropped, `dropped` counter increments; fill `critCh`, send critical → existing overflow-close + `ErrSendOverflow`; priority select drains `critCh` ahead of `outCh`; `Send` (ghost/responses) never dropped.

### End-to-end (manual, dev mode per `.claude/rules/dev-environment.md`)
- Restart a 13-tab / 12-claude-pane dev workspace → daemon spawns **only the active tab's pane(s)**; TUI attaches with **zero** `dropping slow client` in `.quil/quild.log`; switching to an unopened tab boots its pane on demand; unopened tabs retain scrollback across the restart; `.quil/*.log` cap at 5 MB × 10.

## Build Order

**B → A → C.** B is isolated/trivial. A is highest user value. C touches the IPC hot path — land it last on a known-good base. All three merge in one PR.

## Risks

- **C is the riskiest** (concurrency, existing hot-path tests). Mitigated by the dual-queue keeping `ErrSendOverflow`/`overflow`/`closeOnce`/`ConnCount` semantics intact and the priority-select being small and self-contained.
- **§A.4 ghost preservation** is the highest-consequence correctness item — a miss silently destroys user scrollback. Covered by the dedicated regression test, which must pass before merge.
- nil-PTY audit (§A.3) must be exhaustive; a missed `pane.PTY.*` deref on a deferred pane panics the daemon.
