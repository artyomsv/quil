# Process Dialog — Design

**Status:** approved design, not yet implemented
**Branch:** `feat/process-dialog` (based on `origin/master` @ 950d643)
**Supersedes:** the F1 → Processes dialog added in `ab3ac99` and removed from PR #177 before merge

## Goal

Diagnose a runaway process without leaving quil, and see quil's own processes and
their versions, in one dialog under F1.

Two distinct questions, one surface:

1. *What is eating this machine?* — the OS process tree beneath each pane, with
   memory and CPU, and the ability to kill a runaway.
2. *What is quil actually running right now?* — the TUI, the daemon and every MCP
   bridge, with version, uptime and PID, so a stale binary is visible.

## Non-goals

- Killing quil's own processes. The TUI, the daemon and bridges are never killable
  from this dialog.
- Killing a pane's direct child. That is restart-pane, which already exists.
- Replacing a real process explorer. This shows quil's panes and quil's processes,
  not the whole machine.
- Aggregating across daemons. Primary destination only — see *Multi-daemon scope*.

## Why the first attempt failed

`ab3ac99` shipped a dialog whose central capability was dead on **both** platforms,
which is worth stating precisely because the failure was architectural rather than
a collection of bugs:

- It scanned the **client's** OS process table and inferred identity from image
  paths. In remote mode that is the wrong machine entirely — the daemon's panes run
  on the daemon's host.
- Windows: `Cmdline` was filled from `QueryFullProcessImageNameW`, an image *path*
  with no `mcp` token, so the subcommand match never fired and every bridge
  classified as the TUI.
- Unix: an orphan reparents to PID 1, which is always present and always older, so
  the liveness check never reported a dead parent.
- `IsBridge` tested the whole first field for the substring `"quil"`. This repo's
  own path contains it, so `/home/quil/.venv/bin/python -m mcp` classified as
  quil's own orphaned bridge — and would have been offered for killing.

**The lesson this design is built on: do not infer identity from the OS process
table.** The daemon spawned the PTY children, so it knows their PIDs as fact. Every
quil process dials the daemon's socket, so it can state its own identity. Neither
requires guessing, and neither is wrong in remote mode.

The ten review findings recorded against the removed dialog
(`.claude/reviews/global/frame-cost-and-memory.md`, "Removed with the feature") are
treated as a checklist here; *Prior findings incorporated* maps each one.

## Decisions

| # | Decision | Rationale |
|---|---|---|
| 1 | One dialog, both sections | Quil processes and pane trees answer the same operator question |
| 2 | **Replaces** F1 → Memory | Avoids two dialogs with overlapping pane rows |
| 3 | CPU on all three platforms | A runaway usually spins; RSS alone cannot distinguish a fat idle process from a spinning one |
| 4 | Kill descendants only | Blast radius stops at a process the user started inside their own pane |
| 5 | Kill is subtree, TERM then KILL | Killing a parent alone orphans its children onto the pane |
| 6 | Collector **gated on dialog open** | No permanent background cost, and no permanent `ps` fork on Darwin |
| 7 | Status bar + TUI-local memory migrate to the new report | One tick, nothing lost |
| 8 | Primary daemon only | Follows the existing memory-dialog precedent |

## Architecture

```
                    ┌─────────────────────────── daemon host ───┐
  TUI               │  Daemon                                    │
   │                │    helloByConn ◄── MsgClientHello           │
   ├─ MsgClientHello┼──►                    (TUI, bridges)        │
   │                │    procCollector (runs only while a client  │
   ├─ MsgResource ──┼──►   WithTrees requests keep arriving)       │
   │     ReportReq  │      │                                      │
   │                │      ├─ proctree.Enumerate()  ── platform   │
   │  ◄── Resp ─────┼──────┤                                      │
   │                │      ├─ proctree.Build(table, rootPIDs)     │
   ├─ MsgKillProc ──┼──►   ├─ memreport RSS + proctree CPU        │
   │     Req        │      └─ SessionManager.PaneSources()        │
   │  ◄── Resp ─────┼───                                          │
                    └────────────────────────────────────────────┘
```

Everything that touches a process runs **daemon-side**. The TUI renders and asks;
it never enumerates, never reads a PID, and never signals.

### `internal/proctree` — the pure library

Follows the split `internal/notify` and `internal/memreport` already use: logic
files are platform-neutral and take their syscalls as parameters, so Linux CI
compiles and tests all of the logic. Only enumeration and CPU reads sit behind
build tags.

