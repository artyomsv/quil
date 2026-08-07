---
description: Projects (the grouping layer above tabs), the multi-daemon router, and the git subsystem behind the sidebar. Load when touching project state, the sidebar, destination routing, or gitinfo.
paths:
  - "**/internal/daemon/project.go"
  - "**/internal/daemon/gitcache.go"
  - "**/internal/gitinfo/**"
  - "**/internal/gitworktree/**"
  - "**/internal/daemon/worktree.go"
  - "**/internal/daemon/worktree_add.go"
  - "**/internal/tui/worktree_client.go"
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

`elideMiddle` takes both halves by CELL budget. Deriving head/tail from cells
and slicing `[]rune` with them makes each half overrun on wide glyphs, and once
the rune count drops below head+tail the two slices OVERLAP — the row repeats
characters at roughly twice the width asked for, and `padOrTrunc` re-clamps it
so nothing downstream complains.

**No sidebar state glyph may be an EMOJI-CAPABLE codepoint** (`glyphBlocked` /
`glyphWorking` / `glyphDone` / `glyphIdle`, pinned by
`TestSidebarGlyphs_OneCellAndNotEmojiCapable`). A font is free to answer such a
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

**`truncateCells` and `lastCellsToWidth` cut on GRAPHEME CLUSTERS, and both
halves of that are load-bearing.** A rune is not the unit of width — U+FE0F
measures 0 alone and makes the pair before it 2 — so summing independently
measured runes returns a string WIDER than the budget it was handed, which
`renderSidebar`'s closing `.Width(w)` wraps rather than cuts, shifting every row
below while `sidebarRowAt` still maps screen row y to `rows[y-1]`. Re-measuring
an accumulated prefix instead is correct but QUADRATIC: it reallocates the
prefix each step, and a zero-width cluster never advances the budget, so the
loop cannot exit early on a long run of them. Both failures are reachable from
ordinary remote text, because `sanitizeRemoteText` is a control-character
filter and preserves printable non-ASCII byte-identically — it is not a
bounding pass. `lipgloss` must remain the sole measurer (uniseg segments,
lipgloss measures), or the cut can disagree with the `.Width` that paints.

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
pane in `Model.worktreeReplaced` instead of disposing it — `applyCreatePaneResp`
disposes it on success (the swap really happened, so the model describes a pane
that no longer exists) and puts it back in its leaf on failure or timeout. An
ordinary replace still disposes at send time, because there the daemon destroys
the pane the moment it handles the message and there is nothing to go back to.

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

