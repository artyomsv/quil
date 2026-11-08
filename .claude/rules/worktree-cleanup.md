---
description: Removing Quil-made worktrees on close (file list, local branch delete), and the record of worktrees and branches that appear while agents run (agentwt). Load when touching worktree removal, the close dialog's worktree rows, or the agent-worktree record and its cleanup view.
paths:
  - "**/internal/daemon/worktree_remove.go"
  - "**/internal/daemon/worktree_status.go"
  - "**/internal/daemon/agentwt*.go"
  - "**/internal/gitworktree/**"
  - "**/internal/hookevents/worktreeprobe.go"
  - "**/internal/tui/worktree_close.go"
  - "**/internal/tui/worktree_rows.go"
  - "**/internal/tui/agentwt_dialog.go"
---

# Worktree cleanup

Moved from `projects.md` (which keeps creation, stages A and B) when that file
reached the rule size limit.

## Removing worktrees on close (stage C)

**`Pane.WorktreeOwned` is the ONLY gate, on BOTH sides of the socket, and it is
what makes a delete offer legitimate at all.** It is set exactly where this
daemon ran `git worktree add` (`createPaneInWorktree`), so `GitWorktree` — which
says only that a CWD sits inside a linked checkout — is the wrong test: a pane
the user opened in a worktree they made by hand satisfies it, and offering to
delete that is a claim about somebody else's directory. `collectConfirmWorktrees`
(client) decides what is OFFERED and `ownedWorktreePaths` (daemon) decides what
is DELETED; the duplication is deliberate, because only the second one is
authoritative and only the first one is visible.

**Ownership says WHETHER; `Pane.WorktreePath` says WHICH — and keying the
removal on `Pane.CWD` instead was a force-delete of somebody else's checkout.**
`WorktreeOwned` is written once, at creation. `CWD` is a live cursor that OSC 7
rewrites on every `cd` (`handleUpdatePane` is the writer, and `gitcache.go`'s
`referencedDirs` exists because of it), so a pane created in `feat-a` whose
shell walks into `feat-b` carries ownership pointing at a worktree this daemon
never made — and `git worktree remove` accepts it, taking the uncommitted work
with it. A pane's own child reaches this with one escape sequence, so it needs
no privileged position, and the ordinary version is a developer who `cd`s
between sibling worktrees. It also broke the dialog's whole safety story: the
client priced one directory with `git status` while the daemon deleted another
(the count and the deletion were two independent reads of a mutable field).
`WorktreePath` is captured at creation, PERSISTED beside the ownership bit, and
read by both sides — so what the dialog prices is what the close removes.

**An owned pane with NO recorded path is refused on both sides rather than
falling back to CWD.** That is the state of every pane restored from a snapshot
written before the field existed. The fallback is precisely the bug, so the
degradation is deliberate: an old pane loses the offer, which costs a
convenience, where the fallback costs a checkout.