```
internal/proctree/
  proctree.go        Node, Build — pure, no syscalls
  sample.go          Sampler: CPU percent from two samples. Pure, clock injected.
  table.go           ProcessEntry, the table type. Pure.
  table_windows.go   Toolhelp32 enumeration
  table_linux.go     /proc scan
  table_darwin.go    ps -axo pid=,ppid=,lstart=,comm=
  table_stub.go      other GOOS → empty table, supported=false
  cpu_windows.go     GetProcessTimes  → cumulative
  cpu_linux.go       /proc/<pid>/stat utime+stime → cumulative
  cpu_darwin.go      ps -o pid=,pcpu= → instantaneous percent
  cpu_stub.go        other GOOS → unsupported
```

### Data model

```go
// ProcessEntry is one row of the OS process table. Enumeration only —
// no identity, no classification.
type ProcessEntry struct {
    PID   int
    PPID  int
    Name  string        // image name only, never a path or command line
    Start time.Time     // zero when the platform could not read it
}

// Node is one process in a pane's descendant tree.
type Node struct {
    PID      int
    PPID     int
    Name     string
    Start    time.Time
    RSSBytes uint64
    CPUPct   float64   // -1 when unknown
    Depth    int       // 1 = the pane's direct child
    Children []*Node
}
```

**`CPUPct` is `-1`, never `0`, when unknown.** A first sample, a process that
appeared this tick, a platform without a CPU source, and a negative delta from PID
reuse all mean *no answer*. `0` is a confident claim of "idle", which is the
precise wrong answer in a dialog whose purpose is finding something that is
spinning. The dialog renders `-1` as `—`. This mirrors the convention `ab3ac99`
used for a zero `Start`, which was the one judgement call in that package review
did not fault.

**`Name` is the image name only.** Never a path, never a command line. Command
lines carry secrets (tokens in argv), are unbounded, and are the value review
flagged as the one item on the render path that skipped sanitizing. Not carrying
them removes the problem rather than guarding it.

**`Start` is carried, and is load-bearing.** It is not decoration:

- **Tree construction.** A parent map alone is not trustworthy under PID reuse — a
  recycled PPID can splice an unrelated process into a pane's tree, where this
  design would render it *killable*. `Build` therefore rejects a parent→child link
  when the child's `Start` precedes the parent's, both being known. A process
  cannot be older than its parent.
- **Kill re-validation.** See *Kill path*.
- **CPU identity.** A cumulative CPU delta is only meaningful if both samples came
  from the same process. A changed `Start` for a PID invalidates the previous
  sample, yielding `-1` rather than a garbage percentage.

Where `Start` is unknown (zero), the link is **kept** and the kill is **refused**.
A missed splice merely shows an extra row; a false splice invites the user to kill
something in use. The asymmetry decides the direction in both cases.

### Cycle safety

`Build` walks breadth-first from the root PIDs carrying a visited set and refuses
to revisit a PID. A corrupt or racing parent map can contain a cycle — PID reuse
mid-enumeration will produce one — and the alternative to a visited set is a
collector goroutine that never returns. A cycle truncates a branch; it does not
hang the daemon.

### Collector

Same proven shape as `memreport.Collector`: a `Run` loop on a ticker, a `busy`
`CompareAndSwap` that skips a tick if the previous one is still running, an atomic
snapshot pointer, and a pure core with syscalls injected for testing.

Two differences:

1. **It is gated on demand, by request shape rather than by a close message.** The
   collector starts on a `MsgResourceReportReq` with `WithTrees` set, and stops once
   no such request has arrived for 15 s. A status-bar request (`WithTrees` false) is
   served entirely from `memreport` and never starts it.

   The gate is a decaying deadline, not a counter of open dialogs, and that is
   deliberate: an explicit close message is a message that can be lost — a client
   that crashes, drops its conn, or is killed mid-dialog never sends one, and the
   collector would run forever with nobody watching. A deadline that must be
   continuously renewed cannot leak, because the failure mode of every lost message
   is that the collector stops, which is the safe direction. The dialog already
   refreshes on a 5 s tick, so renewal is free.

   There is no permanent background cost, and on Darwin no permanent `ps` fork every
   five seconds for a dialog nobody is looking at.

   Consequence, accepted deliberately: the first report after opening has no
   previous CPU sample, so the CPU column reads `—` for one refresh (~5 s) and then
   fills in. This is the same state the `-1` convention already renders.

2. **It holds the previous CPU sample** — a `map[int]cpuSample{Start, Cumulative}`
   — which is the state an on-demand-only design has nowhere to put.

### Report IPC

