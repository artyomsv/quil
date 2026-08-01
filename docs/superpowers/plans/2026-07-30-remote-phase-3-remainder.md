# Remote Daemon Phase 3 — Remainder

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Supersedes** the unfinished half of `2026-07-29-remote-phase-3-remote-correct-ui.md`. That plan was written before Phase 2 shipped; its Task 1 and half of Task 3 are now merged, and both landed differently from the text. Read this file, not that one, for what is left. The original stays for its Task 2 code sketches, which Task 1 here revises rather than repeats.

**Registry:** RD-020 (TUI half), RD-021 (remainder), RD-022 … RD-028.

**Goal:** Every surface that reads a filesystem or probes a binary reads the *server's*.

---

## What already shipped (2026-07-30)

| Item | State | Departures from the old plan |
|---|---|---|
| RD-020 daemon | **done** | sort **before** cap (else 500 files and no folders); timeout bounds the `ReadDir` **syscall**, not the result loop; symlinks/junctions `Stat`ed; `browseDirResponse` takes the fallback CWD as a parameter; request gained `Child` so the **daemon** joins paths |
| RD-021 Alt+G | **done, confirmed live** | shipped alone, not paired with kube; names are `MsgGitReposReq/Resp`, payload field `Repos`, guard `gitDiscovering` — the old plan's `MsgGitRepoReq`/`Candidates`/`gitScanning` do **not** exist |

**Revision points, now answered.** (1) Staleness is `Model.clientGen` from RD-015 — reuse it, do not add a second mechanism; `freezeInput` already swallows every `tea.KeyPressMsg`, so dialog keys are frozen with no extra work. (2) Timeout is **8 s** (`gitScanTimeout`), chosen for a first ssh round trip after idle and proven against the VM — match it, do not re-derive. (3) RD-026/027 remain **unanswered**; they are Tasks 6–7 and still gated.

---

## Global Constraints