**`paneInWorktree` checks the recorded path AND the CWD**, because either alone
misses a real resident: a pane that merely sits there (a shell the user cd'd in)
is only visible via CWD, and a pane CREATED there whose shell has since walked
out — a build still running in the checkout — is only visible via the recorded
path.

**`ownedWorktreePaths` takes `PluginMu` for both reads.** It runs on a conn's
dispatch goroutine, concurrent with the snapshot goroutine and with
`spawnRestoredPane`'s own writes to those fields; `session.go` declares them
PluginMu-protected and an unsynchronised string read can tear, which here means
a truncated path handed to a force-delete.

**The wire carries a BOOL, never a path** (`DestroyPanePayload.RemoveWorktree`,
`DestroyTabPayload.RemoveWorktree`). The daemon re-derives the directory from
its own ownership record, so the only directories this message can reach are
ones this daemon created. A path on the wire would be a recursive-delete
primitive any IPC client could aim anywhere, and the TUI is not the only IPC
client. `omitempty` + absent-means-false is what keeps every existing producer —
the MCP `destroy_pane` tool, the overlay teardown, an older client —
non-destructive without knowing the field exists.

**`gitworktree.RemoveWorktree` is NOT `Remove`, and the difference is the
BRANCH.** `Remove` undoes an `Add` whose pane could not be created: seconds old,
empty, handed to nobody, and its name must be free for the retry — so it deletes
the branch too. This runs on a checkout the user has been living in, whose
branch can hold commits that exist nowhere else. Reusing `Remove` there destroys
them silently, in a dialog whose stated subject is a directory. The two are one
keystroke apart in a diff, which is why the seam is named
`removeWorktreeKeepBranchFn` rather than sharing `removeWorktreeFn`'s name, and
why the load-bearing test is a REAL-GIT one: no stub can observe a branch
surviving.

**The removal runs LAST, on a worker, and after `ensureTabNotEmpty`.** Off the
dispatch goroutine for the reason the add is (that goroutine carries the
client's input, and a checkout deletion can be seconds on a network mount);
after the broadcast so the pane leaves the screen immediately; and after
`ensureTabNotEmpty` specifically, because that is what destroys a tab's hidden
OVERLAY panes — a lazygit overlay sitting in the same worktree would otherwise
still be live when the in-use check runs, and the worktree would be kept for a
pane the user cannot see. Its replacement shell is not a hazard: it spawns in
`d.defaultCWD()`, never in the closing pane's directory.

**A worktree still hosting a LIVE pane is skipped and logged** (`paneInWorktree`
over `AllPanes`, `pathWithin` for the containment). Panes in other tabs can share
one worktree — a second agent on the same branch, a split made in the same
directory — and removing it deletes the working directory out from under running
processes. `pathWithin` compares on a SEPARATOR boundary rather than a bare
prefix, or `/w/feat-a2` counts as inside `/w/feat-a` and closing one pane deletes
a sibling's checkout; it folds case on Windows because a pane's CWD is rewritten
from whatever case OSC 7 reports while the worktree path is the one git created.

**The removal RETRIES, because the pane's own child is reaped
asynchronously.** `DestroyPane` detaches the pane and closes its PTY off-lock
(`releasePanes`) — that is required, since `PTY.Close` blocks until the child is
reaped and doing it under `sm.mu` starves every reader — so the removal
routinely starts while the shell is still exiting, and neither platform releases
a directory a process still holds. Three attempts with a widening backoff:
something that is not exiting (an editor, a file watcher, a shell outside Quil)
will not release it however long anyone waits, and the log line naming it is
worth more than a minute of silence. Every outcome is logged INCLUDING success —
the pane is gone and so is the dialog, so that line is the only record that a
directory was deleted.

**`MsgWorktreeStatusReq` exists because `git status` is the one call the git
ticker deliberately never makes.** `gitinfo` excludes `--porcelain` precisely
because it can take seconds on a large repository, so the change count cannot
ride the 5 s broadcast; it is fetched ON DEMAND when the confirm opens, once,
against the worktrees that dialog would delete. Its own single-flight slot
(`worktreeStatusing`) rather than `worktreeScanning`'s, for the reason
`dirsChecking` is not `browseScanning`: the setup dialog can be listing
worktrees while the close dialog asks about them. ONE shared deadline across the
request's paths, like `dirsExistResponse` — every path blocks on the same thing,
and a per-path budget lets a tab full of worktrees hold the slot N times as long.

**The answer is ADVISORY, and the client timeout is set from that.** (No longer for the DELETE: an unread row keeps its worktree — see "A worktree whose files were not READ" below.) The toggle
works whether or not the count arrives, so `worktreeStatusTimeout` (6 s) gives up
well inside the daemon's own 10 s bound: an early give-up costs a number, never a
decision, while a dialog stuck on `checking…` in front of someone trying to close
a pane reads as wedged. A row that could not be read renders its REASON, never a
zero — `clean` is the one answer that invites the toggle, so a count nobody
obtained must not look like one. Matched on the request GENERATION rather than
the paths: closing one pane and opening the confirm for another within the round
trip produces two wire-indistinguishable requests about the same worktree.

**The toggle is re-cleared on every open and on every route OUT**
(`resetConfirmWorktrees`). `confirmWorktrees` / `confirmRemoveWorktree` /
`confirmWorktreeGen` are Model state that outlives the dialog, so an armed
toggle inherited by the next close is a deletion nobody was offered — the same
shape as the project form's armed merge plan, and the reason that one re-derives
its plan on open rather than trusting a flag.

**The change count includes IGNORED files, and leaving them out was a hole in
the one thing the count exists for.** `git status --porcelain` says nothing
about ignored entries, so a worktree holding a `.env` and a `build/` reported
ZERO — the dialog rendered "clean", the single answer that invites the toggle,
and `--force` deleted both. An ignored file is in no branch, so unlike a
committed change there is nothing to recover it from. `Status` passes
`--ignored` in its TRADITIONAL mode, which respects `-unormal` and collapses an
ignored directory to one entry (a `node_modules` costs one line);
`--ignored=matching` would expand it and walk every file. It also passes
`--no-optional-locks`, because a plain `git status` refreshes and rewrites the
index of a checkout the user may be working in at that moment.

**`MsgWorktreeStatusReq` carries PATHS, and the ownership filter is where the
destroy payload's "a bool, never a path" boundary is enforced for it.** The
request has to name paths — it is asked before the close, about several
worktrees at once — so `Daemon.worktreeStatusResponse` answers only about
worktrees this daemon created and hands every other path back with a reason
rather than a zero count (a zero renders as "clean"). It refuses nothing
legitimate, since the dialog only ever asks about worktrees it was offered. It
matters because `git status` is less read-only than it looks: it runs in a
directory the client named and honours that repository's `core.fsmonitor`.

**A failed `git worktree remove` may already have deregistered the worktree.**
Measured on Windows: git deletes the tree and the admin entry, then fails on the
now-empty directory because the pane's shell still holds it — so it exits
non-zero having done most of the job, and every later attempt answers "is not a
working tree" while an orphaned directory `git worktree list` no longer mentions
is left behind. `removeOneWorktree` therefore asks the LISTING (not the error
text, which is git's to reword) whether the path is still registered, and when
it is not, finishes with `os.Remove` — never `RemoveAll`, so only an empty
directory, because Quil is completing git's operation rather than performing a
recursive delete of its own. That cleanup retries on the same budget the git
call does, since the child that blocked git is still exiting.

**The status answer carries the FILE LIST and the BRANCH FACTS.**
`gitworktree.Status` returns a `StatusReport` (count, the first 200 entries,
truncated flag) from `status --porcelain -z --ignored`: `-z` keeps a name with a
space, a quote or a newline one entry, and a rename's source path is consumed,
not listed. `gitworktree.ReadBranchFacts` reads the branch from git's listing
(never from a client) and the unmerged count against the FULLY-QUALIFIED
default ref. Local refs only, no network, and nothing about remotes. The
default branch is resolved once per repository per
request, and facts are read for the first six worktrees only (the dialog's row
cap), all on the request's one deadline.

**Branch deletes on close are a bool plus CHECK-ONLY names, and LOCAL only.**
`DestroyPanePayload`/`DestroyTabPayload` carry `delete_branch` and
`confirmations` (per worktree: its path and the branch shown). The daemon
reads the facts BEFORE `git worktree remove` (the listing entry naming the
branch goes with it) and deletes only when the branch it read matches the
confirmation for THAT worktree — so a worktree an agent switched to another
branch after the dialog opened keeps that branch, one row cannot confirm
another's, and no client can name a branch to delete. Never the default
branch, and never with the default branch unknown (unmerged commits could not
be counted). The delete is `branch -D -- <b>` (the dialog showed the unmerged
count; git itself refuses a branch checked out in any worktree, which closes
the race with a sibling worktree). Every refusal and failure is a
`worktree_branch_kept` sidebar card carrying the CLOSED pane's id and name
(`paneRef`, captured before the destroy); success is `worktree_branch_deleted`.
All three close paths go through `removeOwnedWorktreesFn` — the tab close used
to call `removeOwnedWorktrees` directly. The MCP/browser `destroy_pane_req`
deletes no branches.

**Quil never deletes a branch on a REMOTE.** It was built and taken out of
this feature before release: seven review rounds each found a new way for the
delete to reach a server other than the one checked — `pushurl`/`insteadOf`
rules, ssh routing, relative paths resolved from another directory, a
remote's own default unknown locally — and a remote delete reaches everyone
who shares the remote. If it comes back it is its own change, starting from
that list.

**A ticked worktree is never removed before its file list loaded.** The
accept arm returns while any row is still "checking…" (`confirmWorktreesLoaded`)
and the footer says `checking…`; the status request's own timeout loads the
rows with a reason, so it never waits forever.

**The dialog's height is measured, not budgeted by constant.**
`confirmFileRows` renders the dialog once with zero file rows (a probe copy
with `confirmFileRowsSet`) and gives the file list what is left of the
terminal, 1 to 10 rows; with several worktrees only the focused one is drawn in
full and the rest take one row each. A short terminal draws fewer rows, but
`←`/`→` still walk EVERY worktree the close removes and the drawn window
follows the focus (`confirmWindow`); the hidden ones are counted on ONE line
so the measured height holds. `TestCloseConfirm_FitsTerminal` asserts the
property at 24 and 30 rows.

## Worktrees that appear while agents run (`agentwt.go`)

The agents' hooks only say WHERE a worktree-related command ran (a
`WorktreeProbe`, see `hooks-and-sessions.md`). `agentWTStore` answers it by
listing that repository (`git worktree list` + `for-each-ref refs/heads`) and
comparing with a per-repository BASELINE it saved the last time it looked, in
`$QUIL_HOME/agent-worktrees.json` (stem `agent-worktrees` is in
`quilHomeStems`, or Windows `ProtectDir` would refuse the folder and the TCP
listener stays off). The compare is the truth and the hook is the trigger: an
alias, a script or a probe that never fired is caught by the compare at the
end of the pane's turn (`turnEnded`, from `emitHookEvent` on a stop-class
edge), which covers every directory the pane probed plus its own CWD.

**First sight is baseline only** — what already exists is attributed to
nobody. A new worktree after that is one entry, attributed to the pane whose
compare saw it, carrying its branch; a new branch with no worktree is its own
entry; the default branch never is. Something that disappears is marked
`Gone`, or dropped when it lived under 60 s (Claude's own subagent worktrees
come and go inside a turn). Quil's own worktree adds (`noteOwnWorktree`, from
`worktreeAddAndCreate`) join the baseline and never become entries.