```go
type ResourceReportReqPayload struct {
    WithTrees bool `json:"with_trees,omitempty"`
}

type ResourceReportRespPayload struct {
    AtMS      int64          `json:"at_ms"`       // snapshot age basis
    Total     uint64         `json:"total"`       // workspace memory total
    Panes     []PaneResource `json:"panes"`
    Quil      []QuilProcess  `json:"quil,omitempty"`
    Unidentified int         `json:"unidentified,omitempty"`
    TreesStale   bool        `json:"trees_stale,omitempty"`
    CPUSupported bool        `json:"cpu_supported"`
}

type PaneResource struct {
    PaneID      string  `json:"pane_id"`
    TabID       string  `json:"tab_id"`
    GoHeapBytes uint64  `json:"go_heap_bytes"`
    PTYRSSBytes uint64  `json:"pty_rss_bytes"`
    Tree        []*Node `json:"tree,omitempty"` // only when WithTrees
}
```

`Tree` is `omitempty` and absent from every status-bar poll, which is the field that
would otherwise dominate the frame. A workspace of 40 panes each with a handful of
descendants is a few hundred nodes; at roughly 100 bytes of JSON per node that is
tens of kilobytes per report, and only while the dialog is open.

This matters because `daemon-lifecycle.md` records oversized frames on the 64-slot
must-deliver queue as a real failure shape — a client's own critical queue
overflowed at 33 tabs and 36 panes, and the TUI closed its connection and exited.
Sending trees on the always-on status-bar tick would put that frame on the wire
every 5 s for the life of the session.

### Blocking discipline

`daemon-lifecycle.md` records a history of daemon wedges caused by blocking calls,
so this is specified rather than assumed:

- **No syscall runs under a lock.** `SessionManager.PaneSources()` takes its RLock,
  builds adapters and returns; each `paneSourceAdapter.Snapshot()` takes that pane's
  `PluginMu` and releases it. Every enumeration, RSS read and CPU read happens after
  all locks are released, on the collector's own goroutine. This is exactly what
  `memreport` already does.
- **`helloByConn` gets its own mutex**, never `sm.mu` — copying the
  `attachedConns`/`attachedMu` precedent in `daemon.go`, which exists for this
  reason.
- **Every `ps` invocation is bounded.** 2 s `exec.CommandContext` plus a 1 MiB
  `io.LimitReader`, matching `memreport/procrss_darwin.go`, which documents why.
  The salvaged Darwin enumeration used a bare `exec.Command(...).Output()` with
  neither; that regression is not carried forward.
- **A wedged collector must be visible.** The `busy` CAS means a stuck tick makes
  the snapshot silently stale forever. The response therefore carries the snapshot's
  age, and the dialog renders a staleness notice past 30 s rather than presenting
  old numbers as current.

### What is NOT salvaged from `ab3ac99`

The enumeration files were described as reusable during design. They are not,
and the specifics matter because "salvaged" would otherwise hide them:

| File | Defect | Resolution |
|---|---|---|
| `snapshot_windows.go` | Calls `processDetail()` — `OpenProcess` + `GetProcessTimes` + `QueryFullProcessImageNameW` — for **every process on the machine**, every tick | Toolhelp32 gives PID/PPID/name but **not** start time. Handles open only for candidate descendants, via the two-pass scheme above; `QueryFullProcessImageNameW` is dropped outright (the data model has no path) |
| `snapshot_unix.go` (Darwin) | `exec.Command(...).Output()` with no timeout and no output cap | 2 s `CommandContext` + 1 MiB `LimitReader` |
| `snapshot_unix.go` (Darwin) | `filepath.Base(strings.Fields(Cmdline)[0])` truncates any comm containing a space — `Google Chrome Helper` → `Google` | Read `comm` as the trailing field, not a space-split first field |
| `snapshot_unix.go` (Linux) | `boot.Add(ticks * time.Second / clockTicksPerSecond)` overflows int64 past ~2.9 years of uptime — the recorded security/LOW finding | Compute in `time.Duration` from a widened intermediate; regression test at 3-year and 10-year uptimes |
| both | Fills `Cmdline` from `/proc/<pid>/cmdline` and `ps comm` | Not carried; the data model has no command line |
| `snapshot_windows.go` | `syscall.NewLazyDLL` | `windows.NewLazySystemDLL`, matching `memreport/procrss_windows.go` |

The *approach* in those files is sound — Toolhelp32, `/proc`, `ps`. The code is
rewritten against this data model rather than adapted.

### Platform matrix

| | Enumeration | CPU source | Semantics |
|---|---|---|---|
| Windows | `CreateToolhelp32Snapshot` + per-candidate `GetProcessTimes` | `GetProcessTimes` kernel+user | Two-sample delta |
| Linux | `/proc` scan | `/proc/<pid>/stat` utime+stime | Two-sample delta |
| Darwin | `ps -axo pid=,ppid=,lstart=,comm=` | `ps -o pid=,pcpu=` | **Kernel decaying average** |
| other | none | none | `—`, section reads "not supported on this platform" |

