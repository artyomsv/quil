---
name: notify-feature-blocking-com-calls
description: system-notifications feature (internal/notify) has unbounded COM calls on the TUI's Update goroutine — check on next review round whether this was fixed.
metadata:
  type: project
---

Round-1 finding (2026-08-13) on branch `feat/system-notifications`: `internal/notify`'s
Windows toast path has no timeout anywhere around the actual WinRT/COM call.

- `winNotifier.do()` (`internal/notify/notify_windows.go`) hands work to the COM-owned
  goroutine over an unbuffered channel and then does a bare `<-errc` with no deadline.
- `raiseAttentionToast` / `sweepOutstandingToasts` (`internal/tui/notify.go`) call
  `m.notifier.Notify` / `.Withdraw` **synchronously from `Model.Update`** — no `tea.Cmd`,
  no goroutine. `sweepOutstandingToasts` runs on every Update including the 100ms spinner
  tick whenever `outstandingToasts` is non-empty.
- `notify.New()` is also called synchronously during `launchTUI` (`cmd/quil/main.go:581`,
  before `tea.NewProgram` at line 713) — a hang there stalls the whole TUI before it ever
  draws a frame, with no fallback since the blocking call never returns to hit the nil-notifier
  fallback path.

**Why: this is the exact failure class the project has hit before and paid for** —
`.claude/rules/daemon-lifecycle.md`'s pane-input-pipeline writeup and `remote-dialogs.md`'s
blocking-FS-call budget both exist because a blocking OS call on a dispatch/Update goroutine
freezes the whole program. The notify code's own comments even document that the Windows toast
subsystem has measured multi-minute unpredictable delays (Start Menu indexing), which is
precisely the kind of dependency `~/.claude/rules/resilience-patterns.md` (paths: `**/*.go`)
requires an explicit timeout on ("every outbound call ... MUST have an explicit timeout").

**How to apply:** On the next review round for this feature/branch, check whether
`winNotifier.do()` gained a `select` with a timer case, or whether the `Notify`/`Withdraw`
calls moved off the Update goroutine (e.g. via `tea.Cmd` + async message, matching how
`internal/daemon/browse.go` and `discover.go` moved filesystem calls to worker goroutines with
`claimBlockingFSCall`-style budgets). If still absent, keep flagging — this is not a nitpick,
it's the same bug class as the 2026-06-11/12 PTY-write freeze incidents.

## RESOLVED (2026-08-13, same session)

Closed in commit `5ce7944`. `Notifier.Notify`/`Withdraw` now ENQUEUE onto a
bounded buffered channel (`workQueueDepth = 64`) and return immediately,
dropping on overflow — the same shape as `Pane.EnqueueInput`. Display failures
are logged inside the package rather than returned, because no caller on the
Update path did anything but log them.

The synchronous path survives only as `SyncNotifier` (`NotifySync`/
`WithdrawSync`), bounded by `syncCallTimeout`, and is used exclusively by
`quil notify test`, whose purpose is reporting the real HRESULT. `New`'s
startup handshake is bounded by the same timeout.