**An entry says the worktree APPEARED while the pane ran, never that its agent
made it** — a user can make one by hand in another terminal at the same
moment. The cleanup view words it "appeared while <pane> ran"; do not
strengthen the claim.

**No answer never authorises a change.** A `git worktree list` error compares
nothing and marks nothing gone (the `worktreeRegistered` rule). A branch list
that errors or comes back TRUNCATED (`maxBranchList`) skips the branch half
only: reading everything past the cap as gone would later re-record it all as
new. A sandbox pane is never listed — its paths are inside its container.

**Probes go around the hook ingester** (`routeHookPayload`): the coalescer is
last-wins per (pane, event, agent type), so two probes in one 200 ms tick would
lose the first directory, and a probe is no notification. One compare per
directory runs at a time; probes arriving meanwhile cost one more compare
(`running`/`pending`), and two compares of one repository through different
directories may still overlap, so each listing is stamped BEFORE it is taken
and `apply` refuses one older than the baseline's `Seen` — applied second, an
older listing would read a newer worktree as gone. Each compare takes a
`claimBlockingFSCall` permit and a
10 s deadline on a worker goroutine. Bounds: 64 baselines (least recently
seen evicted), 500 entries (gone first). The record is written through
`internal/atomicfile`, debounced 2 s, and flushed at daemon stop.