**`lstart=` is not optional.** It is what gives Darwin a `Start`, and `Start` gates
both the splice rejection and every kill. Omitting it does not degrade the feature
gracefully — it makes `Start` zero for every Darwin process, which by the kill
path's own "unknown `Start` refuses" rule means **every kill on Darwin is refused,
permanently**. `lstart` occupies five whitespace-separated fields
(`Wed Aug 20 12:00:00 2026`), so the row is parsed as: first field PID, second PPID,
next five `lstart`, and **everything remaining** is `comm` — trailing rather than
space-split, which is also what stops `Google Chrome Helper` becoming `Google`.

### Windows needs two passes, and this is real design work

`PROCESSENTRY32W` carries `th32ProcessID`, `th32ParentProcessID` and `szExeFile` —
and **no creation time**. There is no single Windows call that returns a process
table with start times; `GetProcessTimes` needs an open handle, per process.

That collides with the requirement to open handles only for pane descendants:
`Build`'s splice rejection needs `Start` to decide which links are real, but which
PIDs are descendants is only known after building. Resolved with two passes:

1. **Tentative tree** from Toolhelp32's PPID links alone, no start times.
2. **Open handles on the candidate set only** — the tentative descendants, not the
   machine — and fill `Start` via `GetProcessTimes`.
3. **Re-validate** each link against the now-known start times and prune any child
   older than its parent.

The candidate set is bounded by a pane's own descendants, so the per-tick handle
count stays small; what is rejected is the removed code's behaviour of opening a
handle for **every process on the machine**. Unix has no such problem — `/proc` and
`ps` both return start times in the same pass — so this is Windows-only, in exactly
the code CI never compiles (Risk 2), and it needs a natively-run test.

**Darwin CPU is not a two-sample delta, and the dialog says so.** `proc_pidinfo` is
a libproc function; this repo has zero CGo, and `x/sys/unix` provides no wrapper for
it. The syscall number exists (`SYS_PROC_INFO = 336` in `zsysnum_darwin_*.go`) but
raw syscalls are not a supported ABI on macOS — `x/sys/unix` reaches libc through
`//go:cgo_import_dynamic` trampolines against `/usr/lib/libSystem.B.dylib`, which
would mean hand-written assembly stubs and a hand-declared C struct for a platform
CI cannot run. `ps -o pcpu=` is the realistic no-CGo route, and it reports a
kernel-computed decaying average rather than usage over our sample window.

The dialog footnotes this the way the memory dialog already footnotes *"PTY RSS is
OS-reported; not comparable across platforms."* A column that looks uniform while
meaning different things on different platforms is the kind of confidently-wrong
answer this design exists to remove.

**CPU percent is per-core.** A process using two cores fully reads `200%`, matching
`top` and `ps` on every platform here. Machine-normalized percentages hide the most
important case — a runaway saturating several cores.

## Identity: `MsgClientHello`

Client → daemon, fire-and-forget, no response.

```go
type ClientHelloPayload struct {
    Role     string `json:"role"`       // "tui" | "bridge"
    PID      int    `json:"pid"`
    Version  string `json:"version"`
    ExeName  string `json:"exe_name"`   // filepath.Base(os.Executable())
    UptimeMS int64  `json:"uptime_ms"`  // duration, NOT a timestamp
}
```

**Uptime is a duration, never a start timestamp.** In remote mode the TUI and the
daemon are on different machines with unsynchronised clocks. A daemon computing
`now − clientStartTime` would report uptimes skewed by the clock difference, and
negative ones whenever the client's clock runs ahead. A duration is skew-free: the
daemon stamps arrival and extrapolates.

The field is not redundant with the daemon's own connection age, because a
**re-dial** resets the connection but not the process. `redial_local.go`
re-establishes a conn after a daemon restart; afterwards the conn is seconds old
while a stale bridge has been alive for days. That gap is exactly what the section
exists to show.

**`ExeName` is the basename**, so `quil.exe.old.3` still appears — the real
production observation behind the original feature, a bridge pinning a two-day-old
binary through an in-place update swap.

**`⚠ stale` is `hello.Version != daemonVersion`** — a string comparison. No process
query, no PPID liveness check, no orphan classification anywhere in this design.

### Where hello is sent

Not from `ipc.NewClient`. Roughly ten call sites dial the daemon across `cmd/quil`
and `cmd/quild`, and most are short-lived probes — the three in `daemonctl.go`, plus
`status.go`, `version_gate.go` and `cmd/quild/guard.go` — which dial, ask one thing
and close. Registering them would fill the section with processes that no longer
exist. This is the same distinction the
daemon already draws for attachment — *"ATTACHMENT, not connection, is what 'a
client is here' means"* (`markClientAttached`, `daemon.go`).

