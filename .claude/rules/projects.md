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