**The baseline is taken BEFORE a new agent pane's process starts**
(`spawnPane` → `seedSync`, bounded by `agentWTSeedTimeout` = 3 s, no pane lock
held): a listing taken after the agent's first `git worktree add` would adopt
that worktree as pre-existing for good. A failed or slow listing does not hold
the agent back — the spawn goes on, the pane is not marked seeded, and the
turn-start seed retries. An INCOMPLETE read (timeout, no permit, a cut
branch list, a truncated listing, git missing — anything but git ANSWERING
"no repository" (`gitworktree.IsNotRepository` on `ListStrict`)) marks the pane `seedFailed`, and
every repository its compares then see for the first time is baselined
`Uncertain` (persisted): the cleanup view says "may miss items" for it rather
than presenting a baseline that may already hold what the agent made. It is
ENFORCED, not only shown: the daemon refuses a cleanup act in an Uncertain
repository unless the request carries `AcceptUncertain`, and the view's confirm
says so and takes `y` only to send it. A
RESTORED pane seeds off-goroutine (`seedSoon`):
restore runs before the daemon listens, and a resumed agent waits for a
prompt. A pane counts as seeded only after a compare that READ the
repository. A repository the agent first reaches mid-session is baselined by
its first probe, so a worktree made by that very command is not recorded.