Hello is sent by the durable roles only: the TUI's post-attach path, the bridge's
`connectToDaemon`, and every re-dial path. All of them go through **one helper**, so
a new durable dial site cannot silently forget — the `enqueueInput` funnel
discipline, applied to a second problem.

The daemon holds `helloByConn map[*ipc.Conn]ClientHelloPayload` under its own mutex,
cleared in the existing `onClientDisconnect` beside `forgetAttachedClient`.

**The daemon's own row is not a hello.** It reads its own `os.Getpid()`, version
constant and start time directly.

### Unidentified clients

A bridge from a build predating this feature never sends hello. That is not a corner
case: the stale bridges this section exists to expose are, at rollout, precisely the
ones too old to announce themselves — the feature would otherwise demo as "no stale
bridges" in exactly the situation where stale bridges exist.

The daemon therefore counts conns with no hello and the dialog renders
`N unidentified client(s) — predates this feature or failed to identify`. An honest
unknown, not a silent omission.

**The count is age-gated, or it lies.** Conns without a hello are not only old
bridges: `quil status`, the version-gate probe, `quild guard` and the three
`daemonctl.go` probes all hold a conn for a second or two and — by this design's own
rule — never hello. A report taken while one is in flight would claim an
unidentified client that predates the feature, which is false and self-inflicted.

Only conns with **no hello and an age over 60 s** are counted. A probe never
survives that; a stale bridge has been alive for days. The threshold discriminates
on the one axis that actually separates the two populations, rather than on a
property they share.

**Version comparison is variant-aware.** `hello.Version != daemonVersion` would flag
a dev TUI against a prod daemon as stale, and mixed-variant sessions are the normal
case while developing quil itself. The comparison is on the version string proper;
a variant difference is shown as a distinct marker, not as `⚠ stale`.

## Kill path

The highest-risk part of the design, and the one the previous attempt got most
wrong. Every step runs daemon-side.

### What is killable

A node with `Depth >= 2` — strictly below a pane's direct child. The pane's own
shell or agent (`Depth == 1`), the daemon, the TUI and every bridge are not
killable, and the dialog renders them with an explicit `— not killable` marker
rather than silently ignoring the key.

Killability is decided **daemon-side on the fresh snapshot**, never from the
client's request. A client asking to kill a PID it claims is a descendant proves
nothing; the daemon re-derives the tree and checks for itself.

### IPC

```go
type KillProcessReqPayload struct {
    PaneID string `json:"pane_id"`  // the pane whose tree the target must be in
    PID    int    `json:"pid"`
    StartMS int64 `json:"start_ms"` // target's Start as the client saw it
}

type KillProcessRespPayload struct {
    Killed  int    `json:"killed"`            // processes signalled
    Refused string `json:"refused,omitempty"` // reason, when nothing was killed
}
```

### Daemon-side validation, in order

1. Re-enumerate the process table **now**. The client's snapshot is up to 5 s old.
2. Rebuild the tree rooted at `PaneID`'s live PTY child.
3. Require the target PID to be present in that tree at `Depth >= 2`.
4. Require the target's `Start` to match `StartMS` within one second. **This is the
   PID-reuse defense**: a PID recycled between the snapshot and the accepted confirm
   is a different process wearing the same number, and the only thing that
   distinguishes them is when they started. A mismatch, or an unknown `Start` on
   either side, refuses the kill.
5. Only then signal.

Any failed check returns `Refused` with a reason the dialog shows. A refusal is a
normal outcome, not an error.

### Signalling

Subtree, TERM then KILL. Killing `node vite` alone would orphan its `esbuild`
children onto the pane — the mess the dialog was opened to clean up.

**Validation covers the target; signalling must cover every process it touches.**
The five ordered checks above authorise the *operation*. They say nothing about the
subtree's other members, and nothing about the state of anything 3 s later. Three
distinct windows, each closed explicitly:

**1. Every process signalled is start-verified, not just the target.** Children are
re-derived from the fresh tree, which is right — but a child that exits and has its
PID recycled between that rebuild and its turn in the sweep would otherwise be
signalled unverified. Each process carries the `Start` from the rebuild, and each is
re-checked immediately before it is signalled. Deepest-first ordering does not help
here and was never meant to: it exists so a parent does not exit and reparent its
children mid-sweep.

**2. The KILL tier is verified, not "whatever remains".** After the grace period,
survivors are **not** determined by PID liveness. A subtree PID that exits during
the 3 s grace and is recycled would be a wrong-process kill inside the mechanism
that exists to prevent wrong-process kills — the sharpest edge in this design.
Each survivor is re-checked against the `Start` recorded at TERM time, and anything
that fails is dropped from the sweep rather than escalated.

