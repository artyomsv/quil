---
description: Projects (the grouping layer above tabs), the multi-daemon router, and the git subsystem behind the sidebar. Load when touching project state, the sidebar, destination routing, or gitinfo.
paths:
  - "**/internal/daemon/project.go"
  - "**/internal/daemon/gitcache.go"
  - "**/internal/gitinfo/**"
  - "**/internal/gitworktree/**"
  - "**/internal/daemon/worktree.go"
  - "**/internal/daemon/worktree_add.go"
  - "**/internal/daemon/worktree_remove.go"
  - "**/internal/daemon/worktree_status.go"
  - "**/internal/tui/worktree_client.go"
  - "**/internal/tui/worktree_close.go"
  - "**/internal/tui/project.go"
  - "**/internal/tui/projectdialog.go"
  - "**/internal/tui/projectpicker.go"
  - "**/internal/tui/sidebar.go"
  - "**/internal/tui/router.go"
  - "**/internal/tui/dialdest.go"
  - "**/internal/tui/attention.go"
---

# Projects, routing, and the sidebar

A project groups tabs, owns a root directory, and belongs to exactly one
daemon. One client drives several daemons at once, so most of the hazards here
are about **which machine a message is aimed at** and **which machine an answer
describes**.

## Data model

Each `ProjectModel` owns its OWN `[]*TabModel` and its own `activeTab`.
Isolation is the point of the feature; a flat slice with a filtered render
reintroduces exactly the ambiguity it exists to remove. Every tab INDEX in the
client (`activeTab`, `PaneRef.TabIndex`, `hitTestTab`, the tab bar) indexes
`curTabs()`, never `allTabs()`.

`ProjectModel.Dest` is **client-side only** — the daemon does not know it is
remote. Empty means the local daemon.

**The accessor split is the migration's correctness proof**: `curTabs()` is the
default for any read, and `allTabs()` is for the genuine cross-project
minority. `findPaneAndTab` (workstate.go) MUST span every project — background
agents keep firing hook events, and scoping it to the active project would make
a blocked background agent invisible, which is the headline feature.

## Routing

`Router` (`tui/router.go`) multiplexes N daemon connections behind the one
`Client` the Model consumes. There is no single-connection path in production;
`launchTUI` always builds a router, even for a lone local daemon.

**`Message.Origin` is client-side-only state** (`json:"-"`), so multi-daemon
routing needed no protocol bump. `stampDest` maps `""` to the `destLocal`
sentinel because `""` would otherwise mean BOTH "the local daemon" and
"unstamped" — the bug that had explicit local sends re-aimed at the remote.

**Liveness is keyed off `r.stop[dest]`, and the conn record deliberately
outlives its pump** — see `remote-transport.md`, which owns the reconnect
ladder. Anything here that removes a destination must drop `redialFns[dest]`
with it: `canReconnect` is literally `redialFns[dest] != nil`, so a leftover
one has the ladder redial a host the user just dismissed.

## Connecting and disconnecting at runtime (`dialdest.go`)

A host can be added mid-session from the New Project dialog's Host row, and
removed from the sidebar's context menu. Most of the machinery predates it:
`Router.Add` installs a connection and starts its pump, `attachAllDests` is
idempotent (its ledger is why — a second attach replays the whole ghost
buffer), and the dial is the same `dialExtra` the launch path uses, so a
runtime host is indistinguishable from a configured one afterwards.

`DialFunc` is a **factory over dest**, unlike the per-destination `RedialFunc`:
the point is dialling a host nobody has named yet. Both are supplied by
`cmd/quil` because the ssh transport lives there.

**The dial must read the config through a POINTER, not a captured value.** A
remote install rewrites it — `runRemoteSetup` records the absolute path it
installed to, and `remoteSSHOptions` turns that into the ssh `RemoteCommand` —
so a closure holding the launch-time value keeps dialling bare `quil` on a
non-interactive PATH, gets 127, and offers the install it just finished. That
shipped as a five-second install loop.

**Reusing CLI code under the TUI needs `Yes` AND `Out`.** `runRemoteSetup`
depends on stdout for narration and stdin for confirmation, neither visible in
its signature, and Bubble Tea owns both — the prompt landed on top of the
dialog and could never be answered.

## Offline destinations

A destination unreachable at launch keeps its sidebar rows instead of vanishing
(`internal/tui/offline.go`, `SeedOfflineDest`). See `remote-transport.md`'s
"Several daemons from one client" section for the router-level gap this closes
— a destination with no conn had no pump, so nothing ever published a loss for
it and its reconnect ladder never started.

**The offline row is a real `ProjectModel`, never a parallel list.**
`sidebarRows` builds paint order and `sidebarRowAt` indexes the SAME slice — see
the Sidebar section above — so a second collection for offline destinations
would need its own hit-test, and the two are exactly the kind of pair that
drifts the moment a row is added. `SeedOfflineDest` instead appends ordinary
`*ProjectModel` values carrying `Offline *OfflineState`, so every existing
reader (paint, hit-test, the palette, `lastDaemon`) sees them without a special
case, and only the few call sites that must refuse an action on one
(`projectActionable`, Task 10) need to know the field exists at all.

**The row's ID is the CACHED DAEMON project ID, and that is the whole
mechanism that avoids duplication.** `CachedProject.ID` is the daemon's own
project id, persisted the last time that destination broadcast state
(`internal/tui/remoteprojects.go`). `applyWorkspaceState` indexes
`existingProjects` by `p.ID` for the reconnecting destination and looks up
`existingProjects[info.ID]` for each project the broadcast names
(`internal/tui/model.go`) — an offline row seeded with the daemon's real ID
IS a hit in that map, so the first broadcast after reconnect fills it in place
rather than appending a second row beside it. `SeedOfflineDest` skips a
synthetic ID (`isSyntheticProject`) for the same reason in reverse: replaying
one would collide with the placeholder a projects-unaware daemon gets synthesised
fresh, and would make `destSupportsProjects` answer from a stale observation.

**`Offline` is cleared ONLY by the broadcast fill, and that single clear point
is deliberate.** `applyWorkspaceState` sets `proj.Offline = nil` right where it
fills `Name`/`RootDir`/`Bootstrap` from the broadcast — not in `finishReconnect`
— because that is the one place guaranteed to run for BOTH ways a destination
comes back: the reconnect ladder, and `adoptDest` from the New Project dialog,
which attaches a host directly and never touches the reconnect path at all. A
clear in `finishReconnect` would leave a dialog-adopted host's rows permanently
marked offline.

**`offlineDestMsg` is its own message type, not a synthesised `linkLostMsg`,
because of what each arm does to the listen loop.** The `linkLostMsg` arm
re-arms `listenForMessages` for a router, because a REAL link loss is produced
BY that loop stopping to deliver it — re-arming replaces the reader that just
exited. A destination seeded offline at launch never had a loop running for it
in the first place, so synthesising a `linkLostMsg` for it would re-arm a
listener that already exists, installing a SECOND permanent reader of the
router's `r.in` channel — and two readers reorder pane output and
`workspace_state` with no error anywhere. `offlineDestMsg`'s own arm
deliberately does not relisten.

**A redial against a destination that was NEVER attached version-gates; a
mid-session redial deliberately does not.** `verifyRemoteLinkGated`'s `gate`
parameter is `old == nil` in `redialRemote` (`cmd/quil/remote.go`) — a nil
previous client means this destination has never passed `gateExtraVersion` or
the primary version check, so nothing has ever confirmed the two daemons speak
the same protocol. Refusing there is safe: the destination is already offline,
so refusing changes nothing the user can see. Gating a MID-session reconnect
would end a session whose panes are healthy over a mismatch that can only mean
the remote was upgraded out from under a still-running client — logged as a
warning instead, because there is no good recovery once Bubble Tea owns the
terminal.

**`lastDaemon` must count offline destinations, because `knownDests` is the
CONNECTION table, not the destination list.** A destination that never
connected has no entry in `knownDests` — that is exactly the state an offline
row describes — so `lastDaemon` also walks `m.projects` for any dest with
`Offline != nil` and a live dialer (`canReconnect`). Without that second walk,
losing the local daemon while a configured remote is still laddering in the
background reads as "the last daemon died" and quits the client, taking the
ladder and the still-recoverable remote down with it.

## One remote host, one project

A daemon must hold at least one tab and a tab must belong to a project, so a
host ALWAYS presents a project before the user names anything —
`createTabLocked` bootstraps one on attach, `migrateToDefaultProject` wraps a
pre-projects workspace in one. Both are called "Default". Creating a project
beside it left the user with a row they never made, holding the tabs they cared
about.