- Module `github.com/artyomsv/quil`, Go 1.25. Docker-only: `./scripts/dev.sh test|vet|test-race`.
- Production isolation: never touch `~/.quil/`; dev work uses `./quil-dev.exe`.
- Commit subjects imperative, ≤72 chars, cite the RD id. No AI/model/vendor attribution.
- **Every response echoes its request key verbatim.** Never cleaned or normalised — the echo is the client's staleness check.
- **Every handler doing I/O runs on a worker goroutine behind its own single-flight atomic.** Its *own*: the setup dialog resolves a directory, then scans it for repos, then lists sessions. A shared guard makes each step fail exactly when it follows another.
- **Responses cannot exceed `maxFrameSize` by construction** — cap counts, set `Truncated`.
- **A failure is never reported as an empty finding.** "Scan timed out" must not render as "no repositories here". This is the phase's whole thesis: a confidently wrong answer about the wrong machine.
- Local mode uses the same RPC path. A path exercised only by remote sessions rots.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/tui/browse_client.go` | **new** — request/apply/timeout for the directory browser |
| `internal/tui/dialog.go` | setup dialog becomes async (the risky change) |
| `internal/daemon/browse.go` | gains drive-root enumeration + path validation |
| `internal/daemon/discover.go` | gains the kube handler beside the git one |
| `internal/daemon/plugins.go` | **new** — availability from the daemon's registry |
| `internal/config/config.go` | `RecentCWDsPath(dest)` |
| `cmd/quil/main.go` | `buildAttachPayload` |

---

## Task 1 (RD-020b, RD-028, RD-021 remainder): Async setup dialog

**The risky task, and today made it riskier than the old plan assumed.** Four behaviours of the local browser must survive the move, and two of them have no server-side equivalent yet.

Read `dialog.go:2284-2394` (`initSetupBrowser`, `loadBrowseDirAndSelect`, `loadDriveList`) and `dialog.go:2720-2849` (`handleSetupCWDKey`) before starting.

**The four hazards:**

1. **`initSetupBrowser` retries synchronously.** It walks `[lastSelectedCWD, activePaneCWD, home]` and falls to the next on error. Async, that becomes a chain: request candidate *i*, and on an `Error` response request *i+1*. Keep the list in state; do not collapse it to one request.
2. **Windows drive list has no remote equivalent.** `loadDriveList` stats `A:\`…`Z:\` **locally**. Against a Linux daemon there are no drives, and against a Windows daemon the letters are the *server's*. Solve server-side (Step 3 below), not with a `runtime.GOOS` branch in the TUI — the TUI's GOOS is the wrong machine's.
3. **`validateAndNormalizeCWD` stats locally** (`dialog.go:3215`). It is reached by typed input *and* by `ctrl+v` paste. Keep the pure half (trim, unquote, `~` expansion, `Abs`) client-side; the **existence check must move** to the browse response's `Error`. Today pasting a valid remote path fails — that is the bug, not a safeguard.
4. **Height must not change** between pending and loaded. `TestRenderSetup_PickFocusChange_HeightStable` exists because this was got wrong before. Render a fixed row count, blanks while pending, one dim `Loading…` on row 0. **Do not collapse the field.**

**Files:** create `internal/tui/browse_client.go`, `internal/tui/browse_client_test.go`; modify `internal/tui/dialog.go`, `internal/tui/model.go`, `internal/daemon/browse.go`, `internal/ipc/protocol.go`.

- [ ] **Step 1: Extract the decision half first, as a no-op refactor.**

This is how RD-021 was landed safely: split before making it async, so the existing tests keep passing and prove the split changed nothing. Pull the state-mutating tail of `loadBrowseDirAndSelect` into

```go
// applyBrowseListing fills the browser from an already-resolved listing.
func (m *Model) applyBrowseListing(resolved, parent string, entries []ipc.BrowseEntry, selectName string)
```

leaving `loadBrowseDirAndSelect` to call `os.ReadDir` and hand results to it. Run `go test ./internal/tui/` — **everything must still pass with zero test edits.** If anything fails, the split is wrong; fix it before Step 2.

- [ ] **Step 2: Commit the refactor.** `refactor(tui): split the browser listing from its filesystem read`

- [ ] **Step 3: Add drive-root enumeration to the daemon.**

In `internal/ipc/protocol.go`, extend the existing response:

```go
type BrowseDirRespPayload struct {
    // ... existing fields ...
    // Roots lists the filesystem roots when Resolved IS one, so the client can
    // offer "up" from a drive root without knowing the server's platform. On
    // Unix a root has nothing above it and this stays empty; on Windows it
    // carries the drive letters. The TUI must not enumerate drives itself —
    // its runtime.GOOS describes the machine drawing the picker, not the one
    // holding the disk.
    Roots []string `json:"roots,omitempty"`
}
```

In `internal/daemon/browse.go`, populate it when `out.Parent == ""` (the existing root test), via a build-tagged `filesystemRoots()` — `browse_roots_windows.go` stats `A:\`…`Z:\`, `browse_roots_other.go` returns nil.

Tests: a Unix root reports no `Roots`; a non-root directory reports none either.

- [ ] **Step 4: Move existence validation server-side.**

Split `validateAndNormalizeCWD` into `normalizeCWDPath(raw) (string, error)` (pure: trim, unquote, `~`, `Abs` — no `os.Stat`) and keep the stat only in a local-only caller if one remains. The dialog's submit and paste paths use the pure half, then rely on the browse response's `Error` for existence.

Test: `normalizeCWDPath("/home/artyom/homelab")` succeeds **on Windows**. That is the exact case that fails today.

- [ ] **Step 5: Write the failing client tests.**

```go
// A response for a directory the user has already left is dropped.
func TestApplyBrowseDir_StaleResponseIgnored(t *testing.T)
// Both halves of the key must match — two descents from one directory
// differ only in Child.
func TestApplyBrowseDir_StaleChildIgnored(t *testing.T)
// The candidate chain advances on error rather than giving up.
func TestInitSetupBrowser_FallsToNextCandidateOnError(t *testing.T)
// ...and stops, rather than looping, when every candidate fails.
func TestInitSetupBrowser_AllCandidatesFail_ShowsError(t *testing.T)
// A failure is not an empty directory.
func TestApplyBrowseDir_ErrorSurfaces(t *testing.T)
// The invariant that has been broken before.
func TestRenderSetup_BrowsePendingHeightStable(t *testing.T)
```

- [ ] **Step 6: Implement the client half** in `browse_client.go`, mirroring `discover_client.go` exactly (it is the reference implementation now): `browseState{path, child, pending}`, `requestBrowseDir`, `applyBrowseDir`, `applyBrowseTimeout`. Timeout **8 s**, matching `gitScanTimeout`.

- [ ] **Step 7: Replace the call sites** in `initSetupBrowser` and `handleSetupCWDKey`. Descent sends `Child`; "up" sends the response's `Parent`. **Never `filepath.Join`/`filepath.Dir` on a remote path** — that was the RD-020 server-join fix and it is undone the moment the client computes one.

- [ ] **Step 8: Wire dispatch.** `listenForMessages` case + an `Update` branch that **re-arms `m.listenForMessages()`**; the timeout branch must **not**.

- [ ] **Step 9: Point the setup dialog's git pick list at the RPC** (RD-021 remainder). `enterSetupOrSplit` (`dialog.go:2217`) still calls `gitdiscover.Candidates` locally. Reuse `requestGitRepos`/`applyGitRepos` — no new plumbing.

- [ ] **Step 10: Full `internal/tui` suite after every step**, not just at the end.

- [ ] **Step 11: Commit.** `feat(tui): browse the daemon's filesystem in the setup dialog` — RD-020, RD-021, RD-028.