**3. The validate-to-signal gap is closed by pinning identity, where the platform
allows it.** Checking a PID table and then signalling a number leaves a window of
milliseconds — small, but Windows recycles PIDs aggressively, and this is the one
path where milliseconds are worth closing:

- **Windows:** `OpenProcess` first, verify `GetProcessTimes` **on the handle**, then
  `TerminateProcess` **the handle**. The handle pins the identity; the window shuts
  completely. There is no graceful tier — a genuine platform difference, and the
  confirm text says so on Windows rather than promising a graceful stop that cannot
  happen.
- **Linux:** `pidfd_open` + `pidfd_send_signal`, which pins identity the same way.
  Falls back to verify-then-`kill(2)` when `pidfd_open` is unavailable (pre-5.3), a
  fallback that must be exercised in tests rather than assumed dead.
- **Darwin:** no equivalent primitive. Verify-then-signal, with the residual window
  documented rather than hidden.

The 3 s escalation runs on its own goroutine and never blocks the IPC handler; the
response returns after the TERM sweep, reporting how many were signalled.

### Confirm

Routes through the existing confirm dialog with its **own** `confirmKind`, and that
kind gets an explicit case in the confirm renderer. The previous attempt fell
through to `default:` and rendered `Close kill-process "…"?` with a footer promising
Enter while the handler required `y` — a recorded review finding.

The confirm names the process, its PID, and the pane it belongs to.

## Dialog

Replaces F1 → Memory in place: the menu keeps ten rows and row 3 is relabelled.

```
Processes                                          host: local

  QUIL              VERSION   UPTIME    PID
    TUI             1.62.6     2h 14m   3120
    daemon          1.62.6     6d 03h   2044
    bridge  ⚠       1.62.4     2d 11h   5044   quil.exe.old.3
    bridge          1.62.6     1h 02m   6188
    1 unidentified client — predates this feature

  WORKSPACE                        MEM      CPU
  Total                         5.2 GB      41%
  ▾ dev                         4.9 GB      39%
     ▾ api · zsh                4.1 GB      38%   4812   — not killable
          node vite build       3.8 GB      36%   5219
          esbuild               190 MB       1%   5301
     ▸ agent · claude           812 MB       2%   4820   — not killable
  ▸ infra                       244 MB       —
  TUI-local                      18 MB

  r refresh · enter/←→ expand · k kill · esc close
  Darwin reports CPU as a kernel average, not usage over the sample window.
```

Behaviour, each item resolving a recorded finding from the previous attempt:

- **Cursor is dialog-scoped.** A report arriving after the dialog closed must not
  move another dialog's cursor. Guarded on `m.dialog == dialogProcesses`, following
  `applyMemoryReport`'s existing guard.
- **Refresh is single-flight, and the flight times out.** An in-flight request
  suppresses another, matching the daemon-backed browse and discover dialogs — which
  also bound the wait (8 s), and that half is not optional here. The renewal that
  keeps the collector alive rides these same requests, so a single-flight without a
  timeout means one lost response freezes refresh *and* starves the collector
  deadline: the dialog would sit showing stale numbers with no way back short of
  closing it. The timeout releases the slot and the next tick recovers both.
- **Esc from the kill confirm returns to the process dialog**, agreeing with its own
  accept path.
- **Every remote string renders through `sanitizeRemoteText`.** In remote mode
  process names come from a machine the user may not control; this is the trust
  boundary `remote-dialogs.md` documents.
- **Row width is budgeted.** Every row is built to the dialog's content width; the
  previous attempt emitted 89-cell rows against an 86-cell budget, so every row
  would have wrapped and doubled the dialog height.
- **Staleness is visible.** Past 30 s the header says so.

### Multi-daemon scope

Primary destination only, following the memory dialog's existing precedent. The
header renders `host: <name>` so a multi-host workspace shows *which* machine is
being reported rather than leaving it implicit. Aggregation across destinations is
a tracked follow-up, not a silent gap.

## Migration off the memory dialog

The MCP tools are genuinely unaffected: `get_memory_report` and `get_pane_memory`
ride `MsgMemoryReportReq` from the bridge into `handleMemoryReportReq` and never
touch `internal/tui/memory.go`. `MsgMemoryReportReq/Resp` and
`internal/memreport` are left exactly as they are.

The **status bar** is affected, and this is the coupling the first pass of this
design missed. `renderStatusBar` reads `m.lastMemResp.Total + m.tuiLocalMemTotal()`,
fed by a permanent 5 s `memoryTickCmd` armed in `Init` — independent of any dialog.

Migration:

