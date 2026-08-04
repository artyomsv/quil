---
description: Projects (the grouping layer above tabs), the multi-daemon router, and the git subsystem behind the sidebar. Load when touching project state, the sidebar, destination routing, or gitinfo.
paths:
  - "**/internal/daemon/project.go"
  - "**/internal/daemon/gitcache.go"
  - "**/internal/gitinfo/**"
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
therefore branches on `projectFormDest != ""`: an adoptable project is renamed
in place (its tabs are already inside it, which is what puts them under the
chosen name), a host with a user-named project refuses a second. A named
project wins over an unnamed one when both are present — reachable for hosts
that predate the rule — because adopting there would rename a project the user
is not looking at.

**The local daemon is deliberately exempt**, and applying this there would be
wrong twice over: it would refuse the second project on the machine the user is
sitting at, and its create would rename the Default holding their existing work
rather than adding beside it.

**Hosts connected before the flag existed keep an unmarked Default** — the
migration runs once and does not re-run, so the record has no `bootstrap` key
and parses as false, i.e. as the user's. That host is then "occupied" and its
create is refused, which the message answers by pointing at rename. Absence
MUST mean the user's: the alternative makes every pre-flag project adoptable
and a create silently renames real work.

**Disconnect is client-side only, and that asymmetry is what generates
duplicate projects.** The sidebar rows vanish, so the host looks emptied — but
the remote daemon keeps every project, and the next connect replays them all.
A user who re-creates "the project that disappeared" ends up with two rows
carrying the SAME name and the SAME host, which the sidebar cannot tell apart
(it renders name + host and nothing else), so neither can the user: removing
the wrong one takes its tabs. `submitNewProject` refuses a name already present
on `projectFormDest` (case- and space-insensitive, since neither makes the rows
distinguishable). Scoped to ONE dest deliberately — the same name on a laptop
and a build host is ordinary, because the row carries the host. The client
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
the same archive again cannot change that. The LAUNCH path deliberately does
not act on the sentinel — a background destination is dropped with a log line,
because installing software on another machine must not be a side effect of
opening the client. Shipped as the raw gate message plus `run quil remote
setup <host>`: the machinery to fix it had existed since the auto-install
work and was reachable only from `--remote`.

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

`elideMiddle` takes both halves by CELL budget. Deriving head/tail from cells
and slicing `[]rune` with them makes each half overrun on wide glyphs, and once
the rune count drops below head+tail the two slices OVERLAP — the row repeats
characters at roughly twice the width asked for, and `padOrTrunc` re-clamps it
so nothing downstream complains.

**The sidebar is a real reserved column**, unlike the notification sidebar's
overlay — but it must not re-mode a pane. `paneVTSize` takes SIZE from the rect
and MODE from `nativeW` (the width the rect would have with the sidebar
closed), because a 22-column sidebar moved an even split from 92/93 to 81/82,
straddling `min_native_cols` and flipping ONE of two identical siblings to a
161-column cropped canvas.

## Git subsystem (`gitinfo` + `gitcache.go`)

Three plumbing calls per checkout: branch, linked worktree, ahead/behind.
`git status --porcelain` is deliberately excluded — the one call that can take
seconds on a large repository without fsmonitor.

`referencedDirs` bounds what is PROBED; `sweep` is what bounds what is STORED.
OSC 7 rewrites a pane's CWD on every `cd`, so without it one shell roaming a
monorepo adds a map entry per directory it visits and `byDir` keeps every
checkout ever seen — against a daemon that runs for weeks.

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