**`Project.Bootstrap` is the signal, never the name.** A user may legitimately
name a project Default, and renaming the bootstrap one is exactly what stops it
being a bootstrap — so `UpdateProject` clears the flag. It is persisted and
rides the same `map[string]any` that is both the disk snapshot and the wire
broadcast, so one write reaches both.

**The rule lives in the CLIENT, and it has to.** The daemon does not know it is
remote — `Project` has no `Dest` field — so "one project per host" cannot be a
daemon invariant; it is a rule about what create DOES. `submitNewProject`
therefore branches on `projectFormDest != ""`, and on how many projects
`projectsOnDest` reports: a lone bootstrap project is renamed in place (its tabs
are already inside it, which is what puts them under the chosen name), anything
else is FOLDED into one.

**A create-time guard cannot repair the state it prevents, and shipping only
half of that was the bug.** The rule stopped new duplicates and left every
existing one in place — so a host connected before it kept the rows each
disconnect-and-recreate cycle had produced, and the refusal that met the next
create ("already has a project (X) — rename it instead") named a remedy the
dialog had no route to AND one that could not work: renaming one of three
leaves three. Every operation the client owned was 1:1 on a project, and the
only removal, `DestroyProject`, takes the tabs and panes with it — so
consolidating by hand cost the user the work that made them care which project
survived. `MergeProjects` is the missing N:1: tabs are REASSIGNED, the emptied
records dropped, nothing closed.

**The fold is confirmed by RECOMPUTING the plan, not by clearing a flag.** The
first Enter arms `projectFormMerge` and states the consequence; the second
re-derives the plan and acts only if it is identical (`sameAs`). An
arm-then-invalidate scheme has to be right in every edit handler, while this is
right by construction — an edited name re-arms with the new sentence instead of
carrying out the one the user moved away from. The one place that must still
clear it explicitly is dialog OPEN: reopening against the same host and typing
the same name reproduces an identical plan, so a plan outliving its form would
fire on the first Enter of the next session having shown nobody anything.

**Comparing plans is NOT the property required, and that gap shipped inside this
feature.** What is needed is "the user pressed Enter while the sentence this plan
produced was on screen"; the armed plan and the displayed message are
INDEPENDENT fields, so they come apart. Typing into the Name row clears
`projectFormErr` and leaves `projectFormMerge`; backspace restores neither. Two
keystrokes therefore returned the form to a state where the plan matched,
nothing `sameAs` compares had changed, and the warning line was blank — and the
next Enter folded a host with no confirmation anywhere on screen. `foldIsConfirmed`
checks the RENDERED line as well, which also covers the Host/User rows and a dial
whose "connecting to …" replaces the warning while `projectFormDest` still names
the old host.

**`message()` must name everything the plan can change, and the root directory
is how that was learnt.** The dialog's own opening browse resolves an EMPTY
path, so the daemon answers with its default CWD and `applyBrowseListing` writes
that into `cwdBrowseDir` — within a second of every open, so the field almost
always holds an artifact rather than a choice. A plan armed before that landed
carried the survivor's root and one armed after carried the daemon's default, so
the second Enter re-armed *correctly* behind text that had not changed by one
character: three Enters, two identical sentences, and a real project's root
replaced. It also invalidated `submitProjectForm`'s standing claim that
`cwdBrowseDir` is "always one of three safe things" — there is a fourth.

**The fix is that the fold carries NO root directory at all.** Naming the change
in the message was tried first and is the weaker answer: the right response to
"we might do something the user did not ask for" is not to do it. A fold renames
and absorbs; relocating is `MsgUpdateProject`, from a dialog seeded with the
project's own root. `MergeProjectsPayload` has no `RootDir` field, so the
guarantee is structural rather than a branch someone can drop — and the adopt
path keeps taking the form value, correctly, because there the project is
unnamed and its root IS the daemon's default, so writing it back is a no-op.

**Reachability is checked in the client, because the send cannot report it.**
`Router.Send` DROPS a message aimed at a dest it has no conn for, logs, and
returns nil — deliberately, so `resizeAllPanes`/`sendAllLayouts` cannot break
mid-iteration. Every `if err := send(…)` in the dialog is therefore blind to the
likeliest failure of all. `destReachable` guards the whole `projectFormDest != ""`
branch rather than the fold alone, because a host that disconnects between the
two Enters also changes WHICH branch runs: its projects are dropped client-side,
so the recompute finds none and falls through to the generic create — the user
confirms a fold and the client attempts a create, then closes as though the fold
had happened. It is not `destConnected`: that answers "should this be dialled",
and for a non-Router client it answers from `knownDests()`'s `[""]`, which would
report unreachable down the one path guaranteed to deliver.

**`absorb` is an explicit ID list, never "every other project".** The local
daemon is deliberately exempt from one-per-host, so a payload meaning "fold
everything" would let one mis-aimed local send collapse the projects on the
machine the user is sitting at. Unknown IDs and the survivor itself are skipped,
which also makes two clients folding one host converge rather than corrupt: the
loser's absorb IDs no longer resolve, so its message degenerates to a rename.

**The survivor is the first NON-bootstrap project**, falling back to the first.
Which one survives does not decide the name — that is overwritten either way —
it decides the root directory inherited when the browse has not answered, and
the order tabs land in. A bootstrap project's root is whatever CWD the daemon
started in; a named project's is one the user chose.

**`MergeProjects` renames AFTER the deletions**, so the absorbed names are free.
Naming the host after the duplicate being folded away is the ORDINARY case —
that is what the strays are called — and disambiguating first hands back
`cluster-management (2)`, the exact shape the fold exists to remove. For the
same reason `sendMergeProjects` does NOT reuse `sendUpdateProject`'s duplicate
guard: every project it would find on that host is one the message absorbs.

**A daemon too old to understand `merge_projects` cannot receive one**, which is
why no capability probe was needed (contrast `destSupportsProjects`).
`gateExtraVersion` REFUSES the connection on any version difference, and the
runtime-connect path answers the mismatch with the upgrade — so client and
daemon always speak the same protocol or are not talking at all.

**"The host has reported no projects" means NOT YET, never NONE.** A daemon
holds a tab and a tab holds a project, so an empty answer for an attached host
is a broadcast that has not landed — and `destDialedMsg` batches the attach with
the root-dir browse, so the listing can paint and invite an Enter before it
does. Creating there lands beside a bootstrap project the client cannot see, and
that project is still adoptable, so the NEXT create renames it: two named
projects from two ordinary keystrokes. `submitNewProject` waits on
`m.attached[dest]` instead, which is self-clearing where the wrong answer is
permanent.

**Adopting is compare-and-swap, because the decision is made from a snapshot.**
Each client checks `Bootstrap` in its OWN copy, so two clients driving one host
both see the bootstrap project and both send a rename — the second silently
renaming the first's freshly named project. `UpdateProjectPayload.AdoptBootstrap`
makes the daemon apply it only while the project is still a bootstrap; a plain
rename omits the flag and is unconditional. Same ladder as the duplicate name:
the client refuses when it knows, the daemon guarantees when it could not.

**A form that names a destination must SHOW that destination.**
`openNewProjectDialog` seeded `projectFormDest` from the active project while
leaving the ssh fields blank, so with a remote project active the form read
"this machine" and acted on the far one. Survivable while every submit was a
create; destructive the moment a submit can rename the host's existing project.
It seeds Remote/user/host from the dest now, as `beginProjectRename` always did.

**The local daemon is deliberately exempt**, and applying this there would be
wrong twice over: it would FOLD every project on the machine the user is sitting
at into one — the local daemon is expected to hold many — and its create would
rename the Default holding their existing work rather than adding beside it.

**Hosts connected before the flag existed keep an unmarked Default** — the
migration runs once and does not re-run, so the record has no `bootstrap` key
and parses as false, i.e. as the user's. That host therefore takes the FOLD path
rather than the adopt one, which is the right answer for it: the rename is
offered and confirmed rather than applied silently to a project the user may
still be using. Absence MUST mean the user's: the alternative makes every
pre-flag project adoptable and a create silently renames real work.

**Disconnect is client-side only, and that asymmetry is what generates
duplicate projects.** The sidebar rows vanish, so the host looks emptied — but
the remote daemon keeps every project, and the next connect replays them all.
A user who re-creates "the project that disappeared" ends up with two rows
carrying the SAME name and the SAME host, which the sidebar cannot tell apart
(it renders name + host and nothing else), so neither can the user: removing
the wrong one takes its tabs. `submitNewProject` refuses a name already present
on `projectFormDest` (case- and space-insensitive, since neither makes the rows
distinguishable) — reachable only on the LOCAL path now, since a remote host
with any project at all folds before it gets here, and the fold is what clears
duplicates a pre-rule client already left. Scoped to ONE dest deliberately —
the same name on a laptop and a build host is ordinary, because the row carries
the host. The client
itself does NOT duplicate: `mergeProjects` is keyed by ID and `Router.Add`
no-ops on a live dest, both pinned by tests (`dialdest_reconnect_test.go`)
written to disprove exactly that theory before the guard was added.