1. The status bar reads the new resource report's workspace total.
2. `tuiLocalMemTotal` survives as the `TUI-local` row and as a status-bar term.
3. `memoryTickCmd` is removed; the status bar is fed by the resource report.
4. **The replacement tick preserves `skipRender`.** `memoryTickMsg` currently sets
   `m.skipRender = !prologueChangedView` because the tick is *inert* — it only builds
   a Cmd that sends an IPC request, and the reply is what renders. Dropping that line
   makes a 5 s timer repaint the entire frame forever, silently undoing part of the
   frame-rebuild work merged in #175. The new tick carries the same line and the same
   reasoning.
5. **The command palette migrates with it.** `palActMemory` (`palette.go`) registers
   a "Memory report" command that opens the dialog and calls `refreshMemory()`.
   Leaving it behind would ship a palette entry that opens a deleted dialog.
6. **Docs and rules migrate with it**: `docs/features.md`, `docs/keybindings.md` and
   `.claude/rules/tui-dialogs.md` all name `F1 → Memory` / `dialogMemory`.
7. `memory.go`'s tree, rows and rendering are deleted, along with its tests.

**The F1 row is renamed, not removed.** Memory is index 3 in an index-addressed
menu whose order is pinned by a comment in `dialog.go`, with `aboutUpdateIndex = 7`,
`aboutWhatsNewIndex = 8` and `aboutStopDaemonIndex = 9` as named constants below it.
Relabelling row 3 and repointing its case leaves all three untouched; *adding* a row
would have shifted every one of them, including the destructive shutdown-confirm
routing.

**Three consumers keep riding the old pair, indefinitely.** `get_memory_report` and
`get_pane_memory` (MCP), plus `quil status`. `MsgMemoryReportReq/Resp` and
`internal/memreport` are therefore retained permanently, not deprecated — two report
systems coexist by design. The alternative is migrating an MCP tool's stable output
shape and a CLI command for tidiness, which is not worth it.

Because the collector is gated on the dialog, the status bar needs a total when the
dialog is closed. **The daemon's `MsgResourceReportResp` therefore always carries
pane totals**, which come from `memreport`'s always-on collector; only the process
trees and CPU require the gated collector. The status bar's tick asks for totals
only, and the dialog's asks for trees as well — one message, a `WithTrees bool`
request field.

This keeps one client-side tick, and keeps the fat frame (trees for every pane) off
the wire whenever the dialog is closed — relevant because `daemon-lifecycle.md`
records the 64-slot must-deliver queue as the failure shape for oversized frames.

## Testing

**`internal/proctree` — pure, therefore fully testable on Linux CI.**

- `Build`: descendant trees from a synthetic table; depth assignment; multiple
  roots; a root with no children; a missing root.
- `Build` rejects a link whose child `Start` precedes its parent's — the PID-reuse
  splice, asserted as a *tree shape*, not a helper's return value.
- `Build` keeps a link when either `Start` is unknown, and the kill path refuses it.
- `Build` terminates on a cyclic parent map. Table-driven with a self-parent, a
  two-cycle and a three-cycle.
- `Sampler`: percent from two cumulative samples with an injected clock; `-1` on a
  first sample; `-1` when `Start` changed between samples; `-1` on a negative delta;
  per-core percentages above 100%.
- Linux start-time arithmetic at 3-year and 10-year uptimes — the recorded overflow.

**Kill validation — daemon-side, table-driven over the ordered checks:** target
absent from the tree; target at `Depth == 1`; `Start` mismatch; `Start` unknown;
valid target. Each asserts the *outcome*, including that nothing was signalled.

**Kill signalling — the three windows, each asserted separately**, because they fail
independently and the first version of this design closed only the first:

- a child whose `Start` changes between the tree rebuild and its turn in the sweep is
  **not** signalled;
- a survivor whose `Start` changes during the 3 s grace is **not** escalated to KILL
  — the wrong-process kill inside the safety mechanism;
- the Linux `pidfd_open` fallback path is exercised, not assumed dead.

**Darwin `lstart` parsing**, which is the whole platform's `Start` and therefore its
whole kill path: a comm containing spaces (`Google Chrome Helper`) keeps its full
name, and `Start` parses to a real time rather than zero. A test asserting zero
`Start` never appears would have caught the omission this spec shipped in its first
draft.

**Windows two-pass tree**: a tentative link rejected once start times arrive; the
candidate handle count stays bounded by the descendant set rather than the table.
Windows-only, so it runs natively, not in CI.

**Unidentified count**: a conn younger than the age gate is not counted; an old conn
without a hello is. Pinned because the failure is a confident false statement, not a
crash.

**Single-flight timeout**: a request whose response never arrives releases the slot,
and the next tick both refreshes and renews the collector deadline.