**"In use" covers every agent of the pane.** The main agent's last probe
directory, plus one directory per SUBAGENT (`DataProbeAgent`, Claude only),
each stamped with when it arrived. The idle settle (`settleIdle` →
`subagentsDone(pane, idleAt)`, `idleAt` read while idle is confirmed) clears
only directories recorded at or before it, so a next turn's subagent that
probes in between survives. Past `maxAgentWTSubagents` between two settles
the pane FAILS CLOSED (`subOverflow`): it counts as in use for every path
until a settle — or that child's process exiting (`onPaneExitGeneration` →
`subagentsExited(pane, gen)`: records carry the PTY generation that reported
them, and a probe from a newer child first drops an older child's records,
them, so a late exit of an old child never clears a restarted one's) —
clears the mark.

**A gone worktree's surviving branch is queued on the REPOSITORY**
(`agentWTRepo.PendingKept`, persisted), not on the entry — a short-lived
entry is dropped the moment its worktree goes. The queue is settled on the
next COMPLETE branch listing, once: a surviving, non-default branch with no
live entry gets one, with the gone worktree's attribution.

**A worktree whose files were not READ is never deleted** — this reverses
the earlier "the status answer is advisory" rule for the DELETE (the count is
still advisory for closing). A timeout or an error loads the row with a
reason and UNTICKS the row (`disarmUnreadWorktree`); `confirmWorktreesInspected`
(loaded AND no error) still gates Enter, the
worktree row cannot be armed while `worktreeRowReason` names a reason, and a
late answer for the timed-out request is not applied. Unticking closes the
pane and keeps the worktree.

**`AcceptUncertain` needs a full token AND a Quil window.** It stands for a
person pressing `y`: a standard TCP token is refused it in `clientauth`
(`carriesUncertainAck`), and the daemon refuses it from any conn whose hello
role is not `tui` (`uncertainAckRefusal`) — an MCP bridge, the web gateway or
a script has no such view. The role is self-declared: this stops a
cooperating agent from confirming on the user's behalf; a local process
willing to lie already holds the user's own rights over the repository.