**That client guard is NOT sufficient on its own, and the reason is a race.**
It compares against `m.projects`, which is empty for a host until its first
`workspace_state` lands — and Enter on the form's Name row submits immediately,
so the window is one keystroke wide. `CreateProject` therefore disambiguates
daemon-side (`uniqueProjectName`, under `sm.mu`, the only place that can be
sure). It appends ` (2)` rather than REFUSING because a refusal there would be
silent: the daemon has no error channel back to a create, and this package has
already paid for a silently-ignored project message once — a daemon that
accepted create and did nothing read as a broken dialog for an evening. The
two layers are a ladder, not a duplicate: the client refuses when it knows, so
the user picks the name; the daemon guarantees distinguishability when it did
not. `UpdateProject` (rename) deliberately does NOT do this — a rename is a
deliberate act on one project the user is looking at.

**The form's one message line needs a SEVERITY, not just text.** Every writer
assigned `projectFormErr` and the render drew a red ✗, so "installing…" and
"upgrading…" — progress on a host that had answered — were indistinguishable
from "cannot reach that host". Written only through
`setFormError`/`setFormBusy`/`setFormOK`; assigning the string alone leaves the
previous message's colour behind, and the zero kind is ERROR so a writer that
forgets fails loud rather than green.

**A version mismatch is NOT derivable from the exit code, which is why it
needed a sentinel of its own.** `ErrRemoteQuilMissing` is classified from ssh's
127 — and a mismatch can never produce it, because quil RAN over there: the
link delivered bytes, so `ClassifyExit`'s `established` override answers
`RemedyNone` for whatever code follows. `gateExtraVersion` therefore raises
`ErrRemoteVersionMismatch` from the HANDSHAKE, and only when `res.Cmp > 0` —
provisioning pushes THIS client's build, so acting on a NEWER remote daemon
downgrades a machine other clients may share and does not fix this session
either. Both sentinels share `installOffer`, `installedDests` and the retry
dial; the once-per-host guard covers the upgrade for its own reason, since a
daemon still reporting the old version after one did not restart and pushing
the same archive again cannot change that.

**The launch and reconnect paths ASK; only the New Project dialog acts without
asking.** "Installing software on another machine must not be a side effect of
opening the client" is the right rule and it was read one step too far: it
argues against provisioning unprompted, not against offering. So for a year the
two paths a RESTART goes through — the launch dial and the reconnect ladder —
each classified the mismatch, seeded a parked row, and stopped. The user got ⚡
and a `Detail` string nothing rendered, and the remedy lived in a shell command
the tool already knew how to run. The reconnect arm even carried the comment
"let the sidebar offer the upgrade instead", describing an affordance that was
never built.

`enqueueUpgradePrompt`/`promptNextUpgrade` (`offline.go`) close it with a
`confirmKindUpgradeDest` dialog. A QUEUE, because a client update leaves every
configured host stale at once and one dialog shows at a time; deduped, because
the launch dial and the ladder can classify the same host. It drains from the
FIRST-`WindowSizeMsg` branch — that arm returns early, so the later drain never
runs on an unresized session, the same trap `wakeOfflineDests` documents — and
again whenever a dialog closes, so an offer deferred behind the disclaimer or
the plugin migration arrives rather than being dropped. `y` is required, not
Enter: this dialog opens BY ITSELF, so a reflexive Enter would restart a remote
daemon and kill what its panes were running. Declining does NOT arm
`installedDests` — "not now" is not "never" — while accepting does, sharing the
dialog's guard against the install/retry/same-error loop.

**The RECONNECT ladder has the same config constraint as the dial, and got it
later.** `SetRedialFactory` captured the value while `SetDialFunc` beside it
read the pointer, so a host provisioned at runtime attached fine and then, on
its first link drop, redialled bare `quil` forever — 127 is not classified
permanent, so the ladder never stops and never succeeds. Both launch-time and
runtime ladders share the pointer now.

**`config.Save` writes the WHOLE struct, so two writers need `config.Mutate`.**
The install records `[remote.hosts.<dest>].binary`; the TUI records
destinations and UI settings from its LAUNCH-TIME copy. Saving that copy
reverted the path written seconds earlier, so each install ended by erasing its
own result and the next launch offered it again. Three sites did it —
`persistDestination`, `forgetDestination`, and the exit-time save, which fires
on every exit once any setting has been touched. The exit save additionally
restores `c.Remote` explicitly, naming the one section the Model does not own.

**A `Router.Remove` does not stop messages already buffered.** The stop channel
covers a pump parked in `Receive`; a broadcast already in `r.in`, or one that
passed the check just before Remove, is still delivered — and
`applyWorkspaceState` treats a broadcast as authoritative for its dest, so a
late one re-appends the projects the user just dismissed. They return
unusable, since `knownDests` no longer lists the dest. `Update`'s
`WorkspaceStateMsg` arm drops state from a dest `destConnected` rejects; that
boundary is the only place that knows what the Model currently holds.

**Sanitising is not bounding, and the form's message line needs both.** A
remote daemon chooses its project names, and `sanitizeRemoteText` removes
escapes without shortening anything — a megabyte of ordinary printable text
survives it whole. That line is the one value-bearing row in the dialog with no
truncation of its own and lipgloss WRAPS at the box width, so an unbounded name
becomes thousands of rendered lines in every frame. The interpolated name is
capped at `formMsgNameCap`; the render sanitises as well as the eight set
sites, because the render is the one place guaranteed to run for every message
and the ninth set site is the one somebody forgets.

**The adopt path made an "unreachable" case reachable.** `submitProjectForm`
drops its browse-pending gate on the stated grounds that an empty root dir is
"a real answer for a CREATE and unreachable for a RENAME" — and adopting routes
a create into `submitRenameProject`. `UpdateProject` has no unchanged-value
guard, so an Enter pressed before the browse lands would have ERASED the
adopted project's root. It substitutes the project's own root instead. Worth
generalising: a comment that says a state is unreachable is a claim about every
caller, so adding a caller means rechecking it.

**Sanitize at RENDER, and before any width measurement.** A remote daemon names
its own projects. `lipgloss.Width` measures an escape sequence as ZERO cells, so
a truncation neither counts nor cuts it — a width check is not a sanitiser, and
that is why three paths (context-menu title, the rename form's Name field, the
palette's pane labels) looked safe while writing an OSC 52 straight to the
terminal. Render-only is required rather than stylistic on the form field: its
value round-trips to the daemon on submit, so stripping it in state would
rewrite a name the user never edited.

**Every cross-tab jump owes `exitNotesModeInPlace` FIRST.** It reverts focus
mode on whichever tab is active when it runs. `jumpToPane` — the choke point for
MCP `set_active_pane`, the notification sidebar, pane-history back and the
palette — moved the tab with no teardown at all, and the attention queue got it
only when the PROJECT changed, not when just the tab did. `notesKeyExempt` does
not cover this: that branch runs only while the editor holds focus, and notes
open beside a working agent normally has the pane focused.

## Daemons that do not speak projects

`destSupportsProjects` is **behavioural, not a version comparison**: a daemon
that supports projects always reports at least one (it bootstraps a Default
rather than run with none), so the client synthesising a placeholder for it IS
the observation "this daemon told us about no projects". A version number would
have to be maintained against a release that has not happened yet.

This matters because such a daemon **accepts every project message and does
nothing**. Panes, terminals, AI panes and the directory browser all work; only
create/rename/destroy vanish. That combination read as a dialog bug for an
evening. Hence: the placeholder is named `(no projects)` rather than `Default`
(which is also what a real daemon calls its bootstrap project, making the two
indistinguishable), rename and destroy are greyed on it, and a create aimed at
such a host is refused in the form rather than closing on a project that never
appears. Disconnect stays enabled — it is entirely client-side and is what the
user actually wants there.

## The project form

Rows are a **visible-row list**, not fixed indices: the ssh fields exist only
while the Remote toggle is on, and one list drives focus, key dispatch and
render together. Three independent notions of "which row is where" is how a
form grows a case that highlights one field while typing lands in another.

Row ORDER is the flow. A root directory lives on exactly one machine, so Enter
on Host **connects** rather than submitting — the browser below asks whichever
daemon `projectFormDest` names, and submitting first pairs a name with a path
browsed from a different host. Toggling Remote on blanks the listing and
requests nothing: entries describing this machine under a form that says remote
invite a path that exists here and nowhere else.

**There is no wait on the browse.** The hazard — submitting a root dir left
over from a DIFFERENT dialog session — is removed at the source:
`resetProjectBrowseState` clears the scratch value on open, and
`beginProjectRename` seeds the field with the project's OWN root. Empty is then
a real answer for a create (the daemon falls back to its default CWD) and
unreachable for a rename, where it would ERASE the stored value since
`UpdateProject` has no unchanged-value guard. Waiting instead made creating and
then renaming a remote project impossible, because that browse takes seconds.

## Sidebar

`sidebarRows` builds paint order and `sidebarRowAt` indexes the SAME slice — a
hit test written as an independent second copy drifts the moment a row is
inserted, and the symptom (clicking one project, getting its neighbour) looks
nothing like a rendering change. Four fixtures pin it; they shift whenever a
row is added, which is the expected cost.

Layout decisions that were each a bug first: the project NAME alone on its row
with a remote's host on a second one (`name@dest` at 22 columns leaves nothing
of either half, and the badges truncate away first); a blank row between TAB
groups but not between projects; two-cell markers on both project and tab rows
so the levels line up; and middle-elision for branch names and ssh
destinations, since cutting either end of `feat/…` or `user@…` leaves a column
where every row looks the same.