The signalling itself goes through an injected seam (`killPID func(int) error`) so
the sweep order, the escalation timer and the "nothing signalled on refusal"
property are all testable without a real process.

**Explicitly pinned, because the previous attempt's kill path was entirely
untested — disabling its confirm branch left the whole suite green:** a test drives
the confirm through `Update` end to end, asserting the request reaches the daemon
with the right pane, PID and start time. Testing the handler function directly is
what let the previous version pass while unreachable from its call site.

**Dialog:** cursor untouched by a report arriving with another dialog open;
single-flight suppression; Esc from confirm returns to the process dialog; every row
within the content-width budget across the geometry matrix; `—` rendered for `-1`;
the staleness notice past 30 s; the unidentified-clients line.

**Hello:** payload round-trips; an old client omitting it still attaches; a re-dial
re-sends; probe dials do not register; `helloByConn` is cleared on disconnect.

**Collector gating**, with an injected clock: a `WithTrees` request starts it; a
status-bar request does not; repeated `WithTrees` requests keep it running; it stops
after the deadline lapses; and — the property the deadline exists for — a client
that disconnects without any close message still lets it stop. That last case is
asserted directly, because it is the one an explicit close message would fail.

**Windows-only code** is compiled by `dev.sh build`'s six `GOOS=windows` builds and
exercised by a natively-run test binary (`go test -c` in Docker, run the `.exe` on
the host) — CI cannot reach it, so it must be run deliberately.

## Prior findings incorporated

Each recorded finding against the removed dialog, and where it is resolved:

| Finding | Resolution |
|---|---|
| Confirm had no renderer case, fell to `default:` | Own `confirmKind` with an explicit case |
| Esc from confirm returned to `dialogNone` | Returns to `dialogProcesses` |
| Scan result wrote `dialogCursor` unguarded | Guarded on the active dialog |
| Refresh had no single-flight | Single-flight, per the browse/discover precedent |
| `processLabel` skipped `sanitizeRemoteText`, unbounded | All remote strings sanitized; no command lines carried |
| `killProcess` rebuilt rows by hand, dropping the sort | Kill validates daemon-side against a fresh tree; no client-side row rebuild |
| Linux `starttime` overflow past ~2.9 years | Widened arithmetic + regression tests |
| Rows 89 cells against an 86-cell budget | Width-budgeted rows, tested across geometries |
| Kill path entirely untested | Driven through `Update`; validation table-driven |
| `isQuilTUI` subcommand loop untested | Classification removed entirely — identity is self-reported |

## Sequencing

This is not one pull request, and shipping it as one would put the kill path — the
highest-risk code here — into the same review as a dialog migration.

**PR1 — the dialog, read-only.** `internal/proctree`, the gated collector,
`MsgResourceReportReq/Resp`, the dialog replacing F1 → Memory, and the full
status-bar / palette / docs migration. Coherent and useful on its own: it answers
"what is eating this machine" and shows the PID. The migration belongs here, while
`memory.go`'s tests still exist to compare against.

**PR2 — the quil section.** `MsgClientHello`, the identity table, the stale marker,
the age-gated unidentified count. Touches no part of PR1's collector.

**PR3 — the kill path.** Its own review, on top of a dialog already in use. The
Windows two-pass start-time work, the three signalling windows and the platform
pinning primitives are all here.

Each PR is independently shippable and independently revertible. PR1 without PR2
shows pane trees and no quil section; PR1 without PR3 shows a runaway and its PID
and lets the user kill it themselves, which is most of the value.

## Risks

1. **Kill correctness.** Highest severity. Mitigated by daemon-side re-derivation,
   the `Start` match, and refusing on any unknown — but a mistake here kills the
   wrong process on the user's machine.
2. **Windows code is CI-invisible.** Enumeration, CPU and kill all have Windows
   implementations that Linux CI never compiles. Requires deliberate native runs.
3. **Darwin CPU semantics differ.** Documented in the dialog, but a user comparing
   a Mac against a Linux host will see numbers that are not comparable.
4. **Status-bar migration.** Touches `Init` and `renderStatusBar`, both hot paths in
   a file with recent performance work.
5. **Scope.** Three platform enumerations, three CPU sources, a new IPC pair, a kill
   path, a dialog, and a migration off an existing dialog — split across three PRs
   per *Sequencing*. The risk is no longer the size but the temptation to recombine
   them.
6. **Darwin depends entirely on `ps` output format.** Enumeration, start times and
   CPU all parse `ps`. A format difference across macOS versions degrades the whole
   platform at once, and neither the author nor CI runs it.

## Follow-ups, deliberately out of scope

- Aggregating across connected destinations.
- Sorting or filtering the tree (by CPU, by memory).
- Any action on quil's own processes.
- Historical CPU (a sparkline needs retained samples).
