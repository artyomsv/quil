---
description: Update checking, staging, and the rename-aside apply/rollback swap. Load when touching the update package or the apply path.
paths:
  - "**/internal/update/**"
  - "**/cmd/quil/update_apply.go"
  - "**/internal/daemon/update.go"
  - "**/internal/tui/update.go"
---

# Auto-update

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## Auto-update

### Auto-update

`internal/update/` (stdlib-only: GitHub `releases/latest` checker, sha256-verified download, extract to `$QUIL_HOME/update/staged/<ver>/` with `manifest.json` written LAST as the atomic completion marker; daemon-owned `state.json`, TUI-owned `notified.json` and `lastrun.json` — single writer per file; `notified.json` is a version the user was TOLD ABOUT while `lastrun.json` is one they RAN, which is why the What's New dialog cannot reuse the former). The whole pipeline is compiled off for dev/debug builds via `internal/version.SetUpdatesEnabled`/`UpdatesEnabled()` (ldflag sink `main.buildUpdatesOff`, set to `"true"` for both `F_DEV` and `F_DBG` in `scripts/dev.sh`) — without this, a staged release applied to `quil-dev.exe`/`quild-dev.exe` would strip the baked-in `buildDevMode` ldflag and the next launch would silently attach to production `~/.quil`. Daemon: `updateChecker` goroutine (`internal/daemon/update.go`, 1 min after listen then every 24 h; gated on `[update] check` + `version.IsRelease()` + `version.UpdatesEnabled()`) publishes `ipc.UpdateInfo` under the broadcast-only `update` key of `workspace_state`; the exe-dir writability probe gates STAGING only (`[update] auto`'s background stage, and the on-demand path), not the checker goroutine itself. `MsgStageUpdateReq` (on-demand stage, worker goroutine — never on the dispatch goroutine, single-flight via `updateStaging` atomic) re-checks GitHub even when nothing is stageable and answers `Error: "already up to date"`; the TUI's About row uses this to make "Check for updates" trigger a real check instead of repeating stale broadcast state. TUI (`internal/tui/update.go`): status-bar `↑ vX [ready]` segment, dynamic About row (`aboutUpdateIndex`, label becomes "Updates disabled (dev build)" when the pipeline is off; What's New at index 8, Stop daemon at index 9), update-notice dialog gated to the FIRST `WorkspaceStateMsg` per attach (`Model.sawFirstState` — every later broadcast in the session, e.g. switch tab, also carries the update key and would otherwise reopen it), `confirmKindApplyUpdate` quits with `Model.applyUpdateOnExit`. Settings dialog (`internal/tui/dialog.go:settingsFields`) exposes `[update] check`/`auto` as two boolean rows (apply on next daemon restart). Apply (`cmd/quil/update_apply.go`): verify manifest hashes → `[Y/n]` prompt (skipped when pre-confirmed) → resolve `os.Executable()` exactly ONCE before any swap (Linux's `os.Executable()` is a live `readlink /proc/self/exe`; re-resolving after `swapOne` renames the running binary aside would return the `.old` path and respawn the old binary) → rename-aside swap (`<bin>.old` backup, pair-rollback) → respawn self (at the pre-resolved path) as wrapper with `QUIL_UPDATE_RESTART=1`, which makes the version gate skip its own restart prompt; backups + staged dir cleaned (`removeBackups`) on the next launch where versions match. The backup slot comes from `freeBackupPath`, NOT a hardcoded `<bin>.old`: a backup stays undeletable for as long as a live process still runs it as its image (an orphaned daemon, an AV handle), and Windows refuses DELETE on such a file — which fails `os.Remove` AND the `MOVEFILE_REPLACE_EXISTING` rename behind `os.Rename`, both with "Access is denied". So the canonical slot is reused when it can be cleared and otherwise falls back to `<bin>.old.1`, `.2`, … (cap `maxBackupSlots`); `swapOne` returns the slot it used so `swapPair`'s rollback restores the right file. Without the fallback one leftover wedges EVERY later update, since nothing ever clears it. A confirmed "Update now" that fails to apply prints one stderr line (`cmd/quil/main.go`) instead of silently falling through to a normal launch

### The broadcast is a LABEL, never the input to an action

`ipc.UpdateInfo` is push-only and the daemon refreshes it 1 min after start then every 24 h, so anything published inside that window is invisible to every attached client. That is fine for the status segment and the About row, which only DESCRIBE; it was wrong for the row's Enter, which used to read `info.StagedVersion == info.LatestVersion` and open the apply confirm directly — installing whatever was newest at the last tick and leaving the user to repeat the whole cycle (apply intermediate → restart → check → stage → apply) to reach the actual latest. The "up to date" branch had already been fixed for this exact staleness one branch over; the staged branch was missed. **Every branch of `handleUpdateAction` that acts now re-checks first**, and the only branch answering from the broadcast (`!InstallWritable`) offers no action.

`MsgStageUpdateReq` therefore means "ensure the LATEST is staged", not "download": `stageOnDemand` re-checks, and when the just-checked release is already on disk it answers `AlreadyStaged` instead of re-downloading an identical ~15 MB archive — the press meaning "apply what is staged" sends the same request, so without that flag the common case would pay a full download to arrive at the same bytes. The disk check is `FindStaged` **plus a full `VerifyStaged` re-hash, not the manifest alone**: a stage whose bytes no longer verify is one `maybeApplyStagedUpdate` discards on sight, so vouching for it would promise an update that then silently does not happen; a failed verify falls through and re-stages, which is the repair. `Success` is true for both outcomes so a client predating the flag still reads it correctly.

TUI side, `Model.pendingApplyVer` is the INTENT of the press: set only by the staged branch, cleared at the top of `handleUpdateAction` (a leftover would make an ordinary download pop an apply confirm) and one-shot in `applyStageUpdateResp`. It decides the response's meaning — a success continues into the apply confirm for `resp.Version` (the version the daemon ACTUALLY has, which is the newer one when it staged one just now), while the same success with no intent stays a flash, keeping the "(download)" row's behaviour. A FAILED check with an intent still opens the confirm for the on-disk version with the reason in `confirmDetail`: the staged bytes are installable and an offline laptop must not be stranded, but it must also not be told the version is confirmed-latest when it is not. `openApplyConfirm` **refuses to replace an open dialog** — it can now fire a whole download after the keypress — and the confirm requires `y` rather than Enter, the `confirmKindShutdown`/`confirmKindUpgradeDest` rule, because a dialog that appears on its own must not be acceptable by the universal commit key.

`MsgUpdateCheckReq` (fired by `openAboutDialog`, which is the F1 key and the palette's About row — NOT the Esc paths that return to About from a sub-dialog) refreshes the announcement without staging. It has **no response type by design**: the answer is the refreshed `update` key on the next broadcast, and a failed check on an offline laptop is a routine non-event that must not surface as a dialog error. `runUpdateCheck` took an `allowStage` parameter for it (the daily tick passes `[update] auto`; this passes false). Its single-flight slot is `updateChecking`, deliberately NOT `updateStaging` — opening About and then pressing the row is the designed sequence, and one shared slot would reject the stage exactly when it followed the check that motivated it (the `browseScanning`/`dirsChecking` precedent). The rate limit is `updateRecheckMinInterval` against the persisted `LastCheckMs` and lives DAEMON-side because every attached client can open a dialog, while the 60 req/hr GitHub budget is per IP.

**A second press while a request is in flight is REFUSED client-side (`Model.updateReqInFlight`), and that guard is the difference between fixing this bug and re-creating it.** While press 1 downloads a newer release, the daemon answers press 2 from `stageRelease`'s CAS ("staging already in progress") in milliseconds — a fast FAILURE, which the apply-intent fallback read as "the check failed" and turned into a confirm for the OLD staged version. Pressing `y` there installs the intermediate release and abandons the download of the newest, which is the exact loop the change exists to end. The guard sits BEFORE the `pendingApplyVer` reset, because a refused press must leave no trace: clearing the intent there would discard the FIRST press's "apply when this answers" and hand the user a flash after they waited out a download. The daemon-side half is `updateOnDemand`, a THIRD single-flight slot — `AlreadyStaged` answers before `stageRelease`'s CAS, so that guard no longer bounds the handler, and each unguarded request costs a GitHub call plus a full re-hash of both staged binaries.

**`CheckFailed` is what narrows the fallback**, and it exists because "the request failed" and "the question is unanswered" are different things. Only an unreachable GitHub licenses installing what is already on disk; a stage that failed, an unwritable install dir, or a request refused by the single-flight all leave the question answered or the work not done, and treating them alike is what produced the regression above. `"already up to date"` with an apply intent (a yanked release) therefore flashes rather than confirming — a confirm reading "Apply update v1.1.0?" over "already up to date" contradicts itself.

**The rate limit is stamped on the ATTEMPT, in memory (`lastUpdateCheckAt`), never read from `state.json`.** `runUpdateCheck` returns before it persists anything when the check errors, so the persisted `LastCheckMs` only advances on SUCCESS — and the failure that matters is GitHub answering 403 because the quota this limiter protects is spent. Pacing on the persisted value switched the limiter off exactly when it was needed (measured: 10 About-opens → 10 requests, `LastCheckMs` still 0). It also keeps the dispatch goroutine free of the `os.ReadFile` the check used to do there, and a NEGATIVE age counts as not-recent so a clock corrected backwards cannot disable checking until real time catches up.

**All three writers of `state.json` go through `mutateUpdateState`** (daily tick, check-only refresh, on-demand stage) under `updateStateMu`, and it is never held across a download — the transfer runs between two critical sections, or an on-demand record would park behind a network fetch. `update.saveJSON`'s fixed `<path>.tmp` is why serialising matters: two savers open it `O_TRUNC` and the renamed file can be invalid JSON, which `LoadState` reports as the zero `State`, silently dropping the announcement (`techdebt/4-1-update-state-json-fixed-temp-path.md`).

**`recordStagedVersion` takes `writable` as a PARAMETER rather than asserting true**: the already-staged branch returns before the `installDirWritable()` gate and never probes, so hardcoding it promised "applies on restart" for an install dir the swap cannot write — and persisted that promise into `state.json`, where `seedUpdateInfoFromState` re-announces it across a restart.

**`update.SafeVersion` is exported and called in `stageOnDemand`**: `Stage` validates the tag before any network I/O, but the already-staged branch never reaches `Stage`, and the string is persisted into `state.json` and broadcast as display text on that path.

**Every render of a version from a stage RESPONSE is sanitized+bounded** (`confirmName` on the apply confirm, the flashes), not just `confirmDetail` — the apply confirm's title was the one field on that dialog skipping the treatment its neighbours get, and it is the consent text for swapping this machine's binaries. `MsgStageUpdateResp` is honoured ONLY from the local daemon (`msg.Origin == ""`, the `MsgLinkLost` gate applied to a reply): `sendStageUpdateReq` never asks anyone else, so an unsolicited one from a remote daemon could otherwise consume the pending apply intent and name its own version.

Both the row and its action REFUSE when the active project's daemon is remote (`remoteModeFor(activeDest())`): the announcement and the stage describe that host's disk while the apply swaps THIS machine's binaries, the same wrong-machine argument `maybeShowUpdateNotice` already made for the startup notice. It matters more now that a success continues into the confirm, which would otherwise name a version this machine never staged. The status-bar segment is gated the same way (`RemoteMode()` alone misses a MIXED session, where the TUI is local but the project on screen is not — it rendered "↑ vX ready" for a release staged on the far host beside an About row correctly saying updates are local)


### The archive has two binary tiers, and only one of them is required

`BinaryNames(goos)` is the REQUIRED set — `extractBinaries` demands every name back and the stage fails
without them. `OptionalBinaryNames(goos)` is extracted when the archive carries it and skipped without
complaint when it does not; today that is `quil-activate.exe` on Windows and nothing anywhere else.

**The helper cannot simply join `BinaryNames`, and the reason is the archives that already exist.** It
entered the release zip with the toast feature (#154) and not the updater, so every release before that
lacks it — folding it into the required set makes all of them unstageable, which turns a downgrade away
from a broken version into a dead end. Optionality here is a statement about release history, not about
how much the file matters. It matters a lot: it is the windowless binary `quil notify setup` registers
as the `quil://` handler, and without it setup silently falls back to `quil.exe activate`, a CONSOLE
binary whose window takes the foreground on every toast click and then vanishes. That fallback routes
correctly, so the failure is invisible — the click works, it just looks wrong, which is why nobody
reported it for eleven days.

**`extractBinaries`' completeness check is per-NAME, never a count.** `files` now also holds whatever
optional entries the archive happened to carry, so `len(files) != len(names)` would pass an archive
holding the helper and no `quild` — the split-pair case the count existed to catch in the first place.

**Verification needs no tier of its own, but ADMISSION does.** `VerifyStaged` checks manifest COVERAGE
against the names the caller passes as required, then hashes every entry the manifest DECLARES — so an
extracted helper is covered by the same tamper gate as the pair, and an absent one declares nothing.
`maybeApplyStagedUpdate` therefore still passes `BinaryNames(runtime.GOOS)` and must not be "fixed" to
pass the optional names too: that would make a pre-helper release fail the coverage check and be
discarded on sight.

**What that leaves is an asymmetry that is easy to miss: `VerifyStaged` never enumerates the staged
directory.** It walks the manifest, so "present on disk" and "verified" are different sets, and a file in
the first but not the second has been hashed by nobody. `installOptional` therefore admits by
DECLARATION — `if _, declared := man.Files[name]; !declared { continue }` — and the manifest is threaded
down through `swapBinaries` and `swapPair` for no other reason. A nil manifest installs nothing, for the
same reason.

**That gate is corruption resistance and consistency, NOT a security boundary — say so when you touch
it.** The comment that originally stood on `installOptional` claimed a file in the staged dir was
verified (false), and the comment that replaced it claimed the gap was exploitable (also false, in the
other direction). Both were written confidently. The fact that settles it: anyone who can write into
`$QUIL_HOME/update/staged/` can equally write `$QUIL_HOME/plugins/*.toml`, whose `CommandConfig.Path` is
"full path to binary (overrides PATH lookup)" and which the daemon loads at startup (`daemon.go:301`) —
arbitrary execution on the next pane spawn, no update or toast click involved. So the whole of
`QUIL_HOME` is one trust domain and the gate crosses no boundary inside it. What it actually buys is
that a helper truncated by a failed download or a bad disk is refused the way the pair already is, and
that the invariant still holds if the optional tier ever carries something OUTSIDE that blast radius —
an installer, a service binary — where it would become load-bearing.

**A failed FIRST install removes its partial file.** `copyFile` opens `O_CREATE|O_TRUNC` before
`io.Copy` and this branch has no backup to rename back the way `swapOne` does, so a failure mid-transfer
strands a usually-zero-byte executable — and `activatecmd.go` admits any non-directory, so `notify
setup` registers that empty file and every later click dies inside `CreateProcess` with no UI. That is a
DEAD handler where the whole point of tolerating the failure was to fall back to the WORKING
`quil.exe activate`, so leaving the partial behind is worse than never having attempted the install.

**`installOptional` runs AFTER `swapPair`'s atomic unit and can never fail it.** The pair swap is the
update; the helper is a convenience worth one console flash. A helper that cannot be written — pinned by
a click handler still running, denied by antivirus, out of disk — is logged and swallowed, because
rolling the pair back over it would report a failed update when the only thing that failed was the
convenience, and the version gate would then find a matched OLD pair with no explanation. This is the
one place in the apply path where an error is deliberately not propagated.

**A REPLACEMENT helper goes through `swapOne`, not `copyFile`.** NT refuses to overwrite an executable
while any process still runs it as its image, and a toast clicked a second ago is exactly that — the
same constraint `freeBackupPath` exists for. A FIRST install has nothing to displace and uses `copyFile`
directly, since `swapOne`'s opening `os.Rename(target, backup)` fails on a target that does not exist —
and that is the ordinary case, because an install which has only ever been upgraded is precisely how the
helper came to be missing. `cleanupAppliedUpdate` sweeps the helper's backups unconditionally rather
than behind the `sameDir` guard the daemon sweep uses: the helper is installed beside `exe` by
construction (`filepath.Dir(quilTarget)`, which is where `notify.ActivateHelperName` is resolved), so
there is no foreign directory to guard against.