**That "levels line up" claim has an unstated exception below ~5 cells.**
`sidebarTabHeading` budgets ordinal → marker → name, so a narrow tab row gives
up its `▸ ` marker first, to keep the ordinal — the part that maps the row to
`Alt+1..9`. `projectRow`'s own marker is unchanged; only the tab row's gives
way. Unreachable through the UI (`minSidebarWidth` is 12); reachable only via a
hand-edited `sidebar_width`, since `sidebarWidth()` only falls back on
`configured <= 0`.

`elideMiddle` takes both halves by CELL budget. Deriving head/tail from cells
and slicing `[]rune` with them makes each half overrun on wide glyphs, and once
the rune count drops below head+tail the two slices OVERLAP — the row repeats
characters at roughly twice the width asked for, and `padOrTrunc` re-clamps it
so nothing downstream complains.

**No sidebar state glyph may be an EMOJI-CAPABLE codepoint** (`glyphBlocked` /
`glyphDone` / `glyphIdle` / `glyphPinned`, plus every frame `workingGlyph` can
return, pinned by `TestSidebarGlyphs_OneCellAndNotEmojiCapable`). A font is free to answer such a
codepoint with a colour emoji face, which is drawn about two cells wide while
advancing ONE — so it paints over whatever follows it. In the project badge
what follows is the count, which is how `⚠1 ◐2` rendered as a warning sign with
its number underneath it. Where the terminal instead ADVANCES two cells the
damage is quieter: `lipgloss.Width` measures U+26A0 as one (measured — U+26A1
`⚡` measures two, which is why `truncateCells` names that one), so the row is
painted a cell wider than every helper believes and `.Width(w)` wraps the
excess. Forcing text presentation with U+FE0E was tried and rejected: it
depends on the terminal honouring a variation selector, and the emoji-side
alternative U+FE0F walks into the cutter rule below. A codepoint that was never
emoji needs neither.

**The WORKING state is the one glyph that is not a constant: it ANIMATES**
(`workingGlyph`, sidebar.go), cycling the same `spinnerFrames` the tab label and
the pane's top border already run. It shipped as a static `◐` while those two
spun, so one fact wore two notations across three levels of the same screen —
and the sidebar, the level that lists every pane at once, was the one telling
the quieter story. Motion is also what separates working from the rest of the
vocabulary: `▲`/`✓`/`◆`/`○` all describe something that has STOPPED. The frame is
a parameter rather than a read of the Model, because the two callers hold
different copies of one counter — `paneRow` passes the pane's mirrored
`workFrame` (what `buildTopBorder` renders, so a row and its own pane border
cannot disagree) and `projectRow` the shared `workSpinnerFrame` the tick derives
it from. `TestSidebar_WorkingIndicatorAnimatesWithTheTabSpinner` drives it
through `Update`, not through the row builders: a unit call handed a frame by
hand agrees with itself whichever counter the real call site reads.

**`truncateCells` and `lastCellsToWidth` cut on GRAPHEME CLUSTERS, and both
halves of that are load-bearing.** A rune is not the unit of width — U+FE0F
measures 0 alone and makes the pair before it 2 — so summing independently
measured runes returns a string WIDER than the budget it was handed, which
`renderSidebar`'s closing `.Width(w)` wraps rather than cuts, shifting every row
below while `sidebarRowAt` still maps screen row y to `rows[y]`. Re-measuring
an accumulated prefix instead is correct but QUADRATIC: it reallocates the
prefix each step, and a zero-width cluster never advances the budget, so the
loop cannot exit early on a long run of them. Both failures are reachable from
ordinary remote text, because `sanitizeRemoteText` is a control-character
filter and preserves printable non-ASCII byte-identically — it is not a
bounding pass. `lipgloss` must remain the sole measurer (uniseg segments,
lipgloss measures), or the cut can disagree with the `.Width` that paints.

**A row that mixes COLOURS goes through `renderStyledSegments`, never one
`style.Render`.** Every `Render` emits its own reset, so wrapping a line that
already carries SGR closes the outer colour at the first inner segment and
leaves the rest of the row unpainted — which is why `projectRow`'s ▲/◐/✓ badge
was flat grey while the pane rows it rolls up were amber, blue and green. The
helper spends the width budget on PLAIN text segment by segment and styles only
the piece that survived the cut, because `truncateCells` segments on grapheme
clusters and `lipgloss.Width` measures an escape as zero cells: a single pass
over already-styled text both mis-measures the row and can cut through the
middle of an SGR sequence, emitting `38;5;214m` as literal text. `padOrTrunc`
stays right for `paneRow` / `gitRow` / `sidebarTabHeading`, which each have ONE
style for the whole line.

**Its segments must each begin on a GRAPHEME CLUSTER boundary, and that is a
caller requirement the helper cannot repair.** A segment starting with a
combining mark joins the previous segment's last cluster, so the
independently-measured sum understates the row — the U+FE0F trap above, one
level up. The reason no measurement strategy fixes it: whether the two runes
really join depends on the STYLES rather than the text. An SGR emitted between
them separates them and the sum is honest; two property-free styles emit
nothing and it is not (measured, `{"⚠", "️"}` at w=3: 3 cells coloured, 4 plain).
`projectRow` satisfies it by construction — every badge segment starts with a
space, and its head ends wherever `truncateCells` cut, which is a boundary by
definition. Segments are also spent IN ORDER and a segment that cannot start
ENDS the row: yielding its place to the next one would put one state's glyph in
another state's position.

**The attention pin is DAEMON-owned, and the client is not allowed to write
it.** `Pane.PinnedAttention` rides `MsgUpdatePane`'s `*bool` alongside `Muted`,
persists through `workspaceStateFromSnapshot`'s non-overlay block (one line
serving both the disk snapshot and the broadcast) and comes back on every
`workspace_state`. `syncPaneMeta` copies it UNCONDITIONALLY, which is what makes
an unmark performed in another client reach this one — and it is also why the
context menu SENDS rather than flipping the local bool: a local write is
reverted by the next broadcast, and the git ticker alone delivers one every 5 s,
so the mark would visibly undo itself. `Clear attention` is the one place that
does both, and deliberately: the local clear is what stops the row painting ◆ on
THIS frame, the send is what stops the daemon putting it back. The `*bool` is
load-bearing exactly as it is for `Muted` — `handleUpdatePane` is a PARTIAL
update handler, and a plain bool would clear the pin on every rename and every
OSC 7 CWD change.

**`Clear attention` sends the pin UNCONDITIONALLY, never gated on the local
value, and gating it was a real bug.** `pane.pinnedAttention` reports what the
last broadcast said, never what the daemon holds — and Mark deliberately does
not write locally, so Mark followed by Clear inside the round trip read the pin
as false, sent nothing, and then let the Mark's own broadcast restore the ◆
*after* the user had cleared it, persisted. Two right-clicks, and over ssh the
window is hundreds of milliseconds. For the same reason Clear does not clear it
locally either: a broadcast already in flight re-sets it and the next one clears
it (the ◆ blinks off/on/off), and with the link parked `Router.Send` drops the
message and returns nil, so nothing ever arrives to revert a local clear and the
mark stays gone until reconnect. The send is `sendForDestStrict` and the
destination is resolved on the Update goroutine — this is a one-shot the user
asked for, not one of the bulk iterators the loose `Send` was written for.