---

## Task 2 (RD-022): Kube context RPC

Structurally identical to the shipped git handler. Copy `internal/daemon/discover.go`'s shape; do not invent a new one.

**Interfaces:**
```go
const (
    MsgKubeCtxReq  = "kube_ctx_req"
    MsgKubeCtxResp = "kube_ctx_resp"
)
type KubeCtxReqPayload  struct{}
type KubeContextInfo struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace,omitempty"`
}
type KubeCtxRespPayload struct {
    Contexts []KubeContextInfo `json:"contexts"`
    Current  string            `json:"current,omitempty"`
    Error    string            `json:"error,omitempty"`
}
```

The request is empty, so there is **no echo key** — it is CWD-independent and cannot go stale by directory. It can still arrive after the dialog closed, so guard on the dialog being open rather than on a key.

- [ ] **Step 1:** failing tests — current-context marker survives the round trip; cap at `maxKubeContexts` (50) with the cap enforced daemon-side; a missing kubeconfig is an empty list with **no** error (that is a real finding), while an unreadable one **is** an error.
- [ ] **Step 2:** run, verify failure.
- [ ] **Step 3:** implement in `internal/daemon/discover.go` beside the git handler, own `kubeDiscovering` atomic.
- [ ] **Step 4:** replace the TUI call site (kube field population in `dialog.go`).
- [ ] **Step 5:** tests; **Step 6:** commit — `feat(daemon): serve kube context discovery over IPC` (RD-022).

---

## Task 3 (RD-023): Plugin availability from the server

**Today's evidence:** lazygit had to be installed on *both* machines. The VM had it; the TUI greyed it out because the laptop did not — until it was installed locally too. Conversely a tool present only locally is offered and then fails to spawn.

**Blast radius is known exactly.** `.Available` is read in `internal/tui` only — `ctxmenu.go:86`, `dialog.go:1098,1202,1433,1825`, `overlay.go:77,119`, `palette.go:274` — and set in `internal/plugin/registry.go`. **The daemon never gates spawning on it** (verified 2026-07-30), so this is presentation-only and cannot break spawn.

```go
const (
    MsgPluginListReq  = "plugin_list_req"
    MsgPluginListResp = "plugin_list_resp"
)
type PluginInfo struct {
    Name      string `json:"name"`
    Available bool   `json:"available"`
    Homepage  string `json:"homepage,omitempty"`
}
type PluginListRespPayload struct{ Plugins []PluginInfo `json:"plugins"` }
```

- [ ] **Step 1:** failing test — the daemon reports from its own registry; an absent plugin returns `Available:false` with `Homepage` intact so the greyed row still links somewhere.
- [ ] **Step 2:** run, verify failure.
- [ ] **Step 3:** implement in `internal/daemon/plugins.go`. No single-flight: detection is already cached in the registry at load. Invalidate on plugin reload.
- [ ] **Step 4:** consume in the TUI, once per `Ctrl+N` open. **On timeout or error, fall back to the LOCAL check** — a wrong grey-out silently hides a working tool, while a wrong offer fails loudly at spawn. Fail toward the loud error.
- [ ] **Step 5:** tests; **Step 6:** commit — `feat(daemon): report plugin availability from the server` (RD-023).

---

## Task 4 (RD-024, RD-025): Per-target recent CWDs, honest attach payload

Both are local state that silently describes the wrong machine. Take the old plan's Task 5 **as written** — its tests and `RecentCWDsPath(dest)` sanitisation design are unaffected by today's work. Two notes:

- `defaultCWD()` already validates with `os.Stat` + `EvalSymlinks` and falls back, so today's behaviour is safe **by coincidence**. A laptop path that happens to exist on the server is where the coincidence stops.
- Sanitisation matters: the destination is user input reaching a filename. Replace everything outside `[A-Za-z0-9._-]` and append a short hash so distinct destinations stay distinct after collapsing.

- [ ] Steps 1-5 per the old plan's Task 5. Commit message there is still accurate.

---

## Task 5 (RD-026): `quil status` over the transport — **BLOCKED**

> **Decision required.** (a) support `quil status --remote <host>`; (b) keep it refused and document `ssh <host> quil status`.
>
> Recommendation **(a)**, and it is cheap — the daemon already answers a version handshake. The argument is `--json`: a script that gains `--remote` and keeps reporting on the wrong machine is a live failure mode, and (b) prevents it only while the guard is remembered.

If (b): close RD-026 as `dropped`, keep the guard, skip to Task 6.

---

## Task 6 (RD-027): Update controls in remote mode — **BLOCKED**

> **Decision required.** (a) label them ("remote daemon update available", apply disabled with a reason); (b) target them — apply drives `quil remote setup <dest>`.
>
> Recommendation **(b)**: that machinery shipped in v1.44.0, so it is wiring, not new capability. (a) is a strict subset — do it first if the phase runs long.

---

## Verification (whole phase)

- [ ] `test`, `vet`, `test-race` green; Windows native suites per the `go test -c` workflow.
- [ ] **Manual, local — matters most.** Every dialog behaves exactly as before. The RPCs are on the local path too, so a regression here hits every user, not just remote ones.
- [ ] **Manual, remote** (VM `artyom@192.168.6.12`, both ends must run the same build):
  - `Ctrl+N` offers **server** directories; `/home/artyom/homelab` is reachable by browsing *and* by paste
  - git discovery finds server repos in the setup dialog, as Alt+G already does
  - kube contexts come from the server kubeconfig
  - a plugin installed only on the server is offered; only locally, greyed
  - a pane created in a server directory spawns there — **without hand-framing `create_pane`**, which is the test that Phase 3 is actually done
- [ ] Update `docs/roadmap/remote-daemon.md`: RD-020…028 statuses; delete the "Filesystem dialogs read the local disk" and "Plugin availability is decided locally" limit rows; record the answers to open questions 1 and 2.

## Notes

- **Deliberate omission.** Notes and the F1 log viewer stay local per the roadmap. Open question 3 (notes daemon-side) is unassigned.
- **Reference implementation.** `internal/daemon/discover.go` + `internal/tui/discover_client.go` are the shipped, live-confirmed pattern. New RPCs copy them rather than the old plan's sketches.
- **Largest risk is still Task 1**, and more so than the old plan said: the drive-list and paste-validation hazards were not in it. Steps 1-2 exist to de-risk it — the extract-then-swap sequence is what made RD-021 land without breaking a single existing test.
- **Sequencing.** Task 1 unblocks the rest (RD-024/025 depend on it, RD-021's remainder is inside it). Tasks 2-3 are independent and can run in parallel. Tasks 5-6 need answers before any code.