**The baseline and `worktreeRegistered` read `gitworktree.ListStrict`**
(through `worktreeListStrictFn`), not `List`: `List` folds every non-zero git
exit — a permission error, an unsafe directory, a corrupt repository — into
"not a repository" `(nil, nil)`, which here would read as a clean answer.
`ListStrict` answers `ErrNotRepository` only when git failed AND no `.git`
entry, no bare layout (HEAD, objects, refs) and no `GIT_DIR` is found in the
directory or any parent, walking BOTH the written and the symlink-resolved
path (`hasGitAncestor`; anything undecidable counts as present) — decided
from the disk, never from git's message, which is localized and which a
broken `.git` indirection shares ("not a git repository: <gitdir>"). Every
other failure is returned as itself. `stubWorktreeList` stubs both seams.

**A subagent record is stamped with the generation of the child that is
STARTING** (`Pane.startingGen`, set under `PluginMu` just before
`ptySession.Start`; `paneForAgentWT` reports `max(ptyGen, startingGen)`):
`ptyGen` moves only after `Start` returns, and a fast child's first hook can
arrive in between.

**The delete checks compare REAL folders** (`paneInWorktree(path, true)` →
`resolvedWithin`): a pane CWD, a pane's recorded worktree and every agent or
subagent probe directory is matched lexically AND with symlinks resolved, so a
resident working through a link to the worktree keeps it. A path that
no longer exists is resolved through its nearest EXISTING ancestor with the
missing remainder re-appended (`resolveNearestAncestor`): a process keeps its
cwd after a rename, so a stale path still names the worktree it was under — but
a DANGLING LINK on the way (the entry is there, its target is not) is
unknown, never a missing component to re-append. All
resolution of one check shares ONE deadline (`resolveTimeout`, 3 s) under the
process-wide filesystem-call limit, and an UNANSWERED resolution (timeout, no
permit, unreadable) counts as in use — a pane on a dead share keeps the
worktree instead of hanging the cleanup slot. The
cleanup view's `in use` hint passes `false`: it runs on a dispatch goroutine,
where a filesystem call may not wait.

**The Uncertain mark does not gate a pane or tab CLOSE, by ruling.** The mark
says the agent record's BASELINE may hold worktrees an agent made (so the view
may miss leftovers); it says nothing about who sits in a folder. A close
deletes only a worktree QUIL created for the closing pane (`WorktreeOwned`),
after the same occupancy check the view uses (`paneInWorktree(path, true)`),
which reads pane folders and agent probe directories, never the baseline.

**The close removes only the CHECKOUT Quil made, not whatever sits at its
path.** At creation `createPaneInWorktree` writes a random token into the
worktree's own git admin directory (`gitworktree.WriteOwnerMark`,
`<common>/worktrees/<name>/quil-owner`) and keeps it as `Pane.WorktreeToken`
(persisted to the DISK snapshot only, never broadcast). A checkout later made
at the same path gets a fresh admin directory without it. The status check
lists files only for a path whose mark still matches
(`worktreeStatusResponseFor`), and the close checks again right before
`git worktree remove` (`removeOneWorktreeFull` → `ownerMarkRefusal`). No token
(a pane from before identities, or a mark that could not be written), no
answer, or another mark: kept. The mark's file read and write run in
`boundedFSCall`: a worker that holds its own filesystem-call permit until the
I/O really returns, while the caller gives up at its deadline — on a dead mount
the plain file I/O would otherwise hold the status or worktree-add slot
forever. The cleanup view's agent entries carry no
token — they are verified against git's listing instead.

**Every worktree a close asked to remove and KEPT gets a sidebar card**
(`notifyWorktreeKept`, type `worktree_kept`, class System) with the reason —
no identity, a pane still in it, the repository unreadable, the identity no
longer matching — whether or not a branch delete was asked; `branchOpts`
captures the owners and tokens on every close for that reason.