**"The user's own mark" is a statement about a daemon the user CONTROLS.**
`syncPaneMeta`'s unconditional copy is what makes a cross-client unmark work,
and it is also what removes the client's ability to refuse: a hostile remote
daemon can assert `pinned_attention` on every pane and re-assert it within 5 s
of any Unmark. The blast radius is display only — nothing outside rendering
reads the flag, the attention queue keys on `blockedSince` — and such a daemon
already drives `blockedSince`/`unseen` through hook events, so this widens an
existing capability rather than opening a new one. Accepted deliberately; if it
ever needs closing, the shape is a per-pane client dismissal epoch that
suppresses a re-assert until the user re-pins.

**`counts().pinned` is a SECOND AXIS, not a fourth rank.** The other three are
one ordered classification — a pane parked for input has also finished its turn,
"needs you" outranks "is ready", so a pane contributes to exactly one — and the
pin is orthogonal, since a pinned pane is usually also working or blocked.
Folding it into the switch would make a mark that exists to be un-loseable
disappear the moment the pane got busy, which is when it is most wanted.
`paneRow` states the same thing differently: ◆ stays as a SUFFIX when a live
state outranks it, painted in `sidebarPinnedStyle` rather than the outranking
state's colour, and its width is RESERVED before the label floor applies so a
long blocked-reason cannot eat it.

**Manual and automatic marks must not share a colour, and for a while they
did.** `unseenTabStyle`'s green and the pane border's 28 covered both `unseen`
and `pinnedAttention` — but only `unseen` clears itself on focus, so the shared
colour left the user waiting on a green that never went. Pinned now takes purple
141 everywhere (`sidebarPinnedStyle`, `pinnedTabStyle`, the border), matching the
foreground/background relationship `blockedTabStyle`'s 214 has to
`sidebarBlockedStyle`. `pinnedActiveTabStyle` is underlined for the reason
`blockedActiveTabStyle` is — the background is the only thing active and inactive
differ by, and an SGR attribute costs no cells where padding would desync
`hitTestTab`. **`tabLabel`'s ◆ is deliberately OUTSIDE `tabStyle`'s precedence**:
blocked outranks pinned for the colour, so on a tab that is both, the glyph is
the only channel left to say the pin is there.

**`linkGlyphStyles` is a swept MAP rather than a switch**, because
`linkGlyphStyle`'s fallback has to be some style and every candidate lies about
a state it was not written for. A third link state added to `linkGlyph` without
an entry would render in the fallback's colour and read as a state that has
one; enumerating lets `TestLinkGlyph_EveryStateHasItsOwnColour` drive `linkGlyph`
over every `reconnectState` combination and assert each glyph it can produce is
paired, which a switch cannot express. The colours come from OUTSIDE the
pane-state palette deliberately — link health describes the destination, not any
pane — so parked takes `spawnErrorStyle`'s red and retrying takes
`projectFormMsgBusy`'s orange, never the 214 amber reserved for
blocked-on-user, which a self-healing link is the opposite of.

**⚡ (U+26A1) is the one deliberate exemption from the no-emoji-capable rule**,
and it lives outside that const block rather than inside it — the block's own
test lists U+26A1 as exactly the kind of codepoint to refuse, so adding it there
would fail, correctly. It is safe only where it is: `lipgloss` already measures
it as two cells, so the arithmetic accounts for its real width, and it is the
LAST thing on the row, so a font drawing it wider has only padding to paint over.

**The sidebar is a real reserved column**, unlike the notification sidebar's
overlay — but it must not re-mode a pane. `paneVTSize` takes SIZE from the rect
and MODE from `nativeW` (the width the rect would have with the sidebar
closed), because a 22-column sidebar moved an even split from 92/93 to 81/82,
straddling `min_native_cols` and flipping ONE of two identical siblings to a
161-column cropped canvas.

**The edge drag must NEVER move `m.sidebarWidth` mid-drag.** `View()` calls
`tab.Resize(m.paneAreaWidth()…)` on every frame and `ResizeVT` pairs every
emulator resize with a PTY redraw, so a strip that follows the cursor replays
the 2026-07-15 unpaired-rewrap corruption once per motion event. `sidebarDragW`
holds the pending value, a one-column rule is composited at it, and
`finishSidebarDrag` is the single commit point — the same deferral
`finishSplitDrag` makes, for the same reason. Clamping goes through
`sidebarWidth()` and nowhere else, so the edge stops where the user let go
rather than at a value the renderer silently corrects, and `minSidebarWidth`
keeps a drag from collapsing the strip it would then have no edge to grab back
by (collapsing is `Alt+Shift+S`'s job).

**The press is hit-tested BEFORE `projectSidebarSwallowsMouse`, and that order
is load-bearing rather than stylistic.** The zone's left column is the
sidebar's OWN last column, and the swallow branch *always* returns — so with
the checks the other way round the edge is ungrabbable from the side the user
aims at it from, while still looking implemented. The preview composites
through `overlayAt` rather than a one-column cutter of its own: that function
already solves the truncate-lands-mid-glyph, SGR-left-open and
wide-glyph-straddling-the-seam problems, all three of which a second cutter
would have to solve again to be correct at one column.

**The PANES section scrolls; PROJECTS is pinned.** `sidebarRows` returns the
section boundary as a second value rather than having anyone re-derive it from
the heading text, and the offset is applied INSIDE `sidebarVisibleRows` — the
same reason the cap is: paint and hit test share that one function, and a
window applied at the render site is the row-drift bug in another form.
`sidebarVisibleRows` stays PURE and clamps a local copy; `scrollSidebar` and
`scrollSidebarToPane` own the write to `m.sidebarScroll`. When the pinned head
would leave fewer than `minPaneRows` for the body the whole strip reverts to
the old tail cap — a strip showing every project and no panes is worse than a
truncated list of both.

**A park no longer clears `turnActive`.** `Notification` covers a permission
prompt (turn running) and an idle-wait nudge (`Stop` already fired), so
clearing it was a no-op exactly when it was right. The consequence is
load-bearing: the falling edge no longer fires, so a park does not set
`unseen`, and `tabBlocked` is what carries a parked background pane to the tab
bar. The two must not be separated. See `hooks-and-sessions.md`'s Work state
section for the hook side of this.

**`Notification` no longer parks unconditionally, so the paragraph above
describes the `PermissionRequest` path exactly and the `Notification` path only
in part.** `Notification` is its own kind now (`hookevents.WorkEventNotify` /
`workNotify`): the claude hook marks the idle nudge it can recognise from the
message text, and the consumer parks on everything else — so an idle nudge
arriving after `Stop` sets nothing at all, and a still-working pane keeps its
spinner and `⋯N` instead of being outranked by a stale `▲`. Every sidebar reader here is
unchanged; there is simply one fewer event that sets `blockedSince`. See
`hooks-and-sessions.md`'s Work state section for the split.

**`paneRow` suppresses the blocked glyph for the FOCUSED pane, and that is the
only place a sidebar row does more than report state.** `blockedSince` is not
cleared by focus (`ackFocusedPane` clears `unseen` alone — it runs on every
message including the 100 ms spinner tick, so a clear there destroyed the mark
before it could ever be seen). Keeping the state and dropping the glyph is what
"you are looking straight at the prompt" costs: `tabBlocked`,
`ProjectModel.counts()` and `blockedPanes()` all keep reading the same live
flag, so the tab stays amber, the badge keeps counting and the attention queue
keeps offering it, and leaving the pane restores the `▲` with no hook edge
required. An UNFOCUSED pane is blocked-visible always — that is the signal the
feature exists for, and a suppression that leaked into it would be the original
"the mark is never observable" defect in a new place.

**The wheel re-clamps the STORED offset before adding its notch.** Nothing
clamps `m.sidebarScroll` when the geometry moves underneath — the paint clamps
a local copy, deliberately — so a vertical resize (`sidebarContentHeight()` is
`m.height-1`) or a closed pane legitimately leaves it past the current maximum.
Adding to the stale value there clamps straight back to the same visible
maximum for as many notches as it is stale by: not row drift, but a wheel that
does nothing while the strip already shows the bound it is being pushed
against.

## Git subsystem (`gitinfo` + `gitcache.go`)

Three plumbing calls per checkout: branch, linked worktree, ahead/behind.
`git status --porcelain` is deliberately excluded — the one call that can take
seconds on a large repository without fsmonitor.

`referencedDirs` bounds what is PROBED; `sweep` is what bounds what is STORED.
OSC 7 rewrites a pane's CWD on every `cd`, so without it one shell roaming a
monorepo adds a map entry per directory it visits and `byDir` keeps every
checkout ever seen — against a daemon that runs for weeks.

**The worktree's NAME costs nothing, and where it comes from is the reason.**
git spells a linked checkout's per-checkout git dir `<common>/worktrees/<name>`,
so `filepath.Base(gitDir)` IS the name at the exact line that already computes
`LinkedWorktree` — no fourth plumbing call. It is set in **both**
`gitinfo.Probe` and the `gitcache` placeholder entry, and neither is redundant:
`gitcache` assigns `e.info = info` wholesale, so a field only the placeholder
sets is dropped on the first successful probe, while a field only `Probe` sets
is missing for the tick between resolving a CWD and that probe landing — which
is the tick right after a pane is created in a worktree, i.e. the one the user
is looking at. Derived daemon-side because separators belong to the machine
holding the disk; a Windows daemon's path split by a Linux client's
`filepath.Base` returns the whole string. `gitRow` SUPPRESSES it when it is
just the branch with `/`→`-` (the near-universal convention), because
restating the branch costs eight cells of a 22-cell row — but never for a
detached checkout, which has no branch to restate.

**Keyed by the PER-CHECKOUT git dir, not the common dir** as the design spec
called for: linked worktrees share a common dir while sitting on different
branches, which is the entire reason anyone creates one, so a common-dir key
reports every worktree as being on whichever branch was probed first. The
per-checkout key still collapses N panes in one repository to one invocation.

Probes run in a remembered WORKING directory, never the git dir itself — for a
linked worktree that is `<common>/worktrees/<name>`, and git run there resolves
against the repository, reporting the main checkout's branch.

State rides the existing workspace broadcast (no new RPC, no staleness key).
The refresh is a background ticker and `lookup` never probes, so a repository
on a dead mount can slow the ticker but never a state update. Every invocation
takes a `claimBlockingFSCall` permit for the reason `browse.go` documents, and
a HEAD-mtime check makes an unchanged branch free. A probe that does not answer
keeps its last value and marks it STALE rather than blanking or guessing. No
upstream renders NO counts — `↑0↓0` claims a checkout is in sync when the truth
is there is nothing to compare against.

**Windows: every exec needs `CREATE_NO_WINDOW`.** The daemon runs
`DETACHED_PROCESS`, so it owns no console, and a child started from a
console-less parent gets a brand new console allocated — a real window. A probe
per checkout every few seconds is a stream of them. `hideWindow` is a no-op on
Unix; the build tags keep the difference in one pair of files.

## Creating worktrees (stage B)

**The new branch starts at the repository's DEFAULT branch, and the whole point
is that it is not HEAD.** `git worktree add -b <new> <path>` with no start-point
branches from the HEAD of the repository the command runs in — `cmd.Dir =
spec.RepoRoot`, the MAIN checkout — so the base was whatever the user last left
that checkout on. A fix worktree created while it sat on a feature branch came
up carrying that feature's unmerged commits: isolated in its DIRECTORY, which is
what the feature advertises, and not in its HISTORY, which is what the user
assumed. The failure is invisible at create time and surfaces as a PR whose diff
is somebody else's work. `gitworktree.defaultBranch` resolves `origin/HEAD`
first (the repository's own recorded answer — a repo whose default is `develop`
must not be branched off a `master` that merely exists beside it), then
`origin/main`, `origin/master`, `main`, `master`, remote before local at the
same name because a local branch can be behind the remote and a base three
weeks stale is the quiet version of the same bug.

**Every candidate goes through `usableStartPoint`, and the primary path
skipping that check shipped a hard regression.** `git symbolic-ref` READS the
symref without resolving its target, so it exits 0 on a DANGLING `origin/HEAD` —
the ordinary state of any clone made before its remote renamed the default
branch, since a later `fetch --prune` drops the branch and leaves the symref
naming it. Adopting that answer hands `worktree add` a reference git refuses
(`fatal: invalid reference: origin/master`), and because the daemon creates NO
pane on failure, worktree-backed panes stopped working in that repository
entirely, naming a branch the user never typed. git itself reports the state as
`warning: ignoring dangling symref`, i.e. as no answer; an unusable candidate
now falls THROUGH rather than being adopted or silently reverting `Add` to the
ambient-HEAD behaviour. **Candidates are FULLY QUALIFIED for a second reason**:
`rev-parse` applies `ref_rev_parse_rules`, which tries `refs/heads/%s` BEFORE
`refs/remotes/%s`, so probing the short `origin/main` finds a local branch
literally named `refs/heads/origin/main` in preference to the remote-tracking
ref — inverting the ordering the paragraph above promises. It also disambiguates
the `^{commit}` peel, which does NOT reject a tag as an earlier comment here
claimed: it PEELS one, so an annotated tag named `master` answers for
`master^{commit}`. **The dash guard is load-bearing rather than a formality** —
git's ref grammar permits `refs/heads/-evil`, and git permutes option parsing
past positional arguments, so a dash-prefixed start-point reaches `worktree
add` as an option (measured: `--force` as a trailing positional is accepted as
the flag).

**`--no-track` accompanies every start-point, and the two are one decision.** A
remote-tracking start-point configures an upstream (`branch.autoSetupMerge`
defaults to true), after which `git push` in the new worktree FAILS and the
remedy git prints — `git push origin HEAD:master` — pushes the feature work
straight onto the default branch if it is pasted; `git pull` merges the base
into the feature branch; and `gitRow` starts rendering ahead/behind counts
against the base for that pane, on a branch whose push does not work. Branching
off HEAD never set an upstream, so tracking would be a behaviour change
smuggled in by a base-selection fix.

**`Add` RETURNS the base it used and the daemon logs it on success.** A wrong
base is invisible at create time by construction — that is the premise of the
whole mechanism — and surfaces days later as a PR whose diff is somebody else's
work, so the log line is the only place anyone can ever confirm which base was
taken. An empty return means no default branch resolved and git used HEAD,
which is logged as that rather than as a blank. Resolution is DAEMON-side and
absent from the wire: `WorktreeSpec` carries no base, so nothing on the client
infers anything about a repository living on the daemon's disk. **An empty
resolution is a real answer** — `git init -b trunk` is a legitimate repository —
and the caller falls back to git's HEAD default rather than refusing to create
the worktree, which would trade one wrong behaviour for a broken one. The
load-bearing tests are the real-git ones (`realgit_test.go`): the defect lived
entirely in what git DOES with an argv every stub test already agreed was
correct, so no stub could have caught it, and the reproduction puts HEAD on a
feature branch with a commit master lacks and asserts the new branch's tip
against master's.

**A create carrying `CreatePanePayload.Worktree` goes to a WORKER goroutine and
answers the requester** (`worktreeAddAndCreate`, `daemon/worktree_add.go`).
`handleCreatePane` runs on the requesting conn's dispatch goroutine, so a
checkout there blocks that client's input for as long as it takes — the hazard
that moved browse, discover and claudesessions onto workers. The answer is a
request-response pair over the existing `create_pane_resp`, never a broadcast:
the requester holds a layout placeholder armed before the send, and a broadcast
would show one client's failure to every other while giving the requester
nothing to unwind with.

**A worktree CWD is exempt from `handleCreatePane`'s fallback, and the
exemption has to be explicit.** That function substitutes `d.defaultCWD()` for
any CWD that fails its stat — right for a browsed directory, catastrophic here,
where the directory IS the isolation: the pane would come up on `master` while
the user believes it is isolated. `createPaneInWorktree` therefore stats and
FAILS, and destroys the pane if the spawn then fails, so the guarantee is "a
failure produces no pane" rather than "usually no pane".

**`worktreeAdding` is its own single-flight slot, and it doubles as the permit
budget.** The dialog LISTS a directory's worktrees and then CREATES one, so
sharing `worktreeScanning` would reject each step exactly when it followed the
other (same reason `dirsChecking` is not `browseScanning`). One add at a time
daemon-wide is what makes a 120 s `claimBlockingFSCall` safe: at most one
permit is ever held long, however many clients ask.

**`Pane.WorktreeOwned` is PERSISTED; `Pane.SpawnError` is NOT.** Ownership is
the only thing that lets `spawnRestoredPane` tell a missing WORKTREE from a
missing browsed directory — the snapshot stores only CWD otherwise — and a
worktree-owned pane whose directory is gone comes up unspawned with the reason
on screen instead of relocating (for a claude pane the relocation is worse than
a wrong directory: it still resumes its recorded session, continuing against
the wrong tree). Ordinary panes keep the blank-and-fall-back behaviour; their
loss is a convenience, not the isolation. The error is runtime-only because a
fresh daemon re-stats, and a stored one would resurrect a complaint about a
worktree since restored. The pane offers `Alt+R` and nothing more specific:
Quil records the worktree path, not the repository it branched from.

**Replace mode is SUPPORTED, and what makes it safe is ordering rather than a
guard.** It was refused at first, on both sides, because the client set
`leaf.Pane = nil` and called `old.Dispose()` BEFORE the send — so a failed add
cost a LIVE pane, and unlike a dangling placeholder a `Dispose()` is not
something `PrunePlaceholders` can undo. That is a statement about WHEN the
client disposes, not about the operation: replacing a scratch shell with an
agent on a fresh branch is an ordinary thing to want, and the refusal was
overruled by the project owner on exactly those grounds.

Daemon: `worktreeAddAndCreate` creates the worktree FIRST and only then calls
`replacePaneAt` (extracted from `handleReplacePane` so the worktree path can
report failure instead of logging it), so every failure path returns before the
pane being replaced is touched. Client: a worktree replace holds the detached
pane in `Model.worktreeReplaced` instead of disposing it. An ordinary replace
still disposes at send time, because there the daemon destroys the pane the
moment it handles the message and there is nothing to go back to.

**The SUCCESS dispose lives in `rebuildTabs`, not in `applyCreatePaneResp`, and
putting it in the handler was a leak on every successful replace.** The daemon
calls `broadcastState()` and THEN `respondTo`, both must-deliver on one serial
reader — so the broadcast that fills the reserved leaf is processed first and
clears `worktreeCreates`, after which the response handler bails on its own
`worktreeCreates[tabID] == ""` guard and never reaches its dispose. A pane
really landing in the leaf is the only proof of the swap that arrives reliably.

**`rebuildTabs` must SKIP the held pane, or a routine broadcast dismantles the
whole request.** While the add runs the daemon has not swapped yet, so it keeps
reporting the OLD pane id — which is absent from the tree because the client
detached it, and `existingPanes` is built from `tab.Leaves()`. Without the skip
that reads as a new pane: an empty `PaneModel` is built for a live one, the
reserved leaf is consumed, and `worktreeCreates` is cleared, after which nothing
can settle the held model. The trigger is ordinary — the git ticker alone
broadcasts every 5 s against a window that is seconds to minutes wide.

**`CreatePaneRespPayload.Swapped` is a statement about what HAPPENED, and it is
not derivable from `Error`.** The swap precedes the new pane's PTY spawn, so a
spawn failure is an error with the old pane already destroyed; a client that
inferred "error means untouched" restored a pane the daemon no longer had.
`settleReplacedPane` is the single restore/dispose choke point — the three
settling paths had this subtly different when they were written out separately,
including a timeout that mutated the leaf before its own nil-root guard.

**One worktree create per tab, refused in `handleCreatePaneSplit`.**
`worktreeCreates`, `worktreeReplaced` and `pendingSplit` are all keyed by tab,
so a second create overwrites all three — leaking the first held pane and
pointing the first's response at the second's leaf. Reachable from the keyboard
because the dialog closes on submit and `ActivePaneModel` adopts another leaf
once the first replace detaches its pane. The daemon's `worktreeAdding`
single-flight would refuse it anyway; this just says so when the user asks.

**Every abandonment after a successful add REMOVES the worktree**
(`removeWorktreeFn`, paired with `addWorktreeFn` so a stub can observe the
undo). The tab or the replace target can be closed inside the checkout window;
returning without cleanup strands a full checkout plus a branch pointing at it,
and the next attempt at that name fails with "already exists" against a
directory the user never made. `gitworktree.Remove` is the one place `--force`
is right — the tree was created by this daemon seconds ago and handed to nobody
— and it removes the worktree BEFORE the branch, because git refuses to delete a
branch a worktree still has checked out.

**The replace target is validated against `p.TabID`, before AND after the add.**
`ReplacePane` resolves the pane id globally and swaps it inside its OWN tab, so
a payload pairing tab A with a pane in tab B destroys B's pane while the
response echoes A — and the client arms and unwinds its placeholder on the
echoed tab. Any IPC client can send the pair; the pre-existing `p.TabID`
existence checks read as tab scoping and were not.

**A placeholder leaf RENDERS, and it did not used to.** `renderNode`'s
`IsLeaf()` is `Pane != nil`, so a placeholder fell through to the split arm and
joined two empty children — the empty string. Invisible while a create took
microseconds; a black rectangle for the ~16 s a `git worktree add` takes against
a large repository, and on the replace path the whole tab, because the pane it
stands in for has already gone. `resizeNode` records the placeholder's rect
(`phW`/`phH`, runtime-only — persistence goes through `SerializedNode`) and
`renderPendingPane` draws a box naming the branch, sized from that rect so it
cannot paint over a sibling in a split. `worktreeCreates` is
`map[string]string` (tab → branch) rather than a bool set, and `TabModel.CreatingBranch`
is pushed from the SAME read that decides the prune exemption, so the
placeholder and the message standing in it cannot disagree about whether a
create is in flight.

**That push is a MIRROR of `worktreeCreates`, and the `rebuildTabs` write alone
left the box blank for the whole wait it exists to explain.** `rebuildTabs` runs
on a broadcast, and a worktree create is the one request that guarantees none:
`handleCreatePane` hands it to a worker and returns without broadcasting, and
`gitWatcher` broadcasts only when a git fingerprint actually MOVED — so on an
idle tab the next broadcast is the one the finished checkout produces, and
`CreatingBranch` was still `""` until then. The mirror is therefore written at
all three moments the map is: the submit (`handleCreatePaneSplit`, both arms),
the settle (`applyCreatePaneResp`'s failure branch and `applyCreatePaneTimeout`,
so a branch that just FAILED cannot label the next placeholder), and every
broadcast. `worktreeCreates` remains the single source of truth — it is what the
response handlers settle, and two independent copies of "is a create in flight"
is how a placeholder outlives its request.

**A placeholder with NO worktree says what it is starting**, via
`LayoutNode.phType` — runtime-only beside `phW`/`phH`, so persistence is
untouched. It lives on the NODE rather than in a tab-keyed map because that is
what gives it no lifecycle: it dies with the placeholder, where
`worktreeCreates` / `worktreeReplaced` / `pendingSplit` each have to be unwound
by hand on four paths. Blank stays the answer when neither value is known (a
node built by reconciliation) — inventing a subject for a wait nobody can
describe is the same confidently-wrong answer as claiming a worktree.

**"It dies with the placeholder" is only true because two sites make it true.**
A node that stops being a placeholder is not destroyed: a REPLACE writes the
label onto an EXISTING leaf, and `RemoveLeaf` promotes a sibling into a parent
IN PLACE. So `fill` (the single choke point every "give this placeholder its
pane" site goes through — `FillPlaceholder`, `rebuildTabs`, `settleReplacedPane`)
clears the label with the arrival, and `RemoveLeaf` carries `phType` alongside
the other five promoted fields. Without the pair, a placeholder promoted into a
slot an earlier create passed through renders that create's label.

**`splitPane` owes the same in-flight refusal `handleCreatePaneSplit` makes**,
and for one more reason than that one has. It is the split the KEYBINDINGS and
the command palette call, so it is the split people actually use; `pendingSplit`
is keyed by tab, so a second placeholder overwrites the leaf a worktree create
reserved and the pane still on its way lands nowhere. And because `renderNode`
hands the TAB's `CreatingBranch` to EVERY placeholder leaf, a second placeholder
in that tab renders "Creating worktree …" while having no worktree at all. The
tab-level branch is correct exactly while a tab holds at most one placeholder,
and that refusal is what keeps it so.

**The WHOLE line is budgeted, not just the branch, and the box is CLAMPED.**
lipgloss `Width`/`Height` pad but never truncate, so granting the branch `w-4`
while prepending an 18-cell literal produced a line up to `w+14` wide that
wrapped into extra rows — and a box taller than the rect `resizeNode` recorded
pushes every sibling below it down. Measured before the fix: a 10×4 leaf (the
documented minimum) rendered 10×5, and 1×4 rendered 1×18. `MaxWidth`/`MaxHeight`
are the clamp; the whole-line `truncateCells` is the budget; either alone
happens to hold today, which is why the test asserts the PROPERTY (fits its
rect) rather than one mechanism.

**An empty branch renders BLANK, deliberately.** `renderNode` reaches this for
any childless nil-`Pane` node, including an ordinary split's placeholder, which
has no worktree at all — and an ordinary create over ssh is not microseconds.
Claiming "Creating worktree…" there is the same class of confidently-wrong
answer the rest of this feature exists to remove.

**`PrunePlaceholders` cannot repair a ROOT placeholder** — it only inspects a
split node's CHILDREN — and a replace on a single-pane tab creates exactly that.
`rebuildTabs`' root-insert fallback therefore checks `len(tab.Leaves()) == 0`
before indexing `[0]`. Latent until the held-pane skip above landed: the
broadcast used to refill the root in the same pass that consumed the leaf, which
masked the panic.

**`handleCreatePaneSplit` tears the setup dialog down before it builds the
payload.** The branch name is captured with the other choices at the top;
reading it at the payload yields `""` and every "new branch" silently becomes
an ordinary pane in the repository root — the exact relocation the feature
exists to prevent. The teardown also clears the worktree state, or the next
Ctrl+N inherits a branch name and a repository the dialog no longer shows.

**`applyWorkspaceState` DOES prune unfilled placeholders — on every broadcast,
for every tab — and that is exactly the hazard here.** For an ordinary create a
placeholder is unfilled for microseconds, so no broadcast lands inside the
window; a `git worktree add` holds it for SECONDS, and spontaneous broadcasts
land there routinely (a child toggling mouse modes, a pane exiting, a
git-fingerprint change, another client). Pruning then DETACHES the node while
`pendingSplit` still points at it, so the pane that finally arrives is assigned
to an unreachable leaf and shows up nowhere until a later broadcast heals it
through the root-insert fallback. `rebuildTabs` therefore skips the prune for a
tab in `m.worktreeCreates`, and `applyCreatePaneResp` / `applyCreatePaneTimeout`
are then the ONLY things that can retire it — both delete the map entry, so the
exemption cannot outlive the request that armed it. Success deliberately
retires nothing: the pane is about to land in that slot. `createPaneTimeout`
exceeds the daemon's `worktreeAddTimeout` — asserted, not left as two drifting
numbers — so the client can never prune a placeholder the daemon is about to
fill.

**`worktreeCreates` is a MAP keyed by tab, never a scalar "the create I last
started".** The setup dialog closes on submit, so a second Ctrl+N create can
begin while the first is still checking out — and the daemon's single-flight
rejects the second IMMEDIATELY, so its response routinely arrives BEFORE the
first's. A single slot is overwritten by the second and then cleared, stranding
the first tab's placeholder permanently (every later pane in that tab is
swallowed, silently) or unwinding the second's live one. The handler keys on
what the daemon ECHOED (`p.TabID`, plus a non-nil `p.Worktree` so the MCP
bridge's own `create_pane_resp` is ignored), which is what `protocol.go` calls
the client's staleness key.

**A test walking the layout tree for placeholders must not use `IsLeaf()`.** It
is `Pane != nil`, so a placeholder is not a leaf by that definition and an
`IsLeaf`-gated walk recurses into its two nil children and returns 0 for every
tree — silently making every placeholder assertion vacuous. Match `Left == nil
&& Right == nil && Pane == nil`, the same shape `PrunePlaceholders` itself
uses.


## The NEW-TAB worktree placeholder (pane-level, not LayoutNode-level)

Everything above describes the SPLIT path's placeholder: a `LayoutNode` with
`Pane == nil`, labelled from `phType` / `TabModel.CreatingBranch`, filled by
`fill`. A new TAB created on a new branch (Ctrl+T → worktree) uses a **different
mechanism**, and conflating the two is the mistake to avoid — a new tab has no
existing pane to split from and no client-side placeholder at all, because the
tab id does not exist until the daemon mints it.

There the placeholder is a REAL PANE. `handleCreateTab` calls
`constructPreparingPane` (`daemon.go`) instead of `constructPaneAt`: it publishes
a pane into the tab and **spawns nothing**, recording the branch on
`Pane.PreparingWorktree`.

**It used to spawn a live `terminal`, and that was the bug.** A shell in the
repository root is indistinguishable from a create that FINISHED and put the user
in the wrong tree — for the whole of a checkout, which is minutes on a large
monorepo — and a failed add left that shell standing with the reason nowhere but
`quild.log`, behind a three-second status-bar flash. The tab still cannot be
pane-less (`createFirstPaneWorktree` documents why: blank active tab, persisted
blank by any snapshot in the window, nothing recovers it), so the answer is a pane
that is honestly not ready rather than no pane.

**`Pane.PreparingWorktree` is runtime-only and broadcast-only**, exactly like
`SpawnError` beside it: a snapshot landing inside the checkout window would
otherwise restore a pane waiting on an add no daemon is running, and nothing would
ever settle it. The two fields are MUTUALLY EXCLUSIVE by construction —
`failPreparingPane` clears the branch in the same `PluginMu` span it writes the
error — and `syncPaneMeta` copies both UNCONDITIONALLY, because an absent key is
what ends the wait.

**The branch is validated in `handleCreateTab`, before the placeholder exists.**
`worktreeAddAndCreate` validates too, but it runs on a worker goroutine launched
LAST — so validating only there let an unvalidated name reach `PreparingWorktree`
and go out on a full `workspace_state` frame to every attached client first. Any
IPC client can send `create_tab`. The two checks are a ladder, not a duplicate
(the `uniqueProjectName` arrangement); a refused spec drops to a plain terminal
and answers the requester with the error the worktree path would have sent.

**`handleRestartPaneReq` REFUSES a pane whose `PreparingWorktree` is set**, and the
refusal is daemon-side because the MCP `restart_pane` tool reaches it too.
Restarting a placeholder spawns a shell into the same pane object while the
checkout goroutine still holds that id: the shell renders hidden behind the
preparing block, and whichever outcome lands next clobbers it — success destroys
the pane in `replacePaneAt`, failure writes `SpawnError` over a pane now holding a
live PTY child nobody closes. Keyed on `PreparingWorktree` and never on
`SpawnError`: the pane a FAILED add leaves must still restart, since `Alt+R` is
what its error screen offers (it respawns as the DOWNGRADED `terminal`, never the
requested agent type — `firstPaneType`'s reason applies to the retry too).

**`PaneModel.spinnerRunning()` exempts this state from `restoreSettled`.** Those
caps bound a pane BOOT — `restoreMinDisplay` up to `restoreSafetyCap`, seconds —
while a checkout runs for minutes and the daemon allows two, plus a FRESH
`worktreeAddTimeout` for the cleanup an abandonment runs. A frozen glyph in front
of live work is worse than none. It also cannot gate on `screenBlank()` as the
restore indicator does: there is no child to paint over it, so blank is permanent
rather than a window that closes.

**`preparingBranchCap` (`worktree_client.go`) bounds the branch at INGEST**, which
is the one place in this feature where render-time bounding is not enough. The
spinner advances every 100 ms and `spinnerFrame` is in `paneRenderKey`, so the
frame cache absorbs none of it, and `renderPreparingWorktree` makes four `O(len)`
passes per frame — one of which segments the whole string into graphemes. A frame
may carry megabytes. Same shape as the project form's `formMsgNameCap`, and the
reason it sits at ingest rather than at render is that a future second render path
cannot forget it there.

**The branch listing that feeds the dialog's refusal rides the EXISTING worktree
listing** (`WorktreeListRespPayload.Branches` / `BranchesTruncated`,
`gitworktree.Branches`) rather than a seventh request-response pair. It shares one
deadline and one blocking-FS permit with the listing it rides on, and is skipped
entirely outside a repository — the setup browser asks about every directory the
user walks through and most are not repositories, so a second subprocess each
would be a per-keystroke cost for an answer that is always empty. A failure is
non-fatal: the worktree list is what the dialog needs to function, and losing the
branches degrades to git's own refusal at create time.

**`refs/heads` ONLY.** A remote-tracking ref of the same name is not a collision,
so including `refs/remotes` would refuse names `git worktree add -b` accepts —
the false-positive direction, which blocks legitimate work with a message that is
simply wrong. **Absence is never evidence**: `branchTaken` refuses only on a
POSITIVE match, which stays a true positive however short the list is, so a
truncated or failed listing means "no opinion", never "available". That is why
`BranchesTruncated` is carried and deliberately NOT acted on — it exists so a
future affirmative ("✓ free") cannot be written against a list that is not
evidence of absence. The comparison is EXACT: folding case would refuse `Feat/X`
on a repository holding `feat/x`, which git accepts wherever its ref store is
case-sensitive.

**`worktreeState.validateNewBranch` runs syntax FIRST, and that is a security
property.** `ValidateBranch` bounds the name at 255 bytes and rejects every rune
below 0x20, so the interpolation below it is bounded and control-free; swapping
the two would put an arbitrary-length escape-bearing string into a dialog row with
no bound of its own. One function for BOTH call sites (the name field's Enter and
`submitSetupDialog`) because they are reached by different routes — Tab is handled
above the field dispatch, so tabbing away and pressing Continue never runs the
field's Enter, and a check in one but not the other is a name the dialog refuses
and the button beside it accepts.

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

**The answer is ADVISORY, and the client timeout is set from that.** The toggle
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
