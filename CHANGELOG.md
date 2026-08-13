# Changelog

All notable changes to Quil will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Desktop notifications on Windows.** When an agent parks waiting on you, or
  finishes a turn while you were away, Windows raises a toast — and clicking it
  puts Quil on that exact project, tab and pane. It fires on the same two
  states the project sidebar already marks (▲ and ✓) and no others, only while
  the terminal is unfocused, and at most once per pane per 30 seconds; six
  agents finishing together give six separately clickable toasts rather than a
  storm. Answering a prompt withdraws its toast, so Action Center never goes on
  claiming attention you have already given.

  Registration is explicit and reversible: `quil notify setup` writes a Start
  Menu shortcut and a `quil://` handler, prints exactly what it wrote, and
  `quil notify setup --remove` undoes both. Nothing is written as a side effect
  of a config flag. `quil notify status` reports where you stand and
  `quil notify test` sends one labelled canary. Toggle it live at F1 → Settings
  or under `[notification.desktop]`; the Settings row reports whether
  registration is actually in place rather than echoing the flag.

  Clicking a toast can only move your cursor — the handler validates a pane id
  and forwards it over a per-PID named pipe, with no path to spawning a pane,
  sending input or running a command. Windows only: no transport on macOS or
  Linux can carry a click back to a specific pane. Note that Windows indexes a
  new Start Menu shortcut on its own schedule, so toasts may be dropped for a
  while after setup — `quil notify test` tells you when it is live.

## [1.55.1] - 2026-08-13

### Fixed
- **Closing the lazygit overlay now reclaims it.** `Alt+G` only hid the overlay
  — the lazygit process kept running for the life of the tab, so a session that
  had opened it in several tabs carried one live process per tab indefinitely
  (measured at ~116 MB each). Hidden overlays are now closed after five minutes,
  and at most five are kept alive at once, the least recently shown being
  dropped first. Both limits are configurable in F1 → Settings and under
  `[overlay]` in `config.toml`; `0` turns either off.

## [1.55.0] - 2026-08-11

### Added
- **A remote host running an older Quil now offers to upgrade itself.** After
  updating this client, a configured host still on the previous version cannot
  be attached to — client and daemon must match. Quil showed that as a parked
  row with a lightning bolt and nothing else, and the only way forward was
  `quil remote setup <host>` in a shell. It now asks, at startup and whenever a
  live link drifts out of version, and performs the same push itself when you
  press `y`. It says which versions are involved and that the remote daemon
  restarts, since panes there respawn.

## [1.54.1] - 2026-08-11

### Fixed
- **"The local daemon is gone — its panes are lost" could appear over a daemon
  that was perfectly healthy.** If a write to a client stalled for 30 seconds —
  reachable on a busy workspace, where a single frame can outlast the window
  while the client is repainting — the daemon stopped being able to send to that
  client but went on reading from it, silently. Nothing noticed until its
  send queue filled several minutes later, by which point the TUI had been
  starved of updates the whole time and then told the daemon was gone. A stalled
  write is now retried while the client is still draining, and a client that
  accepts nothing at all is disconnected promptly and visibly instead of four
  minutes later. The same fault on the client side is what left a TUI logging a
  send failure every five seconds for hours after a drop.
- **Losing the connection to the local daemon is no longer the end of the
  session.** It used to be treated as fatal on the assumption that a local
  daemon only ever disappears by dying, taking its panes with it — so a dropped
  connection to a daemon that was still running left the client with no way
  back, and `ctrl+q` as the only option. Quil now reconnects to it the same way
  it reconnects to a remote host, with a few seconds' grace so a `quil restart`
  reattaches by itself. A daemon that really is gone still says so, and now
  offers `r` to retry once you have started it again.

## [1.54.0] - 2026-08-09

### Added
- **Mark attention now survives a restart.** The pin was TUI-session state, so
  every mark you set was gone the next time you opened Quil — which is most of
  what a mark that never auto-clears is for. It lives on the daemon now,
  alongside the pane's mute setting, so it survives a TUI restart, a daemon
  restart and a reboot, and reads the same in every client attached to that
  daemon.
- A pinned pane is now visible in the two places that never showed it. The
  project sidebar row carries a `◆N` count of its pinned panes, beside the
  existing needs-you / running / finished counts — independent of them, since a
  pinned pane is usually also doing something. The tab bar puts a `◆` before the
  label.
- An out-of-date remote daemon can be repaired without leaving the TUI: open
  the New Project dialog and enter the same host, and Quil upgrades it and
  reconnects. `quil remote setup <host>` still works as before.

### Changed
- Manual and automatic attention marks no longer share a colour. A pane you
  pinned by hand and a pane that finished a turn while you were away were both
  green, on the pane border and in the tab bar, so the one that clears itself
  when you look at it was indistinguishable from the one that waits for an
  explicit Unmark. Pinned is now purple everywhere — border, tab, sidebar glyph
  and project count — and green means only "finished while you were away".
- A pinned tab keeps its `◆` even when a more urgent state has claimed its
  colour, so a tab that is both blocked and pinned shows both facts rather than
  only the amber.

### Fixed
- The project sidebar's state badges were all painted the same grey as the
  project name. The counts use the same glyphs as the pane rows below them —
  ▲ needs you, ◐ still running, ✓ finished while you were away — so the badge
  is meant to read as a roll-up of those panes, but with every state in one
  flat colour it read as three anonymous numbers instead. Each count now
  carries the same colour its pane rows do: amber for blocked, blue for
  working, green for finished.
- A remote project's connection glyph was grey for the same reason, so a
  destination that had given up reconnecting (⚡) looked identical to one that
  was still retrying on its own (⟳). Parked is now red and retrying orange —
  deliberately not the amber the sidebar reserves for "waiting on you", which
  a link healing itself is the opposite of.
- A remote host that was unreachable when Quil started lost its projects from
  the sidebar entirely, with no way to get them back. Because updating Quil
  leaves the remote daemon on the older version, this happened after every
  update until the host was upgraded by hand — so the projects looked deleted
  rather than disconnected. Those rows now stay in the sidebar, their names
  marked offline, and come back on their own once the host is reachable again.
- Quil gave up on a remote host after 25 seconds, which is less than the 30
  seconds the daemon on that host is allowed for its own startup — so a host
  that had just rebooted was abandoned while it was still starting normally.
- Diagnostics reported by a remote host are sanitized before being printed, so
  a compromised or misbehaving host cannot write escape sequences to your
  terminal at launch.

## [1.53.3] - 2026-08-09

### Fixed
- The TUI quit itself, reporting a clean exit, on workspaces with many tabs.
  Every workspace update made the client re-send the layout of every tab and
  the size of every pane whether or not anything had changed — one guaranteed
  delivery each, against a 64-slot outbound queue. Past roughly 64 tabs plus
  panes a single update could overflow that queue, and the client's own
  transport treats a full queue as proof the other end is dead, so it closed
  the connection and the session ended with no error anywhere. Reordering tabs
  by dragging made it far more likely by producing an update per slot crossed,
  but the trigger was only ever the tab count: it first fired seven seconds
  after a 33rd tab was created, on an update no drag was involved in. The
  client now sends only what actually differs from the state the daemon just
  reported, and only for the daemon that reported it.
- A client whose outbound queue fills now waits a few seconds for it to drain
  instead of disconnecting immediately. That rule exists so a daemon can drop
  one unresponsive client rather than stall the others; applied to a client's
  own connection it had nothing to protect and merely ended the session. A
  daemon that stays unresponsive past the grace period is still reported as a
  lost connection, so nothing you typed is silently discarded.
- Restored panes still receive the first resize after attaching or
  reconnecting, which is what prompts them to repaint. Only repeats of a size
  the daemon already has are suppressed.
- The "dropping slow client" warning now identifies the connection, its queue
  depth, and the limit. It is emitted by code shared between the daemon and
  every client, so on its own it named neither.

## [1.53.2] - 2026-08-09

### Fixed
- A pane created with a new git worktree branched off whatever the main
  checkout was currently on, rather than off the repository's default branch.
  Creating a fix worktree while the main checkout sat on a feature branch
  produced a branch carrying that feature's unmerged commits, so the pane was
  isolated in its directory but not in its history and its diff against master
  was the feature. The base is now the repository's own default branch —
  `origin/HEAD` where it is set, otherwise `origin/main`, `origin/master`,
  `main` or `master`, preferring the remote ref over a local one that may be
  stale. A repository with none of those (`git init -b trunk`) still works and
  falls back to HEAD as before, as does one whose recorded default branch no
  longer exists — common in clones made before their remote renamed `master`
  to `main`, where the recorded answer outlives the branch it names.
- The new branch is created with no upstream, matching what Quil did before.
  Branching from a remote-tracking ref would otherwise configure one, and `git
  push` in the new worktree would fail with advice that pushes the work onto
  the default branch.

## [1.53.1] - 2026-08-09

### Fixed
- A tab was marked amber ("blocked on you") while its agent was still
  demonstrably working with background subagents running. Claude reuses the
  same notification for a permission prompt and for its own idle nudge, and
  the idle nudge — arriving after the turn already finished — was
  misclassified as a fresh block, hiding the pane's working indicator behind
  the "needs you" marker. Only Claude's idle nudge is exempted, and only once
  the turn has ended: any other notification still marks the tab, so a
  permission prompt is never swallowed.

## [1.53.0] - 2026-08-09

### Added
- Sidebar PANES section scrolls with the mouse wheel, with markers showing how
  many rows are hidden above and below. The PROJECTS list stays pinned.
- Sidebar tab headings show their 1-based number (matching Alt+1-9) and the
  tab's custom colour.
- Pinned attention is now visible on sidebar pane rows.
- Tabs holding a pane parked on a permission prompt are marked amber.

### Fixed
- A pane stayed marked "blocked on you" for the whole time an agent was working
  after a Bash/Edit/Write permission prompt was approved. Approving fires no
  hook of its own, so answering the prompt — typing or pasting into the pane —
  is now what clears the mark.
- The "needs you" marker is hidden on the pane you are currently in — you are
  looking straight at the prompt — while the tab, the project roll-up and
  Alt+Shift+A keep showing it until you answer. Looking at a pane, scrolling it
  or dragging a selection across it is not an answer: switch away without
  replying and the marker is still there.
- Right-clicking a pane row in the sidebar opens its context menu; the menu now
  acts on panes in background tabs instead of silently doing nothing.
- Alt+Shift+A scrolls the pane it jumps to into sidebar view instead of
  possibly leaving it below the fold.
- After the terminal is resized, the first wheel notch over the sidebar moves
  the pane list again — several notches could previously do nothing.
- A horizontal wheel over the sidebar — a trackpad swipe, or shift-scroll — no
  longer scrolls the pane list downwards. It is ignored, and still does not
  reach the pane underneath.
- The tab you are on stays distinguishable when two or more tabs are amber: an
  active tab parked on a prompt is now underlined, since the amber replaces the
  background that otherwise marks it.
- A pane's context menu no longer acts on whichever pane is on screen when the
  active tab moves out from under the open menu (MCP `set_active_pane` can do
  this). The menu refuses instead of renaming, muting or closing the wrong pane.

## [1.52.2] - 2026-08-07

### Added
- **A pane can now be replaced with one in a new worktree.** Swapping a scratch
  shell for an agent on a fresh branch is an ordinary thing to want, and the
  dialog refused it. The worktree is created before the pane you are replacing
  is touched, so a branch git rejects leaves that pane exactly where it was.
- **Creating a worktree now says so while it works.** A checkout of a large
  repository takes tens of seconds, and the space the new pane will occupy was
  blank for all of it — on the replace path the tab was blank entirely, because
  the pane being replaced had already gone. It now names the branch it is
  checking out.

### Fixed
- **A branch typed for a new worktree was discarded if you pressed Tab.** Tab
  moves between fields without closing the name box, so the dialog went on
  showing `new branch <name>` while Continue silently dropped it — and the pane
  opened in the repository root with no worktree and no error, which is the
  relocation the feature exists to prevent. What the dialog shows is what it now
  does; an incomplete name is refused with a reason instead.
- **Esc in the branch-name box no longer abandons the whole dialog.** It was
  documented as backing out of just the name, but the dialog took Esc first, so
  that never happened.
- **A create that refuses to run now says why.** Three paths could close the
  dialog and create nothing — one of them with no message at all — while the log
  recorded that a request had been sent. Each reports its own reason now.
- **A worktree that could not be handed to a pane is cleaned up.** If the
  checkout succeeded but the pane could not be created — the tab or the pane
  being replaced was closed while git was working — the worktree and its branch
  were left behind, and the next attempt at the same name failed against a
  directory nobody made.
- **A second worktree pane can no longer be started in a tab that is already
  making one.** It replaced the first one's bookkeeping, so the first pane could
  end up neither restored nor closed.
- **A pane being replaced is no longer restored after the swap has happened.**
  If the worktree was created and the pane then failed to start, the old pane
  was already gone; Quil put a dead one back in its place and typing into it did
  nothing.
- **An ordinary split no longer says a worktree is being created** while it
  waits, and the waiting message can no longer overflow into the pane beside it.

### Changed
- **`+ new branch…` moved to the top of the Worktree field**, directly under
  `off`. In a repository with a dozen worktrees it was buried at the bottom.

## [1.52.1] - 2026-08-07

### Fixed
- **A worktree pane could end up running where you cannot see it.** If a daemon
  reported a worktree create as successful without actually creating the pane,
  the split it was going to fill was cleaned up underneath it, and the next pane
  opened in that tab took its place invisibly — leaving a live process, possibly
  a running agent, with no window. The split is now kept until the pane really
  arrives.
- **A remote daemon could disturb a local tab's layout.** Replies about pane
  creation were matched by tab alone, so a reply from one machine could act on a
  tab belonging to another. They are now matched to the machine they came from.
- **The sidebar width setting accepted values it could not use.** On a terminal
  narrower than 100 columns it stored anything, so the dialog could show a width
  the layout was ignoring.
- Branch names for a new worktree now reject Windows device names (`CON`, `NUL`,
  `COM1`, …), which git could never create a worktree for.

## [1.52.0] - 2026-08-07

### Added
- **Quil can now create the worktree for you.** The create-pane dialog's
  Worktree field gained a `+ new branch…` row: name a branch and the pane opens
  in a fresh worktree beside the repository, at
  `<parent>/<repo>-worktrees/<branch>`. Nothing is nested inside your checkout,
  so tools that walk the tree never see two of it.

### Changed
- **A worktree pane that cannot open says so instead of moving.** If the add
  fails, no pane is created and git's own message is shown — Quil never falls
  back to the repository root, where an agent would run on `master` while you
  believed it was isolated. The same applies on restart: a pane whose worktree
  has been removed comes back unspawned, naming the missing directory, with
  `Alt+R` to retry.

## [1.51.0] - 2026-08-06

### Added
- **A new pane can now be opened directly in one of the repository's git
  worktrees.** The create-pane dialog lists them under the directory you pick,
  so an agent, a shell and lazygit can each sit in the same worktree and the
  sidebar shows each pane's real branch instead of repeating the main
  checkout's. Quil does not create worktrees yet — it offers the ones you
  already have.

## [1.50.0] - 2026-08-05

### Added
- **The pane right-click menu can now clear a stuck attention mark.** The amber
  "needs you" mark beside a pane is switched on and off by the agent's own hook
  events, so when the event that would clear it never arrives — the hook stream
  stopped, the session ended without one, the prompt was answered somewhere the
  hooks cannot see — the pane stayed flagged for as long as the client was
  running, and the project row summarising it stayed flagged too. There was no
  way to dismiss it short of restarting. **Clear attention** drops the blocked
  mark, the green finished-while-you-were-away mark and the pin together, and
  greys out when the pane is carrying none of them. It changes what is shown
  and nothing else: the pane's next hook event works out the truth again, so a
  pane that really is still waiting will flag itself straight back.

### Fixed
- **The project row's warning icon no longer paints over its own count.** A
  project with panes waiting on you showed the warning sign with the number of
  them hidden underneath it. The icon was the one symbol in the sidebar that
  fonts are free to swap for a colour emoji, which is drawn about twice as wide
  as the space reserved for it — so it covered the character that followed. It
  is now a symbol with no emoji form, and every state symbol in the sidebar is
  checked against that property so the problem cannot come back through a
  different icon.
- **Sidebar rows containing emoji no longer shift the rows beneath them.** Text
  arriving from a remote daemon — a project name, a pane name, a branch — is
  passed through untouched apart from control characters, and the two helpers
  that cut a row to its column width measured such text one character at a time.
  That undercounts an emoji written with an explicit presentation mark, so the
  cut returned a row wider than the column, which wrapped onto the next line and
  pushed everything below it down by one. Clicking a project then selected its
  neighbour.

## [1.49.0] - 2026-08-05

### Changed
- **Naming a project on a host that already has one now offers to fix it,
  instead of telling you to go and fix it yourself.** The old message —
  *"already has a project (Default1) — rename it instead"* — named a remedy the
  dialog had no route to, and one that does not even work on a host holding
  several: renaming one of three still leaves three. Press Enter and the form
  now describes exactly what it will do — how many projects there are, what the
  result will be called, how many tabs move, and that **nothing is closed** —
  and a second Enter carries it out. Editing the name in between re-describes
  rather than acting on the sentence you moved away from. A host that has gone
  away is reported instead of the fold appearing to have worked.
- **Folding leaves the surviving project's root directory alone.** The dialog
  fills that field in by itself as soon as the directory listing arrives, so it
  usually holds wherever the daemon happens to start rather than anywhere you
  chose — writing that over a root you had picked was a change nobody asked
  for. Use **Rename**, which opens with the project's own root already in the
  field, to move one.

### Fixed
- **A host carrying duplicate projects can now be consolidated without losing
  tabs.** One host holding one project shipped as a rule about what *creating*
  does, so it stopped new duplicates but could not repair a host that already
  had them — and the only tool for the job, *Destroy project*, takes that
  project's tabs and panes with it. Confirming the fold above moves every tab
  onto the surviving project and drops the emptied records; no tab, pane or
  running command is closed. This is what to use on a host connected before
  v1.48.0, where each reconnect-and-recreate cycle left another row behind.

## [1.48.0] - 2026-08-05

### Changed
- **A remote host now holds exactly one project, and naming it is how you get
  it.** A daemon must have at least one tab and a tab must belong to a project,
  so a host always arrived already holding a `Default` nobody asked for — and
  creating your project left that `Default` sitting beside it, holding the tabs
  you actually cared about. Naming a project on such a host now renames that
  project instead of adding a second, so the host's existing tabs end up under
  your name. A host whose project you have already named refuses a second one
  and points you at renaming it. The local daemon is unchanged: it holds as many
  projects as you like. Note for hosts you connected before this release: their
  `Default` predates the marker that makes this work, so it is treated as one
  you named — rename it once and the host is settled.
- **The New Project dialog fills in the host it is aimed at.** Opening it while
  a remote project is active showed **Remote (ssh)** unticked and an empty Host,
  while the form was already targeting that machine — so it read "this machine"
  and acted on the far one. It now shows the host it will use, and waits, saying
  so, if that host has not yet reported what it holds.

### Fixed
- **Connecting a remote host no longer reports its own progress as a failure.**
  The New Project dialog has one message line and was rendering everything on it
  as a red ✗ — so "installing…" and "upgrading…" looked exactly like "cannot
  reach that host", while an install was in fact running normally. The line is
  coloured by what it means now: red for a host that cannot be reached or an
  install that failed, amber while Quil is connecting or provisioning, green
  once the host is connected.
- **Creating a project with a name that host already has is refused.**
  Disconnecting a host is client-side only — the remote daemon keeps every
  project — so its rows leave the sidebar looking deleted and return on the next
  connect. Creating "the same project" again then left two rows showing the same
  name and the same host, indistinguishable from each other: there was no way to
  tell which one held your tabs, and removing the wrong one took them with it.
  The same name on a *different* host is still fine — that row carries the host,
  so the two are told apart on sight. If a create slips past that check — the
  client can only compare against the projects it has been told about, and a
  submit can beat the first update from a host you just connected — the daemon
  gives the new project a numbered suffix (`cluster-management (2)`) rather than
  a second identical name. It disambiguates instead of refusing because a
  refusal there would be silent, and your create still happens.

## [1.47.2] - 2026-08-04

### Fixed
- **Typing no longer scrambles under load.** When the machine was busy — a virus
  scan, a heavy build, a compile in another pane — characters could arrive in a
  pane out of order: typing `image containers` produced `iamg ecotniaesnr`. Every
  character was delivered, just not in the order you typed them. Each keystroke
  was being handed to its own goroutine, and nothing downstream guaranteed those
  goroutines reached the socket in order; normally the gap is nanoseconds, but
  under CPU pressure it widened enough for adjacent keys to swap. Input is now
  queued in the order you type it and forwarded by a single writer, so typed
  order is delivered order no matter how loaded the machine is. Mouse-wheel
  scrolling and paste share that queue, so neither can jump ahead of characters
  still on their way to a pane.
- **Quitting no longer drops the last thing you typed.** Input accepted just
  before you closed the TUI could be discarded while it was still on its way to
  the daemon. It is now flushed to the socket before the connection is released.
- **Paste goes to the pane you asked from.** Reading the clipboard takes a
  moment — long enough to switch panes, and much longer when it holds an image —
  and the paste was delivered to whichever pane was active when the read
  finished. It now goes to the pane that was active when you pressed the key,
  and is held back entirely if that pane's daemon is reconnecting.

## [1.47.1] - 2026-08-04

### Added
- **Every project action is in the command palette.** `Alt+Shift+P` now lists
  the projects you can switch to — matched on the name or the host, so `gpu`
  finds `build@gpu01` — alongside New project, Rename, Destroy (or Disconnect
  host on a remote one), Previous project, "Go to the agent waiting longest",
  and the sidebar toggle. Each row shows its keybinding, so the palette is also
  the fastest way to learn the project keys.

### Fixed
- **The Shortcuts list (F1 → Shortcuts) no longer runs off the bottom of the
  screen.** It drew every row at once, so on an ordinary terminal roughly two
  thirds of the list — and the footer — were simply off-screen, with no way to
  reach them. It scrolls now, with arrows, `PgUp`/`PgDn`, `Home`/`End` and
  `g`/`G`, and the footer shows where you are in the list. It fits a narrow
  terminal too: rows and the position footer are sized against the width the
  box actually gets, so below 76 columns they shorten instead of wrapping onto
  a second line and spending a row the height budget had already counted.
- **The project keys are listed there too.** Seven of the eight were missing, so
  the only way to learn them was the online documentation.
- **Connecting a remote host now upgrades an out-of-date daemon instead of
  refusing.** A host running an older Quil failed the New Project dialog's
  Host row with `daemon on <host> runs 1.46.3 but this client runs 1.47.0; run
  quil remote setup <host>` — asking you to leave the session to run a command
  that does exactly what connecting was already meant to do. It is performed
  from the dialog now, the same push `quil remote setup` makes: the remote
  daemon is stopped, a matching build is installed, and the dial is retried.
  Installing onto a host with no Quil at all already worked; only the
  out-of-date case was missing, because it cannot be recognised from ssh's
  exit code the way a missing binary can. A remote daemon **newer** than your
  client is still refused rather than downgraded, and now names the client
  upgrade instead of a command that would make it worse.

## [1.47.0] - 2026-08-04

### Added
- **Projects — tabs now group under the work they belong to.** A project is
  named, rooted at a directory, and owns its own tabs, remembering which one you
  left it on. `Alt+Shift+N` creates one, `Alt+P` fuzzy-finds, `Alt+O` bounces
  between the last two, and `Alt+Shift+←/→` cycles. Six tabs spread across three
  repositories used to be six indistinguishable labels; `Ctrl+T` now files a new
  tab into whichever project you are on, and switching project switches the
  whole tab bar. Your existing workspace migrates on first load into a single
  project named Default with tab order preserved — no prompt, nothing to opt
  into, no data loss.

- **A sidebar that watches every project, not just the one you are looking at.**
  `Alt+Shift+S` toggles a reserved left column listing your projects, each with
  a roll-up of its agents — `◐N` working, `⚠N` blocked waiting on you. Those
  counts keep updating for projects in the **background**, which is the whole
  point: an agent that finished or got stuck somewhere you are not looking is
  now visible from where you already are. Under the active project every pane
  gets its own mark — `◐` working (with `⋯N` outstanding subagents), `⚠` blocked
  and the tool it is asking about, `○` idle, `✓` finished while you were away —
  and clicking any row jumps there.

- **Blocked is no longer the same as done.** An agent parked on a permission
  prompt and an agent that finished its turn were shown identically. The
  distinction was always present in the agents' own hook events; the old display
  only needed "mark this unseen", so the two were collapsed. They are separate
  states now, because they want different things from you.

- **`Alt+Shift+A` jumps to whoever has been waiting longest.** Anywhere in the
  workspace, across project boundaries, cycling on repeated presses. Oldest
  first rather than sidebar order — with several agents running, the one that
  has been blocked longest is the one costing you time.

- **Each pane shows the checkout it is sitting in.** Branch name, `wt` when it
  is a linked worktree, and `↑N`/`↓N` against upstream. Refreshed on a
  background ticker and cached per checkout, so ten panes in one repository cost
  one git invocation rather than ten. `git status --porcelain` is deliberately
  not among the calls — it is the one that can take seconds on a large
  repository. A probe that does not answer keeps its last value and is marked
  stale rather than blanked or guessed at.

- **Several machines in one window.** A project belongs to the daemon that holds
  its files, so projects from your laptop and from a build host now sit side by
  side in the same sidebar, in one TUI process — where `quil --remote <host>`
  binds an entire session to a single daemon. Add a host without relaunching:
  tick **Remote (ssh)** in the New Project dialog, give a user and host, and
  press Enter on the Host row. Quil dials it, offers to install itself if the
  host has not got it, and then browses *that* machine's filesystem for the root
  directory. The host is remembered under `[[destinations]]` and attached at
  every launch; **Disconnect host** in the sidebar's right-click menu removes it
  from your window and stops nothing on the far end. Each destination keeps its
  own reconnect state, so one daemon dying no longer ends the session.

  Beta limits, worth reading before relying on it: a destination unreachable at
  *launch* never starts a reconnect ladder (relaunch to pick it up), background
  destinations dial non-interactively so a first-time host key must be accepted
  once with `ssh <dest>` or `quil remote setup <dest>`, plugin availability is a
  single registry fed by whichever daemon answered last, and the
  recent-directories list is per client rather than per host.

### Fixed
- **A restored AI pane no longer comes back with its conversation printed
  twice.** When a pane resumes a session, the agent paints its own transcript
  back from the top — and Quil was replaying its saved copy as well. The two did
  not merely stack up: the agent started writing wherever the replay had left
  the cursor, so its banner landed in the middle of a saved prompt line. Quil
  now recognises which panes restore their own history and leaves those alone.

- **A restored terminal no longer starts typing at the top of the pane.** The
  saved screen is now scrolled into scrollback where you can still reach it,
  rather than left on screen for the fresh shell to paint through — and the
  cursor is returned to the top afterwards, so the prompt appears where the
  shell believes it is. Without that last step the prompt sat at the bottom of
  the pane while typing appeared at the top.

- **A restored pane's saved history no longer grows on every restart.** The
  restored bytes and the new process's output were being written back together,
  so the file on disk got a little longer each time. A pane you open now stores
  that session alone; one you never open keeps the history it was restored with.

- **Panes that ignore terminal resizes repaint again after one.** Claude Code
  redraws on input rather than on a resize signal, so a pane could sit with a
  stale, mis-wrapped screen after the window changed size until you typed
  something.

- **Opening the sidebar no longer reformats one pane of a pair.** Reserving the
  column could push two evenly-split AI panes across the width threshold that
  decides how they render, flipping exactly one of them.

- **Queued notifications replay in the order they happened.** After a
  disconnect, events arrived newest-first on reattach, so a pane that had
  started and then finished work was left showing "working".

- **Installing Quil on a host no longer offers to install it again.** Recording
  the new host in your config wrote back a copy of the file made at launch,
  which reverted the binary path the install had just recorded — the one that
  makes attaching work when the remote's non-interactive PATH cannot see the
  install directory. Each install ended by erasing its own result. The
  reconnect ladder had the matching fault: a host added mid-session attached,
  then on its first dropped link retried forever without ever reconnecting.

- **Disconnecting a host makes it stay gone.** A status update already in
  flight could put its projects back in the sidebar, where they were
  unreachable — nothing re-attaches a host the client has forgotten.

- **Closing a tab keeps you in the project you were in.** Closing the active
  tab could jump you to a tab in a different project, and left the project you
  were in pointing at its first tab rather than a neighbour of the one closed.

- **Project names from another machine can no longer act on your terminal.**
  Names shown in the right-click menu, the rename form and the command palette
  are stripped of terminal escapes and text-direction overrides, as the sidebar
  already did.

- **A project root on an unreachable network share no longer freezes the
  daemon.** Resolving it was unbounded, so a dead mount could park every pane
  behind it; it now gives up and falls back to the default directory.

- **Notes close properly when you jump to a pane in another tab.** Jumping from
  the palette, the notification sidebar, the attention queue or an AI agent left
  the notes editor open and bound to a pane you were no longer looking at.

- **Destroying a project cleans up after its panes.** Their hook and session
  files were left behind, and the daemon kept re-reading dead ones until it
  restarted.

## [1.46.3] - 2026-08-01

### Fixed
- **The working indicator went dark while a Claude Code background agent was
  still running.** A pane could sit with no spinner on its border and none on
  its tab while an agent worked for half an hour. The one cue that says
  "something is still happening here" was missing exactly when it was needed,
  and the tab looked idle enough to close.

  Claude Code reports the end of every ordinary turn as a subagent finishing,
  without saying which one. Quil counted background agents rather than tracking
  them by name, so each of those unnamed reports cancelled out a real agent that
  was still working. Once the tally had drifted, later genuine "finished"
  reports were discarded as well, and the pane never recovered until the session
  ended. Quil now tracks background agents by name and clears one only when its
  own completion arrives, so the indicator stays lit for as long as there is
  real work in flight.

- **The notification sidebar no longer fills with blank " done" entries.** The
  same unnamed report was also posted to the sidebar once per turn on every
  Claude Code pane, where it collapsed into a single `" done" ×N` row that
  jumped back to the top every time it fired — pushing real notifications down
  and naming nothing you could act on. Those reports are now discarded where
  they are produced; notifications for background agents that actually have a
  name are unchanged.

## [1.46.2] - 2026-08-01

### Fixed
- **A restored Claude Code pane could come back holding a different pane's
  conversation.** On the first daemon restart after a pane was created, panes
  whose own session could not be found on disk were quietly attached to
  whichever session in the same folder had been touched most recently — in
  practice, the sibling pane that had just respawned. Several panes could end
  up driving one conversation at once, appending over each other. Nothing was
  lost, but the wrong history came back and kept growing.

  The session itself was never missing, only unfindable: Claude files a
  conversation under the folder it is *working in*, so an agent that moves into
  a git worktree takes the conversation with it, and Quil was looking in the
  folder the pane started in. Quil now records where the conversation actually
  lives, and a pane it cannot locate is resumed by name rather than swapped for
  the nearest one — a session Claude rejects is an error you can see, where the
  wrong session is a silent loss. Restore also refuses to hand one conversation
  to two panes: the second pane starts a fresh session instead.

## [1.46.1] - 2026-08-01

### Fixed
- **Attaching to a host whose Quil was removed now offers to reinstall it,
  instead of blaming your CPU.** If the binary on a remote disappeared — the
  machine was rebuilt from an image, the home directory was wiped, an admin
  moved it, the OS was reinstalled — `quil --remote <host>` reported *"Quil was
  installed on <host>, but will not run there"*, suggested the architecture was
  wrong, and never offered to install. The suggested `uname -sm` came back
  looking perfectly correct, because the architecture was never the problem.
  The only escape was deleting a line from `config.toml` by hand.

  Quil recorded where it installed the binary and then read that record as
  proof the binary was still there. It now asks the host instead, and repairs
  the record from the answer: gone means reinstall, and found somewhere else
  means use the new location and reconnect — which also fixes attaching to a
  host where you installed Quil yourself. A genuinely unrunnable binary still
  reports the architecture mismatch, now naming the file it actually found.

  Nothing is forgotten on a failed check: if the host cannot be reached, the
  record is left exactly as it was, since an unreachable host proves nothing
  about what is installed on it. Quil also declines to adopt a Quil it finds in
  a directory that other users on that host can write to — a shared `/opt/bin`,
  or the group-writable `/usr/local/bin` that Homebrew creates on multi-admin
  Macs — because adopting it would run it on every later connection with no
  further prompt. It offers a normal install into your own directory instead.

### Security
- **A remote host can no longer smuggle a command into the diagnostics Quil
  tells you to run.** When a remote binary cannot be executed, Quil prints an
  `ssh … 'uname -sm; file …'` line to paste into your own shell. The path in it
  comes from the remote, and an apostrophe in that path closed the quoting — so
  a malicious or compromised host could append a command that ran on **your**
  machine when you pasted the line. The path is now escaped, which also fixes
  the ordinary case: the suggestion used to break on any legitimate path
  containing an apostrophe.
- **Remote-reported paths can no longer contain invisible text-direction
  overrides.** Quil already rejected control characters in paths a remote
  reports, because those paths are printed in the confirmation prompt you
  approve an install from. Bidirectional overrides are *printable*, so they
  passed that check while reversing how the rest of the path reads on screen.
  They are now rejected too.

## [1.46.0] - 2026-07-31

### Fixed
- **OpenCode and lazygit panes come back with their content after you
  reattach.** They showed an empty rectangle with a live process behind it —
  the same symptom fixed for Claude Code in 1.43.1, which turned out to have
  been fixed only for Claude Code. Pane types that skip saving scrollback opt
  in to a repaint by naming the key their program answers, and neither of these
  names one. Measured against real panes, they would not have helped if they
  did: OpenCode emits nothing at all for that key and repaints fully on a
  window-size change, and Claude Code is the exact reverse. Panes that name no
  key are now given a size change instead, which no program mistakes for input.
  This was never remote-only — it happened on every reattach, locally too.
- **Claude Code panes keep their conversation history when you reattach.**
  Closing the TUI, dropping an SSH link, or restarting the daemon used to leave
  the pane showing only its current screen — the conversation was still there
  in the program, but you could not scroll back to any of it. Quil now saves
  and replays these panes like it always has for terminals, so the history
  comes back and stays as you keep working.

  It was off because replaying a full-screen program was expected to produce
  garbage. Measured against a real pane, it does not: Claude Code writes to the
  normal screen and scrolls like any other program, which is why you can scroll
  it while attached.

  Two consequences worth knowing. Pane output is now written to disk under
  `~/.quil/buffers/`, as it already was for every terminal pane. And how much
  history returns is bounded by `[ghost_buffer] max_lines` — an AI pane spends
  that budget faster than a shell does, because every redraw costs bytes.

  You will be shown the change on next launch, since your own copy of
  `claude-code.toml` takes precedence over the shipped one. OpenCode panes are
  unchanged for now.
- **Full-screen tools start in a remote session.** `k9s` and `lazysql` opened
  and exited within a moment, leaving a pane that looked like it had crashed
  for no reason. A daemon reached over SSH has no terminal type set — the
  connection carries no terminal — and tools built on that library refuse to
  start without one, so the pane died before it could say why. Quil now gives
  its panes a terminal type when the daemon has none, and leaves an inherited
  one alone.
- **Browsing for a folder on Windows keeps working when mapped network drives
  are unreachable.** Listing the drives probes each one, and a mapping whose
  server has gone away cannot be interrupted — it holds a slot until the
  operating system gives up. With enough dead mappings, one press of "up" from
  `C:\` consumed every slot the daemon had, after which directory browsing, git
  repository discovery and kube-context discovery were all refused until a
  stalled probe finished. The sweep now gives up after the first couple of
  unresponsive drives, can never take the slots the directory listing itself
  needs, and says when the drive list it shows is incomplete — a drive missing
  because its server stopped answering otherwise looks exactly like one that
  was never mapped.
- **A pane's `$PWD` names the pane's own directory again.** Panes for tools
  that set no environment variables of their own — `k9s`, `lazygit`,
  `lazysql`, `ssh`, `stripe` — inherited the daemon's instead, which over a
  remote connection is the SSH login directory rather than the folder the pane
  was opened in. Anything trusting `$PWD` over asking the operating system
  resolved relative paths against the wrong folder, while the pane's own shell
  reported the right one.
- **The rest of the pane setup dialog now describes the remote machine.** The
  kube-context picker lists the contexts from the **daemon's** kubeconfig rather
  than your laptop's, so a `k9s` pane no longer launches with a `--context`
  naming a cluster the server may not have. `Ctrl+N` greys out plugins based on
  what is installed on the **server** — a tool present only there used to be
  hidden, and one present only locally was offered and then quietly opened as a
  plain shell. The recent-directories quick pick keeps a separate list per
  remote host and checks those paths against the daemon's disk; it previously
  rendered empty in remote mode, because a local existence check silently
  dropped every server path and an empty list looks exactly like a feature you
  have never used.
- **A remote daemon is no longer told your laptop's working directory** as the
  place to spawn new panes. It validated that path and fell back when it did not
  exist on the server, so this was safe — but only by coincidence, and a path
  that happens to exist on both machines is where the coincidence stops.
- **Choosing a folder in a remote session now shows the remote machine's
  files.** Every dialog that looks at a filesystem — the directory browser when
  you create a pane, and `Alt+G` for lazygit — was reading the disk of the
  computer in front of you, even when the session was attached to another
  machine over SSH. The result was a screen making two contradictory claims
  about one path: `Alt+G` would report no repository in a directory where the
  agent running in that very pane answered `git status` with the branch name.
  Typing `~/project` asked for your laptop's home directory on a Linux server,
  relative paths resolved against the wrong working directory, the browser
  offered your laptop's drives, and the suggested starting locations pointed at
  folders that existed only locally. All of them now ask the machine that
  actually holds the files.

### Security
- **A remote machine cannot use a folder name to garble the picker or disguise
  which folder you are choosing.** Now that directory names, resolved paths and
  error messages arrive from a host you may not control, they are stripped of
  terminal control sequences before being drawn, so a crafted name cannot
  scramble the dialog around it. Characters that override text direction are
  removed as well — those are printable, so they survive an ordinary
  control-character filter while making a name read as something other than
  what it is, which matters most on the list you pick a working directory from.
  Names in Cyrillic, Chinese, Japanese and emoji are untouched and display
  normally, and the real name is always what gets opened.

## [1.45.3] - 2026-07-31

### Fixed
- **Input history is readable again.** The list showed each prompt as up to
  three lines with a blank line between entries, and every one of those lines
  wrapped onto a second row — so a handful of prompts filled the screen and
  none of them could be skimmed. Each prompt is now a single line, truncated to
  fit, in a wider box. Prompts past the bottom of the list were also drawn
  off-screen with no way to reach them: the list now scrolls, follows the
  selection, and tells you where you are (`12-31/200`). `PgUp`, `PgDn`, `Home`,
  `End` and the mouse wheel all move through it.
- **Machine-written turns no longer appear in your input history.** When a
  background task finishes or a subagent reports back, the agent submits that
  report as if it were a prompt. Those were being recorded and shown alongside
  the things you actually typed, and on a busy session they outnumbered them. A
  prompt that merely mentions one of those markers is still kept — only a turn
  that is nothing but machine output is dropped.
- **Reading a past prompt in full now works.** Opening an entry showed one
  screen-width of each line and hid the rest, with no way to scroll sideways —
  so a pasted paragraph or stack trace was mostly unreachable, including
  unreachable to the selection you opened it to copy. It now wraps, opens at the
  top, and can be selected by dragging, copied with a right-click or `Enter`, or
  selected whole with `Ctrl+A`. The footer says so, which it previously did not.
- **Clicking or dragging inside a dialog no longer changes the layout behind
  it.** With any dialog open, a press-and-drag reached the panes underneath:
  split boundaries moved, panes resized, and the new layout was saved — none of
  it visible, because the dialog covers the screen. A click on the top row
  switched tabs the same way. Dialogs now absorb the mouse.
- **Text from a past prompt can no longer disturb your terminal.** Prompts are
  free text you may have pasted into, and neither the history list nor the
  full-text view passed it through the terminal emulation that protects normal
  pane output. Pasted content carrying terminal control codes could repaint the
  screen, or — with codes some terminals honour — quietly replace your
  clipboard when you re-read the prompt. Such codes are now removed before
  display, on both the sending and receiving side, which matters most when the
  daemon is on another machine.
- **The history list says when the daemon does not answer.** It used to show
  `Loading…` indefinitely. It now reports the failure after a few seconds and
  offers `r` to retry.

## [1.45.2] - 2026-07-31

### Fixed
- **Pasting into a pane no longer arrives one character at a time.** Text
  pasted with your terminal's own paste — Ctrl+V, right-click, middle-click,
  or over a remote desktop — reached the program inside the pane as if you had
  typed it. Interactive tools such as Claude Code redrew the screen for every
  character, so a long paste crawled, even though pasting into a plain shell
  in the same terminal was instant. It now arrives as a single paste, the way
  it does outside Quil. The same change stops a paste from running itself: a
  multi-line paste into a shell used to execute every line but the last the
  moment it landed, and now waits for you to press Enter. Programs that do not
  understand pastes — a command reading plain input, an older shell over SSH —
  still receive the text unchanged, so nothing that worked before starts
  seeing stray characters in its input.
- **Mouse-wheel scrolling inside tools like lazygit and opencode no longer
  fails intermittently.** When a program announced its mouse support at
  exactly the wrong moment — split across two reads of its output — Quil
  missed the announcement and kept scrolling its own history instead of
  passing the wheel through to the program. Uncommon, but once it happened the
  pane never recovered until it was restarted.

## [1.45.1] - 2026-07-30

### Added
- **Reconnecting now stops when retrying cannot possibly help.** If a remote
  session drops for a reason that will not fix itself — a key the server
  rejects, a host key that changed, an agent that went away — Quil no longer
  retries forever. It says so in the banner and waits; press `r` to try again
  once you have fixed the cause, or `ctrl+q` to quit. This matters beyond
  tidiness: every retry is a full SSH login, and a laptop left overnight
  retrying a rejected key can get its own address banned by the server's
  brute-force protection. Anything Quil cannot confidently identify as
  permanent still retries as before, because a session that would have
  recovered must never be stopped by mistake.

### Fixed
- **Quil no longer freezes when a remote session ends on Windows.** Closing a
  remote session, or reconnecting after a drop, could hang indefinitely instead
  of finishing — leaving the banner stuck on the first attempt with the
  keyboard unresponsive. The cleanup was waiting on a read that could never be
  interrupted while the step that would have ended it waited behind the same
  cleanup.
- **SSH's own explanations reach `quil.log` again after a reconnect.** Once a
  remote session had reconnected even once, warnings from `ssh` — a timeout, a
  server that stopped responding — went nowhere, so a connection that kept
  dropping became hardest to diagnose exactly when you needed the detail. They
  are recorded again, one entry per line.
- **A remote host can no longer grow Quil's memory or flood its log.** Output
  from the far side is now kept within a fixed size, and what reaches the log
  is limited per session, so a noisy or misbehaving host cannot consume memory
  without bound or push the rest of your log history out of the archive.

## [1.45.0] - 2026-07-30

### Fixed
- **Remote error messages no longer cascade down the screen on Windows.** When a
  `--remote` launch could not go ahead — the daemon on the far side running a
  different version, or the host not answering — the explanation was printed with
  each line starting further right than the one above it, trailing off the edge
  and wrapping mid-word. `ssh` reconfigures the console when it starts and puts
  it back when it exits, but Quil stops it rather than waiting, so it never got
  the chance; Quil now restores the console itself before printing. This only
  ever affected the messages that explain a failed connection, which is where it
  mattered most.

### Added
- **A dropped connection to a remote daemon is now a pause, not an ending.**
  Closing a laptop lid, losing wifi, or changing network used to end a
  `--remote` session outright: the panes kept running on the server, but getting
  back to them meant noticing, and re-running the command by hand. Quil now
  reconnects on its own. An amber bar names the host, counts the attempts, and
  shows what `ssh` said went wrong, with retries backing off from half a second
  to at most thirty. Keystrokes are dropped rather than queued while the link is
  down — a key typed at a dead connection would otherwise arrive in a live agent
  session minutes later, answering a question that had already moved on — and
  `ctrl+q` stays available throughout for a host that is not coming back.
  Reconnecting restores the panes' contents without duplicating them, and an
  agent that was mid-task while you were away is shown as still working rather
  than stuck.

## [1.44.1] - 2026-07-29

### Fixed
- **A failing ssh connection no longer writes over the screen.** In a
  `--remote` session, `ssh` keeps its error stream attached for the whole life
  of the connection and multiplexes the remote command's errors onto it, so a
  message such as `packet_write_wait: Broken pipe` could surface anywhere on
  the display — through a pane, across the tab bar — on a screen Quil believes
  it controls. From the moment the interface starts, those messages go to
  `quil.log` instead. They still print to the terminal during connection setup,
  which is where they are useful and where host-key confirmation and passphrase
  prompts have to stay readable.

## [1.44.0] - 2026-07-28

### Added
- **`quil remote setup <host>` installs Quil on the machine you want to work
  on.** Attaching to a remote daemon previously assumed Quil was already there,
  and getting it there by hand meant working out the server's platform,
  downloading the right archive, verifying it, copying it over, and putting it
  somewhere a non-interactive shell could find — then repeating the whole thing
  on every version bump. Now your own machine downloads the release for the
  *remote's* platform, verifies its checksum locally, and pushes it over the SSH
  connection you already have. The server needs no route to GitHub, which
  matters because cluster nodes frequently have none.

  You rarely need to run it yourself. `quil --remote <host>` against a machine
  with no Quil offers to install it and then **attaches** — the command asked to
  attach, so that is what it finishes doing. A version mismatch offers an
  upgrade the same way. Nothing happens without an explicit `y`, and the prompt
  names the host, the exact path, the version, and — for an upgrade — that the
  remote daemon will be stopped.

  This also fixes a trap that was easy to hit and hard to read: `ssh host quil
  --stdio` runs a *non-interactive* shell, and on Debian and Ubuntu `~/.bashrc`
  returns before reaching any `PATH` line, so a binary in `~/.local/bin` is
  invisible and the failure looks identical to an unreachable host. Setup
  records the absolute path per destination and uses it directly, so `PATH`
  never participates. Installs never use `sudo`.

  Supported remote platforms are Linux and macOS on amd64 and arm64; any local
  platform can provision any of them. Windows remotes are not supported — a
  running `.exe` cannot be overwritten, which makes the *upgrade* half
  impossible to do the way the Unix path does it.

- **`--from-dir` pushes locally built binaries** instead of a release, which is
  the only route available to a development build.

### Changed
- A version mismatch against a remote daemon now offers to upgrade it. The
  previous message suggested `ssh <host> 'quil daemon restart'`, which could not
  work — restarting the same binary reports the same version.

## [1.43.1] - 2026-07-28

### Fixed
- **Claude Code panes no longer come back blank after you reattach.** Closing the
  TUI and reopening it left them showing an empty rectangle, which looked exactly
  like the session had died. It hadn't — the process was still running and the
  conversation was still there, and pressing a key brought it straight back. AI
  panes deliberately don't save scrollback (replaying a captured full-screen app
  produces garbage rather than history), so there was simply nothing to draw
  until the program next wrote something, and a full-screen program has no reason
  to redraw unprompted. Quil now asks such a pane to repaint itself when you
  reattach, so what you see matches what is actually running.

  Pane types opt in to this by declaring the key that makes their program
  repaint (`redraw_key` in the plugin's `[persistence]` section) — it is sent to
  the program's input, so a pane that might be reading input as data must not
  declare one. Claude Code is set up for it; OpenCode is not yet, pending
  someone verifying which key it responds to.

## [1.43.0] - 2026-07-28

### Added
- **Attach a Quil TUI to a daemon on another machine** over SSH:
  `quil --remote gpu01`. The panes, tabs and AI sessions live on the remote
  host and keep running there when you close the laptop; the TUI is only a
  viewer. Nothing listens on a network port on the remote side — Quil runs
  `ssh -T gpu01 "quil --stdio"` and speaks its normal protocol over that one
  channel, so a host reachable only through a bastion, a Tailscale/WireGuard
  address, or a jump chain works with no extra setup.

  The destination is handed to `ssh` verbatim, so everything in your
  `~/.ssh/config` still applies: `Host` aliases, `ProxyJump`, `ControlMaster`
  multiplexing, per-host keys, hardware tokens and SSH certificates. Quil adds
  only timeouts and a set of hardening options it forces off — agent forwarding,
  X11 forwarding, port forwarding and local-command execution are all disabled
  for this connection regardless of what your config says, because the remote
  side never needs them. The remote daemon is started on demand if it is not
  already running.

  Both ends of the connection's life are bounded, so it fails fast and visibly
  rather than hanging. Connecting to a host that silently drops packets gives up
  after 15 seconds instead of inheriting the operating system's multi-minute
  connect timeout, and once attached, keepalives detect a dead link within about
  45 seconds. When the connection cannot be made at all, Quil says so and shows
  you the exact command to test by hand (`ssh <host> quil --stdio`) rather than
  reporting it as a version problem.

  Commands that manage a daemon's lifecycle refuse to run under `--remote`
  rather than silently acting on the wrong machine: `quil restart`,
  `quil daemon start|stop|restart`, the upgrade-restart prompt, and
  `quil --remote <host> mcp`. Manage the remote daemon over a normal SSH
  session, or drop `--remote` to manage the local one.

  This is the first phase and has limits worth knowing before you rely on it.
  A dropped SSH connection ends the session — there is no automatic reconnect
  yet, though the panes on the server survive and you can re-attach. Dialogs
  that browse the filesystem (the pane working-directory picker, git repository
  and kube-context discovery, the Claude session list) still read your **local**
  disk rather than the server's, so creating panes remotely works best with
  paths you type. Pane notes, clipboard paste and the log viewer are local by
  design. `quil status` also reports on the local daemon.

## [1.42.1] - 2026-07-26

### Fixed
- The pane setup dialog keeps showing which working directory the pane will
  start in while you configure the rest of it. The selection highlight was drawn
  only on the focused field, so tabbing from **Working directory** on to the
  toggles erased every trace of the directory you had picked — and the field was
  equally blank while still focused if the cursor sat on the trailing "Browse…"
  row, which never becomes the answer. The committed row now carries a `▸`
  marker whenever the cursor is not already on it, so the directory that will
  actually be used stays on screen for the whole dialog. The kube-context list
  had the same gap and gets the same marker.

## [1.42.0] - 2026-07-26

### Added
- Claude Code panes can start inside an **earlier conversation** instead of a
  fresh one. The pane setup dialog gains a **Session** field listing the
  sessions recorded for the working directory you selected — newest first, each
  row showing a relative age and the first prompt you typed in that session.
  Picking one spawns `claude --resume <id>` in place of the usual
  `--session-id <new-uuid>`; permission-mode and `--chrome` toggles still apply,
  and the pane rejoins the normal restore machinery from the first instant.

  The field stays collapsed to one line until focused and only fetches the
  listing when you go looking for it, so creating an ordinary fresh pane is
  unchanged and costs no extra I/O. Sessions already open in another live pane
  are listed but blocked from selection — two `claude` processes attached to one
  transcript overwrite each other's history. Changing the working directory
  clears the choice and rescans.

  Press **`i`** on a session for details: when it started, when it was last
  touched, how many prompts you typed, and the last prompt you left it on —
  which appears nowhere else and is usually what identifies a conversation.
  `↑`/`↓` re-read for the next session, so candidates can be compared without
  toggling the panel per row. The transcript is read on demand, never as part of
  the listing.

  Opt-in per pane type via the new `[command] sessions = "claude"` plugin key
  (`claude-code.toml` schema_version 8 → 9, so the plugin-migration dialog will
  offer the merge once on first launch).

### Fixed
- Rows in the pane setup dialog no longer wrap onto a second line. Every content
  budget in that dialog reserved the box's padding but not its border, which
  lipgloss counts inside `Style.Width` — so a row that filled its budget sat two
  cells past the wrap limit and reflow dropped its last word onto a line of its
  own. Most visible in the session picker, where long titles made every row wrap;
  the working-directory pick list and the footer hints had the same two-cell
  overrun. All four now derive from one `setupTextWidth()`, pinned to lipgloss's
  actual behaviour by a test that fails in both directions.
- A session already open in another pane keeps its `[open in …]` marker when its
  title is long. The marker was appended and the whole row then truncated, so it
  was cut off exactly the rows that needed it — leaving a blocked row looking
  selectable until `Enter` refused it.

## [1.41.1] - 2026-07-24

### Security
- The staged-update integrity gate now verifies the set of binaries it is about
  to install rather than the set the manifest chooses to declare. `VerifyStaged`
  ranged over `manifest.files`, so a manifest with no `files` key hashed nothing
  and passed, and one naming only `quil` let `quild` be installed and executed
  unverified. It now rejects a manifest that does not cover every binary the
  swap installs. Reaching this required write access to
  `$QUIL_HOME/update/staged/`, so it was not a privilege escalation on a default
  single-user install — but it was the control that exists to be the tamper gate.

### Fixed
- Self-update no longer fails permanently with `back up …: Access is denied` on
  Windows. A backup left by an earlier update (`quild.exe.old`) stays locked for
  as long as some process still runs it as its image — an orphaned daemon that
  survived that update, or an antivirus handle. Windows refuses to delete such a
  file, which broke both the stale-backup cleanup and the rename that follows, so
  a single leftover wedged every subsequent update. The swap now falls back to
  `.old.1`, `.old.2`, … when the canonical backup slot cannot be cleared, and
  startup cleanup sweeps those fallbacks once they are free again.
- An upgrade no longer leaves the previous daemon running. The version-gate
  restart sent a shutdown, waited 5 s, and — if the daemon had not exited —
  deleted its socket and PID file and spawned a replacement anyway. The old
  daemon kept running with every pane PTY attached and no bookkeeping left to
  find it by, while the new one restored the same workspace into a duplicate
  set of panes (including a second `claude --resume` on an already-resumed
  session). The restart now uses the same escalating stop as
  `quil daemon stop` (IPC → SIGTERM → SIGKILL, PID-reuse guarded) and aborts
  the upgrade instead of spawning a second daemon when the stop cannot be
  confirmed.

## [1.41.0] - 2026-07-24

### Added
- Recent working directories: the pane setup dialog (`Ctrl+N` for plugins with
  `prompts_cwd`, e.g. Claude Code) now offers the last 5 used folders as a
  one-keystroke quick pick, persisted to `~/.quil/recent-cwds.json` and surviving
  daemon/TUI restart. Stale (deleted) folders are skipped; **Browse…** still opens
  the full directory browser. Git-repo discovery keeps priority when it finds repos.

### Changed
- Shortened the Claude Code permission/Chrome toggle labels so they no longer wrap
  mid-word on narrow terminals.

## [1.40.0] - 2026-07-24

### Added
- Command palette content search: as you type, the palette also searches every
  pane's scrollback and lists matching panes in a "Found in panes" section below
  the filtered commands (match count + preview) — one query narrows commands and
  finds content at once, no separate mode. Enter on a match jumps to that pane.
  Background and muted panes are searched too; a search that never comes back
  shows a timed-out row instead of an endless "Searching…".

## [1.39.0] - 2026-07-23

### Added
- Command palette (`Alt+Shift+P`) — a modal, keyboard-first fuzzy-find launcher for every action plus jump-to-tab and jump-to-pane across the workspace. Entries are grouped under section headers (Pane / Go to pane / Tabs / System) with actions first; headers disappear when you type. Type to filter, `Enter` to run, `Esc` to close; each row shows its keybinding. Dispatches into the same handlers the keybindings use. Configurable via `command_palette` (`Ctrl+Shift+P` is opt-in — many terminals intercept it).

## [1.38.0] - 2026-07-19

### Added
- **Pane context menu** — right-click a pane (or press `Alt+A`) for a popup of
  everything you can do to it: split, focus, rename, notes, input history,
  lazygit, mute, always-resume, restart, close. The menu targets the pane under
  the cursor and highlights it, so it works on any pane without focusing it
  first. Rows that don't apply are greyed with the reason (input history on a
  pane type that doesn't record it, lazygit when the binary isn't installed).
- **Mark attention** — a context-menu action that pins the green attention
  border to a pane and keeps it there until you clear it. Unlike the automatic
  unseen mark, focusing the pane does not remove it, so you can flag a pane to
  come back to.

### Changed
- Right-clicking a pane that has an active text selection still copies the
  selection, as before — the menu only opens when there is nothing selected.

## [1.37.0] - 2026-07-18

### Added
- **Automatic updates** — Quil now notices new releases, downloads them in the
  background, and can install one without you leaving the app. The status bar
  shows `↑ v1.37.0 [ready]` when an update is staged, and F1 → "Check for
  updates" runs a real check on demand. Applying swaps both binaries and
  restarts Quil; your panes come back from the workspace snapshot as they do
  after any restart.
- Two settings under `[update]` in `~/.quil/config.toml`, both editable in the
  F1 → Settings dialog: `check` (look for new releases; on by default) and
  `auto` (download them in the background so applying is instant).

### Fixed
- Downloads are verified against the release checksums before anything is
  swapped, and the previous binaries are kept as a backup that is restored if
  the swap fails halfway.

### Changed
- Dev and debug builds have the whole update pipeline compiled out. Applying a
  release build over `quil-dev` would strip its dev-mode wiring and silently
  point the next launch at your production `~/.quil` data.

## [1.36.2] - 2026-07-17

### Fixed
- **Orphaned MCP bridges on Windows** — `quil mcp` processes no longer pile up
  after the AI client that spawned them exits. Stdin EOF is not a reliable
  end-of-life signal on Windows: clients spawn stdio servers concurrently, and a
  sibling process inherits the pipe handle, so the bridge kept waiting on a
  stdin that would never close. Observed as 20 abandoned bridges accumulating
  over a week, each holding a live connection to the daemon. The bridge now
  watches the process that started it and exits when that process does, with a
  guard against a recycled process id being mistaken for a live parent. Covers
  pane kill, pane restart, session restart, and client crash.

## [1.36.1] - 2026-07-16

### Fixed
- **Work spinner going dark while subagents are still running** — Claude Code
  runs subagents detached, so the main turn reports "stop" while the subagents
  are still grinding. The spinner treated that as end-of-work and the pane went
  quiet with a green "done" mark exactly during the heaviest phase. Outstanding
  subagents are now counted: the spinner keeps running until the last one
  finishes, and only then does the pane get its unseen mark. A session ending
  clears the count, so a lost subagent event can't leave the spinner stuck on.

## [1.36.0] - 2026-07-15

### Added
- **Drag a split border to resize panes** — click and drag any border between
  two panes to move the split, like every other tiling terminal. The panes on
  each side of the dragged line highlight while you drag. The size is clamped so
  no pane can be squeezed below a usable minimum, including through nested
  splits. The PTY resize and the layout save happen once when you release the
  mouse, not continuously during the drag — mid-drag resizes are what garble a
  running program's output. Disabled in focus mode, notes mode, and on
  single-pane tabs; the scrollbar keeps priority over its own column.

## [1.35.0] - 2026-07-09

### Added
- **`quil status`** — a scriptable health check for the daemon (alias
  `quil daemon status`). Reports whether the daemon is running, its pid,
  version, environment (production or dev), roughly how long it has been up,
  and a per-tab/pane breakdown with each pane's state and memory use. `--json`
  emits the same data for scripts. Exit codes distinguish a healthy daemon from
  one that isn't running from one that is wedged, so it works in a monitoring
  loop.

## [1.34.2] - 2026-07-08

### Fixed
- **Input history filled with machine-generated turns** — the `Alt+Shift+I`
  history for Claude Code panes listed `<task-notification>` blocks (task ids,
  tool-use ids, usage stats) alongside your actual prompts. Claude Code fires
  the same prompt-submitted hook for synthetic turns, which is how the harness
  resumes the loop when a background task finishes. Those turns are now skipped
  when recording, filtered when the list is displayed (so history recorded
  before this release is clean too), and dropped when the file is trimmed. A
  prompt of yours that merely quotes the tag is preserved.

## [1.34.1] - 2026-07-08

### Fixed

- **"daemon did not come up" when starting with a large saved workspace** —
  launching Quil or running `quil restart` no longer falsely reports the daemon
  as failed while it is in fact still starting. On restart the daemon respawns
  the active tab's panes plus any panes marked "always resume" before it begins
  accepting connections, so a session with several Claude/AI panes could take
  longer than the old 2-second wait allowed and the client gave up prematurely.
  The readiness wait is now 30 seconds and aborts early if the daemon actually
  crashes — a slow-but-healthy start succeeds, and a genuine failure is still
  reported promptly instead of after a long hang.
- **Orphaned background daemons** — starting the daemon while one is already
  running (for example after the false timeout above) no longer leaves earlier
  daemon processes running invisibly. A redundant daemon now detects the healthy
  one and exits cleanly instead of hijacking its socket and stranding it; the
  stranded daemons had leaked memory and held the log file open, which broke log
  rotation.

## [1.34.0] - 2026-07-06

### Added
- **AI panes render at their real size when they're wide enough** — a Claude
  Code or OpenCode pane used to always render on a window-wide canvas and show a
  cropped view of it, which protected the program from constant resizes but cost
  you selection and made the crop visible. Once a pane is at least 80 columns
  wide (`[display] min_native_cols`) it now renders natively at its own size,
  which brings back mouse and keyboard text selection. Below the threshold it
  falls back to the canvas as before — and even then, mouse selection now works
  on the cropped view.

## [1.33.1] - 2026-07-05

### Fixed
- **Garbled claude-code panes after working in a multi-pane tab** — mixed-width
  line wraps and duplicated chunks of transcript accumulated over a session.
  Claude Code hard-wraps its transcript at the width it streamed at and
  re-renders its tail on every resize, and Quil resized panes far more often
  than a real terminal does: toggling the notification sidebar, entering focus
  mode, splitting, and every state broadcast each triggered one. The
  notification sidebar is now drawn as an overlay that reserves no layout width,
  so `Alt+N` no longer resizes anything, and same-size resizes are dropped
  before they reach the PTY.
- **Session resume after a pane restart** — restarting a Claude Code pane could
  reattach to the session chosen when the pane was created rather than the
  conversation it is actually in now.

## [1.33.0] - 2026-07-03

### Added
- **Model and context usage in the status bar** — AI panes show the model and
  the context-window token count from the last completed turn, e.g.
  `opus-4.8 · 612k ctx`. The number is the real total (input plus cached
  tokens), deliberately shown as tokens and not a percentage, because the window
  size isn't recorded in either tool's data and a Claude session may run at 200k
  or 1M. Right after a compaction the pane shows `· compacting` until the next
  turn reports the reduced size, rather than displaying a stale pre-compaction
  count.

## [1.32.2] - 2026-07-03

### Fixed
- **Work spinner broken by muting a pane** — muting a pane instantly killed its
  work-in-progress spinner and unmuting never brought it back, even while the
  pane was still working. Mute is meant to silence the notification sidebar, not
  to blind the spinner; work-state events now keep flowing to the TUI for a
  muted pane while its notification cards stay suppressed.

## [1.32.1] - 2026-07-03

### Fixed

- **macOS render corruption in claude-code panes** — typing over the input
  placeholder no longer leaks stray text (`AAA` shown as `AAAude Code`), and the
  header logo no longer doubles (`Claude CodClaude Code`). The bundled terminal
  emulator ended the child app's window-title escape sequence early when the
  title contained certain Unicode characters (claude-code's `✳` marker), spilling
  the title text into the visible pane. Quil now filters window-title sequences
  before the emulator — it renders its own tab titles, so nothing is lost.

- **Word-jump keys on macOS Terminal.app** — Option-based word navigation now
  works inside panes. With "Use Option as Meta key" enabled, `Option+B` /
  `Option+F` (and other `Alt`+key combinations) are forwarded to the pane as Meta
  keys, so claude-code and shell readline word navigation work with no extra
  configuration. `Ctrl+Arrow` word-jump on Windows/Linux is unchanged.

## [1.32.0] - 2026-07-02

### Added

- **Mouse-wheel scrolling in AI/TUI panes** — scrolling the wheel over a pane
  running an app that handles its own mouse input (opencode, claude-code, vim,
  htop, lazygit, …) now scrolls that app's viewport instead of doing nothing.
  These apps run on the alternate screen, which never fills Quil's local
  scrollback, so the wheel is forwarded straight to the program. The daemon
  detects when a pane's program enables mouse tracking — reliable even when you
  reattach to an already-running session — and the client forwards each wheel
  notch as the matching mouse sequence. Plain terminal/shell panes keep
  scrolling Quil's own scrollback as before.

## [1.31.2] - 2026-06-18

### Fixed
- Releases whose changelog section contained only whitespace were published with
  a blank description instead of falling back to generated notes — the check
  treated a lone newline as real content. This shipped v1.31.1 with an empty
  release page.

## [1.31.1] - 2026-06-18

### Fixed
- Every release now gets a description: when a release ships without a changelog
  entry, the release page falls back to notes generated from its commits and
  pull requests instead of publishing empty (as v1.29.0 and v1.31.0 did).
- Fixed a race that made quil.cc deployments fail with "Deployment failed, try
  again later" when a release and a site change landed together. The release
  workflow no longer deploys the site itself; the version bump it pushes
  triggers the site workflow, which is now the only thing that deploys.

## [1.31.0] - 2026-06-18

### Fixed

- **Windows 10 input rendering** — typing in claude-code (and other TUIs) no
  longer shows an extra space after the first character on Windows 10
  (`Hello` → `H ello`). The Windows 10 inbox console host re-serializes
  incremental screen updates incorrectly; Quil now bundles Microsoft's
  OpenConsole (MIT) and hosts panes through it on Windows 10 only — Windows 11
  keeps its built-in host, which is unaffected. Fail-safe: falls back to the
  inbox host if the bundle is unavailable. See `docs/architecture.md` (ADR-25).

## [1.30.0] - 2026-06-17

### Added

- **Per-pane input history (AI panes)** — Quil records every prompt you submit
  to a Claude Code pane so you can find what you asked without scrolling back
  through the agent's output. `Alt+Shift+I` opens a list of your past inputs
  (3-line previews, newest first); `Enter` opens the full text in a read-only
  viewer you can scroll and copy from (`Esc` returns to the list, `Esc` again to
  the pane). History persists across daemon restarts at
  `~/.quil/history/<pane>.jsonl` (64 KiB per entry, ring-trimmed to the last
  200) and is removed when the pane is destroyed. Opt-in per plugin via
  `[command] record_history = true` (enabled for `claude-code`); pane types
  without it show an empty state. OpenCode support is planned.

### Fixed

- **Plugin schema migration now reloads the daemon** — accepting the startup
  migration dialog rewrote the plugin file and reloaded only the TUI's registry,
  so the daemon kept the stale config until restarted. It now sends a reload, so
  newly added plugin fields (such as `record_history`) take effect immediately.

## [1.29.0] - 2026-06-17

### Changed
- **Stop daemon moved to the F1 root menu** — it now sits alongside Settings,
  Shortcuts, Plugins, Memory, and the log viewers instead of being buried in the
  Settings list. Confirming still requires `y` rather than Enter, so a reflexive
  Enter can't take down every pane, and Esc returns to the menu with the cursor
  where you left it.
- **Wider lazygit repository picker** — the picker shown when a pane's directory
  contains several git repositories grew from 60 to 90 columns, so long paths
  keep the tail that tells them apart instead of being truncated to a common
  prefix.

## [1.28.0] - 2026-06-15

### Added

- **lazysql plugin** — database TUI (MySQL, PostgreSQL, SQLite, MSSQL) as a
  built-in pane type. Binary-gated (greyed in `Ctrl+N` with a homepage link when
  `lazysql` is not on `PATH`), opens lazysql's own connection manager, with an
  optional read-only toggle. Connection selection and credentials stay inside
  lazysql. Cross-platform; re-runs on daemon restart.

## [1.27.0] - 2026-06-15

### Added

- **Uninstalled tool plugins stay discoverable** — the `Ctrl+N` pane-creation
  list now shows plugins whose binary isn't on `PATH` greyed out (sorted below
  the available ones) with a link to the tool's homepage, instead of hiding
  them entirely. Selecting a greyed entry is blocked with an inline hint. New
  optional `homepage` field in the plugin `[plugin]` schema; set for the
  external-tool plugins (k9s, lazygit, Stripe CLI).

- **k9s plugin** — Kubernetes cluster TUI as a built-in pane type. Binary-gated
  (greyed out when `k9s` is not on `PATH`), opens as a normal pane with optional
  read-only (`--readonly`) and start-on-Pods toggles. k9s is cluster-scoped, so
  there is no working-directory prompt — it connects to whatever your kubeconfig
  points at (`KUBECONFIG` / `~/.kube/config`). Cross-platform (Windows, macOS,
  Linux); re-runs and reconnects on daemon restart.
- **k9s context picker** — the k9s pane setup dialog lists kube contexts from
  `KUBECONFIG` / `~/.kube/config` and pins the pane to the chosen context via
  `--context`. "Default context" uses the kubeconfig current-context. Backed by
  the new `discover = "kube"` plugin field (documented in the plugin reference).

## [1.26.0] - 2026-06-15

### Added
- **Restore progress for panes that are coming back** — a restored or
  slow-starting pane now shows a centered checklist instead of an empty box that
  looks frozen: session loaded → history restored (with the line count) or
  restored via the tool's own resume, or none saved → resuming the tool →
  waiting for first output, with a spinner on the current step. It stays up
  through a multi-second boot (claude-code clears the screen before it paints)
  and disappears the moment real output arrives. Panes on other tabs that are
  restored lazily show it when they actually start, not at the original restart.

## [1.25.1] - 2026-06-15

### Fixed
- Website: social preview images on pages with an absolute image URL were
  double-prefixed and failed to load.

## [1.25.0] - 2026-06-15

### Changed
- Website: real product screenshots on quil.cc and in the README, served from a
  CDN, replacing the placeholder imagery on the landing page, feature catalog,
  and blog.

## [1.24.0] - 2026-06-15

### Added
- Website: a blog at [quil.cc/blog](https://quil.cc/blog), starting with a post
  on how Claude Code session resume works in Quil.

## [1.23.1] - 2026-06-14

### Fixed
- Website: corrected the advertised MCP tool count and the AI-resume claim on
  quil.cc, which had drifted behind the shipped feature set, plus an SEO title
  fix.

## [1.23.0] - 2026-06-13

### Added

- **Claude Code Chrome support toggle** — the Claude Code pane setup dialog now offers a standalone "Chrome support" checkbox that appends `--chrome` to the launch command, connecting the CLI to the Claude in Chrome browser extension. It is independent of the permission-mode radio buttons, so it can be combined with either mode (or neither). Requires the Claude in Chrome extension (v1.0.36+) and `claude` ≥ 2.0.73. The pane setup dialog now also auto-sizes its width to fit the longest toggle label instead of a fixed width, so long option descriptions render on one line.

## [1.22.0] - 2026-06-13

### Added

- **Lazygit integration** — a built-in `lazygit` plugin plus a per-tab overlay for dropping into a git UI for whatever repository a pane is working in. Two entry points: **Ctrl+N → Tools → Lazygit** opens lazygit as an ordinary pane — when the binary is installed, the working-directory step lists the git repositories discovered near the active pane's directory (the enclosing repository plus any one level down, capped at ten) with a "Browse…" fallback to the plain directory picker; and **Alt+G** toggles a full-tab lazygit overlay for the repository resolved from the active pane's current directory. Press Alt+G again to hide it — the process keeps running, so re-opening is instant with lazygit's UI state intact — and when several repositories are found near the pane, a picker lets you choose. Overlays are deliberately ephemeral: one per tab, excluded from workspace snapshots, recreated with a single keypress, and destroyed automatically when you quit lazygit (`q`). Repository discovery (`internal/gitdiscover`) is a pure filesystem walk that canonicalises paths and refuses UNC/device paths, so an untrusted working directory can never steer it onto a network share. The plugin and the overlay are offered only when the `lazygit` binary is found on `PATH`. New keybinding `toggle_lazygit` (default `alt+g`); new plugin field `discover = "git"` documented in the plugin reference.

## [1.21.1] - 2026-06-12

### Fixed
- **Daemon freeze whenever Claude Code rang the terminal bell** — the whole
  daemon (every pane, every tab) could stop responding, most often right when
  Claude asked for your attention or finished compacting. The bell handler held
  a per-pane lock while raising the notification event, and raising the event
  needed the same lock to check whether the pane was muted — so the first
  un-cooled-down bell deadlocked the pane's output goroutine, and the snapshot
  loop, idle checker, memory report, and pane input all piled up behind it. This
  was the root cause of the remaining production freezes, caught by the
  goroutine dump added in v1.20.3.

## [1.21.0] - 2026-06-12

### Added
- **Restart a pane from the keyboard** — `Alt+R` restarts the active pane behind
  a confirmation dialog, using the same kill-and-respawn path as the MCP
  `restart_pane` tool, so session resume and pane settings are preserved.
  Configurable via the `restart_pane` keybinding.

## [1.20.3] - 2026-06-12

### Fixed
- **Daemon freeze caused by a pane that stops reading input** — a child process
  that stalls (observed with Claude Code after context compaction) filled its
  input buffer, and the daemon blocked forever trying to write to it. Because
  that write happened on the connection's shared dispatch path, *every* pane's
  keyboard input died with it. Input is now handed to a per-pane writer with a
  bounded queue; a stalled pane drops its own keystrokes and posts a "Pane not
  accepting input" notification instead of freezing the app.
- **Daemon freeze when closing a pane or tab whose child won't exit** — closing
  waited for the child to be reaped while holding the lock that every other
  operation needs, so the snapshot loop, attach, tab switch, and memory
  reporting all died behind it. Panes are now detached under the lock and closed
  outside it.
- Added a watchdog that dumps all goroutine stacks to the log when no workspace
  snapshot completes for two minutes, so a future freeze names its own culprit.

## [1.20.2] - 2026-06-12

### Fixed
- Made an internal connection-teardown test deterministic; it intermittently
  failed CI and blocked releases.

## [1.20.1] - 2026-06-12

### Fixed
- **Tab color cycle stuck on the last color** — cycling a tab's color with
  `Alt+C` wrapped back to "no color" in the client, but the daemon read the
  empty color as "leave it unchanged" and the next state broadcast snapped the
  tab back to orange. Clearing a color is now sent explicitly, so the cycle
  completes.

## [1.20.0] - 2026-06-11

### Added
- **`quil restart`** — stops the daemon and starts a fresh one with the TUI in a
  single command, plus `quil daemon stop` / `quil daemon restart` for the daemon
  alone. Stopping escalates: a graceful shutdown request, then a terminate
  signal, then a kill, each with its own bounded wait, so it also works against
  a daemon that has stopped responding. Stale socket and pid files are cleaned
  up afterwards, and the command prints which environment (production or dev) it
  acted on before doing anything.

### Fixed
- `quil daemon stop` no longer intermittently does nothing. The shutdown request
  was queued on a connection that the command then closed, discarding it.

## [1.19.1] - 2026-06-11

### Fixed
- **macOS: `zsh: killed quil` after upgrading** — the installer overwrote
  binaries in place, reusing the file's identity on disk. macOS caches code
  signatures per file, so the kernel killed the newly installed binary at launch
  as an invalid signature, and reinstalling never fixed it. The installer now
  writes each binary to a temporary file and moves it into place, which both
  prevents the problem and repairs machines already stuck in that state — just
  re-run the normal install command. Added a troubleshooting entry with the
  diagnosis and the manual fallback for anyone on an old copy of the installer.

## [1.19.0] - 2026-06-11

### Changed

- **"Turn finished" green flash is now a persistent unseen indicator** — when an AI pane finished a turn or parked for input (permission prompt, options question), the tab label flashed green for 5 seconds and reverted — easy to miss when away from the keyboard, and with several agent panes split in one tab it couldn't say *which* pane needed attention. The cue is now persistent and per-pane: the finished/parked pane gets a green border, and a background tab containing one derives a green label; both stay green until you focus that pane (click it, Alt+Arrow onto it, or switch to its single-pane tab) — focusing is the acknowledgement, there is no timer. Completion in the pane you're currently focused on shows no cue (seen by definition), and a fresh turn replaces the green with the work spinner. Border precedence stays below active/ghost/MCP-highlight; the mark is not persisted across TUI restarts (same as the rest of work state).

## [1.18.6] - 2026-06-10

### Changed

- **TUI render path stops rebuilding unchanged frames** — every Bubble Tea update used to re-render the full VT grid, borders, and labels of every visible pane (hundreds of times per second under streaming output; the 100 ms spinner tick alone forced full-tab rebuilds). Pane frames are now cached behind a complete render fingerprint (content/selection generations + every visual input), tab pane-lists are memoized until the layout tree mutates (the tab bar walked every tab's tree twice per render), and the per-update perf instrumentation no longer does reflection. `Alt+Shift+L` (redraw) also clears the caches as the escape hatch.
- **Daemon hot paths rebuilt for allocation-free steady state** — the per-pane output ring buffer is now a true circular buffer (the old implementation reallocated and copied the full 256 KB backing array on every write once full — the daemon's dominant GC pressure under chatty AI panes; steady-state writes are now zero-allocation). Snapshots skip ghost buffers unchanged since the last write via a ring-buffer generation counter (previously every 30 s snapshot rewrote all buffer files — ~20 GB/day of identical bytes). Notification excerpts and idle analysis read only the trailing 4 KiB window (`RingBuffer.Tail`) instead of copying the whole ring per event. IPC framing builds each wire frame in a single allocation (replacing a marshal → buffer → clone chain) and reads through a buffered reader (halves read syscalls; removes a per-message allocation).

### Fixed

- **TUI: closed panes no longer leak VT emulators** — every pane removed from the layout (Ctrl+W, tab close, pane replace, daemon-side destroy) left its VT emulator's drain goroutine parked forever, pinning a 10,000-line scrollback grid per closed pane. `applyWorkspaceState` now disposes the emulators of panes that did not survive reconciliation.
- **TUI: notification-sidebar refresh chains no longer stack** — every pane event with the sidebar open started an additional self-perpetuating 10 s re-render chain (50 events → 50 immortal chains → constant background CPU). Scheduling is now guarded by a running flag, mirroring the work-spinner pattern; the notes auto-save chain got the same guard.
- **Daemon: PTY output coalescer is bounded** — the 2 ms coalescing timer is a debounce, so a PTY streaming without gaps (`cat bigfile`) grew the accumulator and the resulting broadcast frame without bound. Flushes are now capped at 64 KiB.
- **Daemon: tab destroy and pane replace clean hook artifacts** — only direct pane destroy released the hook spool file, spool/ingester map entries, and session-id files; panes destroyed via tab close or replace leaked them, and the spool watcher kept re-polling the dead file every 200 ms. All destroy paths now share one `cleanupPaneArtifacts` helper (which also unlinks the persisted session-id files — previously never removed by any path).
- **Windows: child-process handles are released** — `WaitExit` now closes the process HANDLE after reaping the exit code; previously one kernel handle leaked per destroyed/restarted pane for the daemon's lifetime.
- **Second daemon no longer bricks the running one** — `quild` now probes the socket before starting and refuses when a live daemon is already serving the same `QUIL_HOME`, instead of unlinking the live socket and overwriting `quild.pid` (which left the original daemon unreachable for every new client). A stale socket with nothing listening behind it still starts normally.
- **Dev builds no longer silently target production state** — pane environments now carry `QUIL_HOOK_HOME` (consumers fall back to `QUIL_HOME` for one release) so children of claude/opencode panes stop inheriting a production-pointing `QUIL_HOME`; additionally, dev builds ignore an inherited `QUIL_HOME` that equals the production default `~/.quil` (with a stderr warning). The `quild claude-hook` fast path dispatches before the dev-mode gates so hook writes always honor the spawning daemon's data dir.

## [1.18.5] - 2026-06-10

### Fixed

- **Windows: Ctrl+V stopped pasting screenshots** — pressing Ctrl+V on a clipboard holding an image (but no text) did nothing, while F8 still worked. Windows Terminal performs its own paste on Ctrl+V and delivers it to Quil as a bracketed `tea.PasteMsg`; for an image-only clipboard that message's content is empty, and the empty-content branch called `sendClipboardToPane("")` and silently no-oped. The image→PNG proxy (save the image under `~/.quil/paste/`, type the file path into the pane) lived only in the F8/Ctrl+Alt+V keypress path (`pasteClipboard`), so F8 worked while Ctrl+V did not — a regression introduced when bracketed-paste handling was added. An empty bracketed paste now routes to that same image-capable path, restoring Ctrl+V screenshot paste. The paste flow's clipboard readers were made injectable so the routing is covered by a unit test.

## [1.18.4] - 2026-06-10

### Fixed

- **Work-in-progress spinner stayed spinning while an AI pane waited for your input** — when Claude (or opencode) parked on a permission prompt or an options/question prompt, the tab + pane spinner kept animating as if work were ongoing, and the "turn finished" green tab flash never fired. The TUI derives work state purely from the hook-event stream, and the park edges (`hook.claude.Notification`, `hook.claude.PermissionRequest`, `hook.opencode.permission.ask`) were unmapped, so `working` was never cleared. Those edges now resolve to a stop transition: the spinner stops and the (non-active) tab flashes green for 5 s to pull attention. The earlier v1 decision to treat permission-waiting as "still working" (no separate blocked state) is reversed.
- **Spinner did not resume after you answered an AI prompt** — once the spinner was parked, selecting an answer left the pane looking idle even though the agent had resumed. There is no "resumed after approval" hook, so the resume edge had to be found empirically: diagnostic hook logging showed `PreToolUse` fires *before* the prompt (useless as a resume), while **`PostToolUse` fires the instant the prompt tool returns the user's answer**. The Claude hook now registers `PostToolUse` with a tool-name matcher (`AskUserQuestion|ExitPlanMode`) so the hook is invoked only for interactive-prompt tools — no per-tool-call overhead, which is why the full `PostToolUse` stream was excluded from the default tier. That edge re-arms the spinner and clears the pending green flash; it is a work-state-only signal and never appears as a notification card. Known limitation: a permission-gated *command* (e.g. an approved `Bash`) resumes only when the command completes (its `PostToolUse`), not at the moment of approval — the options-prompt case resumes instantly.

## [1.18.3] - 2026-06-10

### Fixed

- **Windows: ConPTY ghost-window mouse block was not actually fixed in v1.18.2** — the v1.18.2 guard gated on `IsWindowVisible`, assuming the ConPTY ghost is invisible. It is not: the `PseudoConsoleWindow` has `WS_VISIBLE` set while sitting at a zero rect, so `IsWindowVisible` returns true and the guard never fired in a real Windows Terminal / VS Code session — `ShowWindow(SW_MAXIMIZE)` still spawned the invisible full-screen window that swallows mouse clicks across the whole desktop. The guard now discriminates by **window class** via `GetClassNameW`: only a genuine conhost window (`"ConsoleWindowClass"`) may be moved, maximized, or have its geometry persisted; the ConPTY ghost (`"PseudoConsoleWindow"`) is skipped on both restore and save. Verified against a real ConPTY (`realConsoleWindow()` returns 0 for a live `PseudoConsoleWindow`); the pure `isRealConsoleClass` discriminator is unit-tested.

## [1.18.2] - 2026-06-10

### Fixed

- **Windows: launching from Windows Terminal froze mouse input to every other app** — restoring a persisted `maximized: true` window state called `ShowWindow(SW_MAXIMIZE)` on the handle returned by `GetConsoleWindow()`. Under a ConPTY host (Windows Terminal, VS Code) there is no real console window — that call returns a hidden `PseudoConsoleWindow`, and maximizing it spawns an invisible full-screen window that swallows mouse clicks for every window beneath it in the Z-order (only the focused window and Alt+Tab kept working; everything else looked frozen). `IsZoomed` on the ghost then stayed true, so exit re-saved `maximized: true` and the bug reproduced on every launch. Window restore and save now gate on `IsWindowVisible` via a new `realConsoleWindow()` helper — a real conhost window is always visible by the time the TUI runs, the ConPTY ghost never is — and save carries the previous session's pixel/maximized values forward so a ConPTY session can no longer poison real conhost geometry.

## [1.18.1] - 2026-06-10

### Fixed

- **Attach kick-loop: daemon force-closed a healthy TUI mid ghost replay** — ghost replay and notification-event replay during attach route through the per-conn 64-slot critical send queue. Two full 256 KB ghost buffers chunk into exactly 64 must-deliver frames, so a freshly attached client still busy applying workspace state overflowed the queue and tripped the slow-client defense (`ipc: dropping slow client`), disconnecting the TUI on **every** attach — production was locked out permanently. New `Conn.SendBlocking` applies backpressure for unicast bulk transfers instead of the overflow close: it waits for the queue to drain below half capacity (reserving headroom so concurrent broadcasts never hit a replay-saturated queue), aborts on conn close or daemon shutdown, and leaves genuinely wedged peers to the existing 30 s write deadline. `sendGhostChunked` and the attach event-replay loop (up to 200 events — same latent overflow) now use it.

## [1.18.0] - 2026-06-09

### Added
- **Work-in-progress indicators for AI panes** — a spinner appears on the pane
  border and its tab label while a Claude Code or OpenCode pane is working, and
  clears when the turn ends. It is driven by the agent's own lifecycle hooks
  rather than by guessing from screen output, so it is accurate through long
  turns with no output. A pane that is waiting on you — a permission prompt or
  an option prompt — stops spinning and flags for attention, then resumes when
  you answer.
- **Native Claude Code hook** — Quil's hook is now a built-in subcommand instead
  of a generated shell/PowerShell script. It starts in tens of milliseconds
  rather than seconds, and it removes an entire class of encoding bugs that
  broke the old scripts on non-UTF-8 Windows code pages. Quil still registers
  its hooks per pane at launch and never modifies your `~/.claude/settings.json`.

## [1.17.0] - 2026-06-09

### Added
- **Always resume** (`Alt+Shift+E`) — mark a pane to be started immediately on
  daemon restart instead of when you first visit it. Tabs containing such a pane
  show a `●` marker.

### Changed
- **Restarting with a large workspace no longer disconnects the TUI.** With many
  tabs and AI panes, the daemon restored everything at once and force-closed the
  busy client mid-restore (observed: 13 tabs / 12 Claude panes, TUI closing
  itself about a minute after launch). Two changes fix it: on restart the daemon
  now starts only the active tab's panes plus any marked "always resume", and
  defers the rest until you switch to them; and a client that falls behind now
  sheds live output frames instead of being disconnected — only a genuinely
  wedged client is dropped.

### Fixed
- **Log files grow without bound** — `quild.log` and `quil.log` now rotate at
  `[logging] max_size_mb` (default 5 MB), keeping `max_files` timestamped
  archives (default 10) and pruning the rest. These settings existed but were
  never honored; production logs had reached 74 MB and 182 MB.

## [1.16.1] - 2026-06-08

### Fixed
Windows rendering and pane-sizing fixes, all seen in the legacy console host:
- **No visible caret** in interactive panes (claude-code, opencode). Every pane
  type now draws its own caret into the frame.
- **Column drift after emoji or CJK characters** — a phantom space was emitted
  for the second half of a double-width character, pushing everything after it
  one cell to the right.
- **Panes stuck at 80×24 after starting or restoring** — the Windows console
  drops resize events sent before the child starts reading input and never
  replays them. The size is now re-applied on the pane's first output, and pane
  dimensions are saved so restored panes start at the right size.
- **Window resize not picked up** — maximizing or restoring the window could
  leave Quil rendering at the old size. A one-second size poll recovers it, and
  the new `redraw` keybinding (`Alt+Shift+L`) forces a full repaint plus a size
  re-query.

## [1.16.0] - 2026-06-08

### Added

- **Notification events carry an excerpt of the triggering output** — every `process_exit`, `command_complete`, `bell`, and `output_idle` event now embeds the last few stripped output lines in the event's `Message` field and `Data["excerpt"]`. The notification sidebar renders the first line of the excerpt as a 4th line per event card (dim grey, blank when there is none). MCP consumers see the full excerpt in the event payload, so an agent can act on context without a follow-up `read_pane_output` round-trip. Single helper `paneOutputExcerpt(pane, n)` reads the trailing 4 KiB of the ring buffer, ANSI-strips it, and returns the last n non-empty lines; `withExcerpt(event, excerpt)` populates the fields idempotently.
- **Per-pane notification mute** — `Alt+M` toggles a `[muted]` chip on the active pane and suppresses every notification event sourced from that pane (idle, bell, OSC133, process exit). Events are dropped at the daemon, not just hidden in the UI, so muted panes never enter the queue, never wake watchers, and never reach `get_notifications`. Solves the "`npm test --watch` floods the sidebar" problem without disabling notifications globally. Mute is persisted in `workspace.json` (`paneData["muted"] = true`) and survives daemon restart. `MsgUpdatePane` gains an optional `Muted *bool` field (pointer so unset is distinguishable from explicit false).
- **MCP `dismiss_notifications` tool** — agents can finally ack events from their side. Pass `event_id` to dismiss a single event, or omit it to clear the entire queue. Closes a long-standing asymmetry: `get_notifications` was read-only, so MCP-only sessions accumulated events until the bounded queue evicted them.
- **MCP `watch_notifications` `since_timestamp` parameter** — closes the race between "kick off a task" and "start watching." When an agent passes the timestamp of the last event it handled, the daemon scans the existing event queue for the oldest event newer than the marker, returning it immediately without registering a blocking watcher. New `eventQueue.FindSince(sinceMs, paneFilter)` walks the queue oldest-to-newest so agents process events in order.

### Changed

- **Default `notification.max_events` raised from 50 to 200** — a busy multi-pane session evicts 50 events within an hour. 200 events at ~300 bytes each is ~60 KB, negligible memory, and gives genuinely useful history depth.
- **Active-pane `output_idle` events are suppressed in the sidebar** — TUI-side filter in the `paneEventMsg` handler. The pane you're staring at is by definition idle when you can see it idling; the sidebar entry is pure noise. Other event types (`process_exit`, `bell`, `command_complete`) still queue on the active pane because they're transient state changes worth a sidebar audit-trail entry.
- **`docs/mcp.md` corrected** — the event-observation section incorrectly referenced `[[notification_handlers]]` as the source of idle matches. The actual mechanism has been `[[idle_handlers]]` since the deprecated `MatchNotification` codepath was removed from the daemon; anyone editing the legacy section was getting silent no-ops. Plugin loader now logs a one-shot deprecation warning per stale plugin.

### Internal

- **Defensive nil-guards on `Daemon.broadcastState` and `emitEvent`** — both now no-op when `d.server` is nil, allowing unit tests that exercise notification dispatch and pane updates to construct a bare `Daemon` via `New(config.Default())` without spinning up the IPC server. Production behavior is unchanged — `d.server` is always non-nil after `Start()`.

## [1.15.1] - 2026-06-05

### Fixed

- **Claude Code session restore silently failed on paths with underscores** — every `claude-code` pane respawned with `--continue` instead of `--resume <session_id>` at daemon restart, so users had to manually re-attach to their Claude sessions. Root cause: `escapeClaudeCWD` only replaced `/`, `\`, and `:` with `-` when computing the path to Claude's per-project session directory (`~/.claude/projects/<encoded-cwd>/<id>.jsonl`). Claude Code also replaces `_` — so a macOS home like `/Users/Foo_Bar` lives under `~/.claude/projects/-Users-Foo-Bar/` while Quil was probing `~/.claude/projects/-Users-Foo_Bar/`. Every `claudeSessionFileExists` call returned false, both the hook-recorded id and the preassigned id failed the existence probe, and the resume path fell through to the `--continue` fallback. Hits every macOS user whose home directory contains an underscore. The encoder now also handles `_`; regression tests in `TestEscapeClaudeCWD` lock in the new cases.

### Internal

- **Snapshot refreshes session ids from hook files at shutdown** — `Daemon.Stop()` now calls a new `refreshPluginStateFromHooks()` before writing the final snapshot, copying the live `SessionStart`-hook-recorded id (which reflects post-`/clear`, post-`/resume`, post-compaction rotations) into `PluginState["session_id"]` for every `claude-code` and `opencode` pane. Without this, `workspace.json` carries the original preassigned id forever — and if the hook file is later lost (e.g. `~/.quil/sessions/` wiped, plugin uninstalled) the restore probe has nothing to fall back to. F1 → Stop daemon and signal-driven shutdowns both run through this path. Terminal panes are skipped — they have no session-id concept. Empty/error reads preserve the existing `PluginState["session_id"]` so we never strip a usable preassigned id in favor of nothing.

## [1.15.0] - 2026-06-05

### Added

- **Active-tab asterisk marker** — every active tab is now prefixed with `* ` in the bar in addition to the existing bold-on-color styling. Colored tabs already use foreground 230-on-color for active and 255-on-color for inactive — a contrast small enough that the active tab is hard to spot at a glance. The asterisk works regardless of tab color. A shared `tabLabel(idx)` helper is the single source of truth for the label string so `renderTabBar` and `hitTestTab` cannot drift — click coordinates always line up with what the eye sees.
- **macOS-friendly fallback binding for Rename pane** — keybindings now accept comma-separated alternatives in a single config field (`rename_pane = "alt+f2,alt+shift+r"`); `kbMatches` parses the spec at match time. macOS Terminal.app eats `f2` unless "Use F1, F2, etc. keys as standard function keys" is enabled in System Settings, and Option-as-Meta is terminal-specific — the second binding is the reliable fallback. The match helper is used at every keybinding site (global switch, notes-mode delegation, notification sidebar, `notesKeyExempt`) so the multi-binding behavior is uniform. `kbDisplay()` renders comma-separated specs as `"alt+f2 / alt+shift+r"` in the F1 → Shortcuts help.
- **Click-and-drag scrollbar** — left-clicking on a pane's scrollbar zone jumps the thumb to that Y position; holding the button and dragging follows the cursor's Y. The hit zone is 3 cells wide (the rightmost content column, the scrollbar column, and the right border) so off-by-one clicks still register as scroll instead of starting a text selection. The visible scrollbar is unchanged at 1 cell. Math is the exact inverse of `renderScrollback`'s thumb-position formula — a click at content row R puts the thumb's top at R (matches every GUI scrollbar). The drag rect is captured once at click time so a window resize, split, or notes-mode toggle mid-drag doesn't drift the mapping; the drag-target pane survives an active-tab switch through `activePaneByID` lookup.
- **Drag-and-drop tab reorder** — left-click a tab and drag it left or right; intermediate tabs slide one slot at a time so the dragged tab moves through positions (browser/VSCode UX) instead of swapping with whichever tab is under the cursor. `MsgReorderTab` IPC (`TabID`, `NewIndex`) carries the change to the daemon, which updates `SessionManager.tabOrder` and broadcasts the new state. The TUI's local reorder happens immediately for zero-latency feedback; the daemon's broadcast is a no-op reconciliation on the next frame. `NewIndex` clamps to the daemon-side bounds, so a stale TUI never has to race for an authoritative tab count. The original click-to-switch behavior is preserved: a click without motion just switches tabs.

### Fixed

- **Tab label overlap during rename** — typing a longer name into F2 rename grew the active tab cell but the rendered positions of the neighboring tabs lagged behind, producing a visible overlap that only cleared on a window resize. `handleRenameKey` now emits `tea.ClearScreen` on every keypress so the next frame is a full repaint, matching the "width changes — force full redraw" pattern already used in the Settings and migration dialogs. The clear is imperceptible in practice — renames are rare and the screen repaint is one frame.

### Internal

- **`Model.clearDragState()` invariant helper** — every "start a new drag" path in `MouseClickMsg` and every "drag ended" branch in `MouseReleaseMsg` routes through one helper that zeros every mutually-exclusive drag flag (`tabDragFromIdx`, `scrollDragPaneID`/`scrollDragRect`, `mouseDown`, `notesMouseDown`). A future drag mode can be added by extending the helper in one place rather than auditing every click handler for "did I clear my siblings?". `TestModel_ClearDragState` guards the invariant.

## [1.14.0] - 2026-06-05

### Added

- **Stop daemon action row in the Settings dialog** — `F1 → Settings` now ends with a "Stop daemon" entry that opens a confirmation explaining the TUI window will close and panes will respawn from the snapshot on next launch. `y` (not Enter) accepts the confirm: Enter is what every other Settings row uses to commit a toggle, so requiring an explicit `y` here prevents finger-memory misclicks from killing the daemon and every pane child. The accept handler fires `MsgShutdown` **synchronously** over the IPC client and returns `tea.Quit` — the synchronous Send eliminates the `tea.Batch` race that would otherwise let `main.go`'s `defer client.Close()` close the socket out from under the in-flight write. The daemon's stop defers (final snapshot write, PID file removal, log close) run before the TUI exits, so panes respawn cleanly on next launch. Implemented as a non-config "action row" via a new optional `settingsField.action func(Model) (Model, tea.Cmd)` — when set, Enter on Settings calls the action instead of opening the inline editor; the existing get/set/isBool wiring is untouched for the other seven config rows. Esc on the shutdown confirm returns to Settings with the cursor restored via label-lookup (`stopDaemonRowIndex()`) so a future action row inserted after Stop daemon does not misplace the cursor. Send errors are best-effort: a stale socket logs but does not block the quit, matching the operator intent that "I asked to stop" results in the TUI exiting either way. The Model's `client` field is now a small `tuiClient` interface (Send + Receive) so handler-level tests can inject a `fakeSender` and exercise the synchronous Send path, the send-error fallback, and the nil-client guard.

## [1.13.0] - 2026-06-05

### Changed
- Documentation restructured into a navigable `docs/` tree with an index, and a
  new user-facing MCP guide covering client setup, every tool, and the
  redaction model. Only `README.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, and
  `LICENSE` remain at the repository root.

## [1.12.0] - 2026-05-22

### Added

- **OpenCode AI plugin with session-id rotation tracking** — new built-in plugin (`internal/plugin/defaults/opencode.toml`) for the [opencode](https://opencode.ai) CLI, the second production AI pane type alongside claude-code. Quil tracks opencode's session-id rotation (new session, `/new`, fork, compaction) by registering a small JS plugin via the `OPENCODE_CONFIG_CONTENT` env var at pane spawn. The plugin lives entirely under `$QUIL_HOME/opencodehook/quil-session-tracker.js` (no writes into `~/.config/opencode/`) and hooks `session.created` / `session.updated` / `session.idle` / `session.compacted` / `session.deleted` events from opencode's plugin runtime, writing per-pane session ids atomically to `$QUIL_HOME/sessions/opencode-<paneID>.id`. The daemon's restore path (`opencodeResumeTemplate` in `internal/daemon/daemon.go`) consults that file and promotes the resume args to `["--session", "{session_id}"]` when an id was recorded, falling back to `["--continue"]` otherwise. `OPENCODE_CONFIG_CONTENT` merges with the user's existing opencode config (verified against opencode 1.14.x) so user-installed plugins, agents, and modes remain active inside Quil-spawned opencode panes.
- **Hardening across the opencode hook pipeline**: paths embedded into `OPENCODE_CONFIG_CONTENT` must be absolute (rejected up-front so a relative `QUIL_HOME` cannot silently break tracking under `prompts_cwd`); recorded session ids are shape-validated by `opencodehook.IsValidSessionID` (Go-side mirror of the JS plugin's regex) before promotion so a corrupted file cannot inject text into the spawn argv; `ReadPersistedSessionID` uses `O_NOFOLLOW` instead of Lstat-then-Open to close the TOCTOU window on symlink rejection; the JS plugin caps `$QUIL_HOME/opencodehook/hook.log` at 1 MB with a single rotation, de-duplicates writes (so `session.updated` bursts during a single response don't thrash the disk), and logs one `recorded <event-type> session=<id>` line per actual id change for support diagnostics. Pane-id validation is aligned via the same regex on both sides (Go `paneIDRe` and JS `PANE_ID_RE`) so a future pane-id format change can't silently disable tracking via JS-only rejection. Static-template resume args (e.g. `--continue` with empty `PluginState`) also pass through the restore-args gate (`templateHasPlaceholder` helper) so a fresh opencode pane that closed before its first session event still respawns with `--continue` instead of empty args.

## [1.11.0] - 2026-04-30

### Fixed

- **New panes spawned in the daemon's start-time CWD instead of the user's** — because `quild` is auto-started in the background, `os.Getwd()` was frozen to whatever directory was current at daemon-spawn time (typically the user's home or the launcher's path). Every new tab/pane created afterwards inherited that frozen directory regardless of where the TUI had `cd`'d. The TUI now sends its `os.Getwd()` in `MsgAttach` via a new optional `AttachPayload.CWD` field (omitempty — old clients still work), the daemon stores it in `Daemon.clientCWD` as an `atomic.Pointer[string]` so concurrent IPC dispatch goroutines read and write it race-free, and a new `defaultCWD()` helper returns the validated client value (`os.Stat` + `EvalSymlinks`) with a fallback to the daemon's `os.Getwd()` if the path is empty or stale. All six pane/tab creation sites — `handleAttach` default workspace, `handleCreateTab`, `handleDestroyTab`/`handleDestroyPane`/`handleDestroyPaneReq` auto-replacements, and `handleCreatePane` — now consume the helper. The daemon's own CWD is also pinned to `config.QuilDir()` at spawn, with `MkdirAll` guarding against `quil daemon start` failing with `chdir: no such file or directory` on a fresh install where the data directory does not yet exist. New tests `TestDaemon_DefaultCWD` (set/stale/unset/empty branches) and `TestAttachPayload_CWDRoundTrip` (back-compat with old clients omitting the field).
- **`shellinit/zsh-init.sh` broke zsh sessions running under `set -u` / `setopt nounset`** — the bare `${arr[(Ie)x]}` array-index expansion returns empty when the element is absent, which strict-mode zsh treats as an unset-variable error and aborts the init. Added `:-0` parameter-expansion fallbacks and inverted the conditionals from `(( !N )) && add` to `(( N:-0 )) || add`; semantics preserved. Affects the OSC 7 (`chpwd`) and OSC 133 (`precmd`/`preexec`) hook installers.

### Changed

- **VT emulator construction consolidated into `(*PaneModel).newVTEmulator`** — the drain goroutine that reads and discards the emulator's response pipe (a workaround for `charmbracelet/x/vt` blocking inside `Write` when nobody reads its DA1 / DA2 / DSR / OSC replies) used to be spawned inline at two call sites in `pane.go`. It is now folded into a single `newVTEmulator(w, h)` method, paired with `replaceVT(em)` (close-old → install-new), so adding a third construction site cannot accidentally skip the drain spawn. The drain itself logs unexpected (non-EOF, non-`io.ErrClosedPipe`) read errors as a breadcrumb so a future library regression that reintroduces the deadlock fails loudly instead of silently. New regression test (`TestPaneModel_AppendOutput_DoesNotDeadlockOnVTQueries`) feeds DA1, DA2, DSR, and 20× DA1 bursts through `AppendOutput` with a 2 s deadline — guards against the freeze fixed in 1.9.1.

## [1.10.2] - 2026-04-26

### Fixed

- **Daemon `Stop()` leaked goroutines on programmatic shutdown** — `Stop()` tore down the IPC server and snapshot machinery but never closed `d.shutdown`, so `idleChecker`, the memreport ctx-bridge, and `sendGhostChunked` workers stayed alive until process exit on any Stop path that didn't go through `MsgShutdown`. `Stop()` now closes the channel via `shutdownOnce` as its first action and wraps the rest in `stopOnce` for full idempotency. The IPC server is now also stopped before the final snapshot so a late-arriving `MsgCreatePane`/`MsgDestroyPane` cannot be ACK'd to a client after the on-disk snapshot is sealed.
- **Snapshot pane-count inconsistency between `workspace.json` and ghost buffers** — `snapshot()` called `SessionManager.SnapshotState()` twice (once via `buildWorkspaceState`, once for the buffer-flush loop). A pane create/destroy slipping between the two atomic reads produced an off-by-one mismatch on disk. The two halves now share a single snapshot via the new `workspaceStateFromSnapshot` helper. The periodic 30 s ticker still calls `snapshot()` directly so the safety-net write cannot be starved by sustained event-driven traffic resetting the debounce timer.
- **`paneSourceAdapter` could observe a torn pane state** — the memreport collector called six methods per pane per tick, each acquiring `PluginMu` independently. Under interleaving with a pane-exit write, the trio (`Alive`, `PID`, plugin-state size) could be inconsistent — e.g. "alive with PID 0". The seven-method `PaneSource` interface collapses into a single `Snapshot() PaneSourceSnapshot` call that takes `PluginMu` once (with `defer Unlock` for panic safety) and returns a frozen value type.

### Changed

- **MCP `get_memory_report` halves its IPC latency** — the daemon now embeds the current tab list (`Tabs []TabInfo`) directly in `MemoryReportRespPayload`, eliminating the second `MsgListTabsReq` round-trip and the tab create/destroy race window between the two requests. The MCP bridge falls back to bare tab IDs against pre-1.10 daemons during a rolling upgrade.
- **Notes editor focus indicator is now non-subtle** — when the pane-notes editor (Alt+E) is open, the header carries a persistent reverse-video badge: `INPUT` on bright blue when keystrokes route to the editor, `PANE` on orange when they route to the bound PTY. Border colour alone was easy to miss in peripheral vision, leaving a defence-in-depth gap against synthesised mouse-click focus redirection. At narrow widths the badge degrades to single-letter form (`I` / `P`) before falling back to an empty header — never to a corrupted partial that would give the same visual on both sides. Implementation uses explicit `Background`+`Foreground` rather than `Reverse(true)` so the fill colour is stable across terminal themes.

## [1.10.1] - 2026-04-25

### Changed
- Documentation caught up with v1.8.0–v1.9.x: the version handshake, the VT
  drain fix, and the Claude Code session hook are now described in the README,
  roadmap, and architecture docs.

## [1.10.0] - 2026-04-24

### Added

- **Notes editor: soft-wrap** — long lines in the pane-notes editor (Alt+E) now wrap onto the next visual row instead of being hard-truncated at the column edge with a trailing `~`. Character-wrap (not word-wrap), opt-in per editor via a new `TextEditor.SoftWrap` flag — the TOML plugin editor and F1 log viewer keep their existing truncation. Cursor Up/Down walks visual rows with column preservation; Home/End snap to the current visual row; Shift-arrow selections stay contiguous across wrap boundaries. Mouse clicks on a wrapped continuation row now resolve to the correct logical column via a new `visualToLogical` helper in `notesEditorPosAt`. Internals: `visualLayout(contentW) []visualRow` drives rendering, scroll (`ScrollTop` reinterpreted as a visual-row index when wrap is on), and navigation from a single source of truth.

### Fixed

- **End-of-line cursor invisible past a shorter selection** — in `renderLineWithSelection`, when the cursor sat at end-of-line and the selection ended earlier on the same row, the padding math reserved a cell for the cursor but never painted a reverse-video glyph on it. The cursor now renders correctly in that state. Pre-existing bug exposed more often by the new soft-wrap path.

## [1.9.2] - 2026-04-23

### Fixed

- **claude-code: session-id rotation tracking** — `/clear`, `/resume`, and compaction rotate Claude's session id to a new jsonl file. Before this fix, the daemon kept resuming the preassigned jsonl after a restart, silently restoring the pre-rotation conversation and discarding the user's post-rotation work. Quil now registers a `SessionStart` hook via `claude --settings '<inline JSON>'` at every spawn (never touches `~/.claude/settings.json`) and passes `QUIL_PANE_ID=<paneID>` in the PTY env; the hook script — shipped in `$QUIL_HOME/claudehook/` and written atomically on daemon start — writes the live session id to `$QUIL_HOME/sessions/<paneID>.id` on every rotation. `resumeTemplateFor` consults this file on restore (snapshotting `PluginState["session_id"]` under `PluginMu` before the disk probe) and resumes the current session with per-pane attribution. Hardening: `ValidateQuilDir` rejects shell-unsafe paths before hook install, `ReadPersistedSessionID` rejects pane ids containing path separators and caps reads at 256 bytes, scripts validate the extracted id matches a uuid regex before persisting and log failures to `$QUIL_HOME/claudehook/hook.log`, missing script on disk is detected at spawn time (`claudeHookSpawnPrep`) so the spawn falls back to the pre-feature behaviour instead of silently registering a dead hook. Introduces `internal/claudehook/` package with embedded sh + ps1 scripts.

## [1.9.1] - 2026-04-22

### Fixed

- **TUI freeze on claude-code pane creation** — creating a new claude-code pane could hard-wedge the Bubble Tea main goroutine, requiring a client kill. Root cause: `charmbracelet/x/vt`'s `Emulator.handleRequestMode` writes DECRQM replies to an unbuffered `io.Pipe`. Quil uses the emulator as a renderer only (ConPTY is the real terminal), so nobody drained the pipe — when claude-code sent a mode query, `SafeEmulator.Write` blocked forever *inside* Update, under its own mutex. Fix: per-pane goroutine in `internal/tui/pane.go` that reads and discards emulator replies; shutdown via `em.Close()` → `io.EOF`, wired into `ResetVT` so no goroutine leaks on VT reset. Any TUI pane running software that probes terminal modes is covered.

### Added

- **Stuck-Update watchdog + breadcrumbs** — `internal/tui/watchdog.go` launches a process-lifetime goroutine that ticks every 2 s and, if a Bubble Tea Update has been in flight for more than 10 s, writes `runtime.Stack(buf, true)` to the log. Memoized per start-ns so one wedge produces exactly one dump; `sync.Pool` reuses the 1 MiB buffer. Eight new `apply: ...` breadcrumb log lines bracket each step of `applyWorkspaceState` and the `WorkspaceStateMsg` handler so the next wedge pinpoints the line that hung to within one statement. Seven white-box tests in `watchdog_test.go` cover the logic kernel via an injected clock/stack/logger.
- **Memory reporting** — F1 → Memory opens a collapsible tab/pane tree showing Go-heap (ring buffer + ghost snapshot + plugin state), PTY child resident memory, and notes-editor bytes per pane. The status bar gains a `mem <n>` segment updated every 5 s from a new daemon-side collector (`internal/memreport/`). Cross-platform PTY RSS: `/proc/<pid>/status` on Linux, `ps -o rss=` batched on Darwin, `GetProcessMemoryInfo` on Windows. Two new MCP tools — `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single pane detail) — expose daemon-side layers for external agents. Spec at `docs/superpowers/specs/2026-04-20-memory-reporting-design.md`, plan at `docs/superpowers/plans/2026-04-20-memory-reporting.md`.
- **claude-code: per-pane resume** — multi-pane Claude sessions sharing a working directory now reattach to their own session on restore, instead of all converging on claude's "most recent in cwd" lookup. On restart, the daemon checks `~/.claude/projects/<escaped-cwd>/<session_id>.jsonl`; if present, it promotes the pane's resume args to `--resume <uuid>`. Otherwise (pane closed during claude's startup screens before any exchange persisted a session file), it falls back to `--continue`. Plugin schema bumped to v4 — users with edited `~/.quil/plugins/claude-code.toml` get the standard side-by-side migration dialog on next launch.

## [1.9.0] - 2026-04-20

### Added

- **Memory reporting** — F1 → Memory opens a collapsible tab/pane tree showing Go-heap (ring buffer + ghost snapshot + plugin state), PTY child resident memory, and notes-editor bytes per pane. The status bar gains a `mem <n>` segment updated every 5 s from a new daemon-side collector (`internal/memreport/`). Cross-platform PTY RSS: `/proc/<pid>/status` on Linux, `ps -o rss=` batched on Darwin, `GetProcessMemoryInfo` on Windows. Two new MCP tools — `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single pane detail) — expose daemon-side layers for external agents. Spec at `docs/superpowers/specs/2026-04-20-memory-reporting-design.md`, plan at `docs/superpowers/plans/2026-04-20-memory-reporting.md`.
- **claude-code: per-pane resume** — multi-pane Claude sessions sharing a working directory now reattach to their own session on restore, instead of all converging on claude's "most recent in cwd" lookup. On restart, the daemon checks `~/.claude/projects/<escaped-cwd>/<session_id>.jsonl`; if present, it promotes the pane's resume args to `--resume <uuid>`. Otherwise (pane closed during claude's startup screens before any exchange persisted a session file), it falls back to `--continue`. Plugin schema bumped to v4 — users with edited `~/.quil/plugins/claude-code.toml` get the standard side-by-side migration dialog on next launch.

## [1.8.0] - 2026-04-18

### Added

- **Client/daemon version negotiation** — the TUI now performs a version handshake with the running daemon before attaching. If the daemon is older (or pre-dates version negotiation), the TUI prompts before gracefully stopping it and auto-spawning the matching daemon from alongside the TUI binary. If the daemon is newer (i.e., the TUI is stale), the TUI refuses to attach and points the user at the releases page. Eliminates the manual "stop daemon → replace both binaries → restart" dance on every upgrade. Dev/debug builds and unstamped local builds skip the check. New IPC pair `MsgVersionReq`/`MsgVersionResp` added to the protocol; new shared `internal/version/` package provides proper semver comparison (no more lexical-ordering traps with `1.10.0` vs `1.9.0`).

## [1.7.0] - 2026-04-18

### Added

- **claude-code: `--enable-auto-mode` toggle** — the pane setup dialog (Ctrl+N → AI Tools → Claude Code) now offers Claude Code's safer auto-mode alongside the existing `--dangerously-skip-permissions` option. Both toggles share a new `permission_mode` mutual-exclusion group: enabling one automatically disables the other, and "neither" remains valid (Claude's default interactive confirmations). claude-code's plugin schema is bumped to v3 — users with edited `~/.quil/plugins/claude-code.toml` get the standard side-by-side migration dialog on next launch.
- **Plugin toggles: mutually-exclusive groups** — `[[command.toggles]]` entries now accept an optional `group = "name"` field. Toggles that share a non-empty group value render as radio buttons (`( ) / (•)`) instead of checkboxes (`[ ] / [x]`); enabling one disables the others in the group. Empty `group` keeps the existing independent-checkbox behaviour. Documented in `docs/plugin-reference.md`.
- **Event-loop perf instrumentation** — new `internal/tui/perf.go` measures per-Update-message cost, View duration, pane-output throughput, and key-backlog depth on the Bubble Tea program goroutine. Emits one aggregate Info line every 5 s and per-event Debug lines above tunable thresholds (50 ms Update, 30 ms View, 10 ms pane-output, 20 msgs key backlog). Zero overhead when stats are disabled (nil-receiver guard on every method).

### Fixed

- **Pane rendering corruption after focus toggle** — toggling focus mode (Ctrl+E) on a wide screen left narrow-column ghost rows from the pre-focus layout in TUI panes (most visible in claude-code's tool-output tree). Root cause: `PaneModel.ResizeVT` was rebuilding the VT emulator from scratch on every resize and replaying the entire raw-PTY ring buffer — including cursor-positioning sequences laid out for the previous width. The replay now uses the `x/vt` library's in-place `Resize`, which preserves the current cell grid; the PTY child redraws via SIGWINCH (already wired through `MsgResizePane`) into the resized emulator. Same fix benefits any TUI pane that resizes (vim, htop, fzf, less).
- **Shift+Tab silently swallowed in claude-code panes** — pressing Shift+Tab to cycle Claude Code modes (auto-accept / plan / etc.) had no effect since selection support landed. The pane-input router was matching every `shift+*` key with `strings.HasPrefix` and routing it into the scrollback selection handler, whose `default:` branch silently dropped any non-arrow shift combo. The guard is now a precise allow-list (`shift+arrow`, `ctrl+shift+arrow`, `ctrl+alt+shift+arrow`); everything else falls through to plugin raw-key handling and PTY forwarding. Locked in via `TestIsSelectionExtendKey`.
- **Release workflow silently skipped when squash subject came from branch name** — `release.yml` parsed conventional commits via `git log --oneline`, which strips bodies. When GitHub's "Squash and merge" defaulted the subject to the branch name (e.g. `Feat/claude-code-permission-modes`), the strict `feat(:|()` regex didn't match `Feat/`, the parser fell into the no-bump branch, and the release was silently skipped despite the body containing proper `feat(scope):` lines. The parser now scans both subject and body (`--format='%s%n%b'`), matches case-insensitively (`-i`), and accepts the `feat/branch-name` shape via `\bfeat[(:/]`.

## [1.6.0] - 2026-04-15

### Added

- **CWD memory in pane creation dialog** — the directory browser (Ctrl+N → setup) now remembers the last selected working directory within the TUI session. On the next pane creation, the browser starts from the previous selection instead of always defaulting to the Quil launch directory. Priority order: last selected CWD → active pane's OSC 7 CWD → user home. Stale directories (deleted between creations) are detected, cleared from memory, and the next candidate is tried automatically.

## [1.5.0] - 2026-04-15

### Added

- **Windows executable icon** — `quil.exe` and `quild.exe` now embed the Quil brand mark (ember Q) as a Windows resource icon, visible in Explorer, taskbar, and Alt+Tab. Build assets live in `winres/` (icon PNGs + `winres.json` manifest). `go-winres` v0.3.3 generates `.syso` files at build time — both `build`, `cross`, and GoReleaser invoke it automatically. `RT_VERSION` metadata (ProductName, FileDescription, version) surfaces in Explorer's file properties dialog.

### Fixed

- **Pane CWD ignored on creation** — selecting a working directory in the pane setup dialog (Ctrl+N → CWD browser) had no effect; the spawned process always started in the daemon's own working directory. `spawnPane()` now calls `ptySession.SetCWD(pane.CWD)` before `Start()`. The redundant `SetCWD` calls in `respawnPanes()` were removed — `spawnPane` is now the single source of truth for CWD application.

## [1.4.2] - 2026-04-14

### Fixed
- The release workflow now deploys quil.cc, so the website no longer lags a
  release.
## [1.4.1] - 2026-04-14

### Fixed
- Website: a FAQ entry was missing its question text.

## [1.4.0] - 2026-04-14

### Added

- **Three-variant build system** — `./scripts/dev.sh build` now produces 6 binaries: `quil.exe`/`quild.exe` (prod, stripped), `quil-dev.exe`/`quild-dev.exe` (auto dev mode + debug logging), `quil-debug.exe`/`quild-debug.exe` (debug logging, production data dir). Compile-time ldflags (`buildDevMode`, `buildLogLevel`, `daemonBinary`) bake in behavior — dev variant needs no `--dev` flag. Each variant auto-starts its matching daemon (e.g., `quil-dev.exe` starts `quild-dev.exe`).
- **Plugin schema migration dialog** — when a plugin's on-disk `schema_version` is lower than the embedded default, a full-screen side-by-side merge dialog blocks startup. Left pane shows the user's config (editable), right pane shows the new default (read-only). Diff highlighting: red tint for lines only in the user config, green tint for new lines in the default. Ctrl+C copies, Ctrl+V pastes, Ctrl+S saves and advances, F5 accepts the full default. Esc is blocked — migration must be resolved before using Quil.
- **Plugin schema versioning** — `schema_version` field in `[plugin]` section of embedded default TOMLs. `EnsureDefaultPlugins` returns `[]StalePlugin` for stale files instead of silently overwriting. `ParseSchemaVersion` exported for TUI validation.
- **Windows drive navigation** — the CWD directory browser (Ctrl+N → plugin setup) can now switch between Windows drive letters. Pressing backspace at a drive root (e.g., `C:\`) shows all available drives (`A:\` through `Z:\`). Selecting a drive navigates into it.
- **TextEditor: Ctrl+C copy** — copies the current selection to the system clipboard without deleting it. Previously only Enter (copy) and Ctrl+X (cut) were available.
- **TextEditor: Ctrl+Y delete line** — deletes the current line. On a single-line document, clears the line content.

### Fixed

- **Ghost buffer replay freeze** — large ghost buffers (80KB+) sent as single IPC messages starved Bubble Tea's unbuffered input channel on Windows, freezing the TUI on startup. Ghost buffers are now sent in 8 KB chunks with 2 ms yield between each, matching the live-output coalescing interval. The `sendGhostChunked` function supports early abort via the daemon's shutdown channel.
- **Stale plugin configs on upgrade** — existing users who installed Quil before v1.3.0 never received `prompts_cwd`, `[[command.toggles]]`, or the updated `resume_args = ["--continue"]` in their `claude-code.toml` because `EnsureDefaultPlugins` was create-only. Now detected and surfaced via the migration dialog.
- **Resize artifacts in full-screen dialogs** — the migration and disclaimer dialogs now skip the 150 ms resize debounce, applying window size changes immediately. Previously, maximizing the window caused rendering artifacts during the debounce window.

### Changed

- **`quil-dev.ps1` / `quil-dev.sh`** — now launch the self-contained `quil-dev.exe` / `quil-dev` binary directly instead of `quil.exe --dev`. No flags or env vars needed.
- **`scripts/dev.sh` PROJECT_DIR** — derived dynamically via `pwd -W` instead of hardcoded absolute path.
- **`quild` background mode** — stdout/stderr prints gated on `!background` instead of redirecting to `/dev/null` (eliminates a file descriptor leak).

## [1.3.1] - 2026-04-09

### Fixed
- Release notes were not being applied to published releases, and the version
  pill on quil.cc stayed at 1.2.1 regardless of the release.

## [1.3.0] - 2026-04-08

### Added

- **Pane setup dialog — working directory prompt** — when creating a `claude-code` pane (Ctrl+N → AI Tools → Claude Code), the TUI now asks for the working directory with a smart default (the active pane's CWD, tracked via OSC 7). This preserves project-specific `.claude/` context that Claude Code ties to the directory. The empty input falls back to the daemon's `os.Getwd()`, matching the old behaviour.
- **Pane setup dialog — runtime toggles (checkboxes)** — the same setup dialog renders one checkbox per plugin-declared `[[command.toggles]]` entry. claude-code ships with a single toggle, `Dangerously skip permissions`, which appends `--dangerously-skip-permissions` to the claude command line when checked. Off by default, per-pane, persists across daemon restarts.
- **Plugin TOML opt-ins** — new `prompts_cwd = true` flag under `[command]` triggers the CWD prompt for a plugin. New `[[command.toggles]]` array-of-tables declares runtime boolean switches (`name`, `label`, `args_when_on`, `default`). New `raw_keys = [...]` list forwards specific keys directly to the PTY bypassing Quil's global shortcut layer. All three are opt-in; default plugins don't set them (terminal / ssh / stripe untouched).
- **Spatial pane navigation (`Alt+Arrow`)** — `Alt+Left`/`Alt+Right`/`Alt+Up`/`Alt+Down` focus the pane in that direction. Navigation is directional, not linear: it picks the closest neighbor in the target direction based on screen coordinates, matching `tmux`'s `select-pane -L/R/U/D`. New `pane_left`/`pane_right`/`pane_up`/`pane_down` fields in `[keybindings]` — vim users can rebind to `alt+h/l/k/j` (but they'd want to move `split_horizontal` off `alt+h` first).
- **Image paste from clipboard** — pressing the paste key now reads the system clipboard for image data when no text is present. Quil decodes the DIB (or DIBV5 for alpha), encodes it as PNG, saves it under `~/.quil/paste/quil-paste-<timestamp>.png`, and types the absolute path into the active pane. AI tools like Claude Code can then read the file via their normal file tools. This sidesteps the upstream Claude Code Windows clipboard bug ([anthropics/claude-code#32791](https://github.com/anthropics/claude-code/issues/32791)).
- **Paste key aliases for Windows Terminal** — `Ctrl+Alt+V` and `F8` are now hardcoded as alternate paste triggers. Windows Terminal captures the default `Ctrl+V` for its own paste action and never delivers the key event to the running TUI; the aliases bypass that interception. `F8` is the recommended choice on Windows because it has no AltGr ambiguity on European keyboard layouts. Linux/macOS native ttys continue to receive `Ctrl+V` and don't need the aliases.
- **`internal/clipboard.ReadImage()` API** — new platform dispatch (`internal/clipboard/clipboard.go`). Win32 implementation in `image_windows.go` reads `CF_DIBV5`/`CF_DIB`, copies the DIB out of the GlobalLock, and hands off to the platform-independent DIB parser in `dib.go`. Unix/macOS get a stub returning `ErrNoImage` for now.
- **`config.PasteDir()`** — returns `~/.quil/paste/` (or `./.quil/paste/` in dev mode). The directory is created lazily by `tryPasteClipboardImage`.
- **Leveled logger** — new `internal/logger` package wraps Go's stdlib `slog`, exposes `Debug/Info/Warn/Error` helpers, and **bridges the existing 152 stdlib `log.Printf` call sites** through the same handler at info level so both old and new code respect a single filter. The level is read from `[logging] level` in `config.toml` (`"debug" | "info" | "warn" | "error"`, case-insensitive) by both `cmd/quild/main.go` and `cmd/quil/main.go` at startup. Useful for diagnosing missing-key bugs and clipboard-paste issues — flip `level = "debug"` to see the per-key handler trace, the paste pipeline, and the Win32 clipboard image read step-by-step. Default is `"info"`.
- **F1 → log viewers** — three new menu items in the F1 About dialog: `View client log` (`~/.quil/quil.log`), `View daemon log` (`~/.quil/quild.log`), and `View MCP logs` (aggregates per-pane files in `~/.quil/mcp-logs/`, most recently modified first, with file-name headers). Reuses the existing `TextEditor` in **read-only** mode (new `TextEditor.ReadOnly` field gates every mutation path: typing, paste, cut, save, enter/backspace/delete, tab, multi-line insert from clipboard). Tail-reads the last 256 KB of each file at line boundaries with a `[... older lines truncated ...]` marker. Cursor starts at the bottom so the most recent lines are in view. The viewer also rejects symlinks via `os.Lstat` so a swapped link inside `~/.quil/` cannot redirect the read to an arbitrary file.
- **Alt+Up / Alt+Down page navigation in the log viewer** — jumps the cursor by `[ui] log_viewer_page_lines` (default `40`). Configurable via `config.toml`. New `TextEditor.PageSize` field; works in both read-only and editable modes; clamps to first/last line at the edges.
- **`.claude/rules/dev-environment.md`** — project-level rule documenting the production/dev isolation constraint. Developers of Quil who run Quil in production must use dev mode (`./quil --dev`, data in project-root `.quil/`) for all testing, and never touch the production daemon or `~/.quil/` metadata.

### Changed

- **Tab and Shift+Tab are no longer intercepted globally** — previously bound to `next_pane` / `prev_pane`, which ate the keys before they could reach shell tab-completion or Claude Code's mode-cycling. Both keys now fall through to the PTY. Pane navigation moved to `Alt+Arrow` (see Added). `next_pane` / `prev_pane` config fields remain for backward compat but default to empty (unbound); users who had customized configs keep their old bindings until they edit.
- **Split shortcuts moved to `Alt+Shift+H` / `Alt+Shift+V`** (were `Alt+H` / `Alt+V`). Claude Code uses `Alt+V` to paste an image, and leaving the plain `Alt+letter` keys free for the PTY is consistent with the Tab/Shift+Tab policy. The `H for horizontal, V for vertical` mnemonic is preserved via the extra Shift.
- **Notes-mode focus toggle** (editor ↔ bound pane) is now hard-coded to Tab / Shift+Tab instead of reading `kb.NextPane`, which is now empty by default. Behavior unchanged for the end user.
- **Settings dialog (F1 → Settings) now persists every field**, not just `Show disclaimer`. Snapshot interval, ghost dimmed, ghost buffer lines, mouse scroll lines, page scroll lines, and log level all flag the config as dirty so the change is written to `~/.quil/config.toml` on TUI exit. Log-level changes apply on the next launch (no live re-init).
- **Spatial pane navigation now uses center-distance as a third tie-breaker** (after gap and overlap), matching tmux/vim/iTerm muscle memory. Previously, ties resolved by layout-tree order — now the pane whose perpendicular center is closer to the active pane's center wins.
- **`internal/plugin/registry.LoadFromDir` prunes stale plugins** — deleting a plugin's TOML file and reloading the registry now removes the in-memory entry. The Go built-in `terminal` plugin is always preserved.

### Fixed

- **`preassign_id` resume strategy preserves `InstanceArgs` across daemon restarts** — `spawnPane`'s restore branch previously replaced `args` with `ExpandResumeArgs(...)`, which dropped any runtime args (notably `--dangerously-skip-permissions` from the new setup toggle). Now the resume args are appended to the existing args slice, so both InstanceArgs and `--resume <uuid>` reach the child process on restart.

### Security

- **Paste PNG files are now owner-only.** `~/.quil/paste/` is created with mode `0o700`, individual `quil-paste-*.png` files with `0o600`, and the filename gains an 8-byte `crypto/rand` suffix so a co-tenant on a Unix machine can no longer enumerate or guess recently-pasted screenshots.
- **DIB parser hardened against degenerate dimensions.** A new per-axis cap (`maxDIBDimension = 16384`) plus `uint64` stride math defends against crafted clipboard payloads that slip under the 64 MB byte cap but would otherwise allocate gigabytes during decode. Inert on 64-bit builds today; defends future 32-bit builds.
- **Daemon CWD validation now re-resolves symlinks** in both `handleCreatePane` and `handleCreatePaneReq`. Combined with the existing TUI-side `EvalSymlinks`, this closes the small TOCTOU window where a symlink swap between Stat and exec could redirect a child process to a different directory. Applies to all IPC clients (TUI, MCP, future tooling).
- **Log viewer rejects non-regular files.** `readLogTail` runs `os.Lstat` before opening, refusing symlinks, devices, and named pipes. A re-stat through the open handle defeats a TOCTOU swap between Lstat and Open.

## [1.2.1] - 2026-04-07

### Changed
- Website redesign: ember palette, monospace hero, matte-black cards.

### Fixed
- Website: sitemap entries and canonical URLs now match how GitHub Pages
  actually serves the site, so search engines see one URL per page instead of
  two.

## [1.2.0] - 2026-04-07

### Added
- Website: per-page social preview images, richer structured data, and a web app
  manifest; page freshness dates now come from git history.

## [1.1.0] - 2026-04-07

### Added
- The marketing site at [quil.cc](https://quil.cc) — landing page, feature
  catalog, install instructions, and plugin overview.

## [1.0.0] - 2026-04-07

### Changed
- **Renamed the project from Aethel to Quil.** The binaries are now `quil` and
  `quild`, the data directory is `~/.quil/`, the config file is
  `~/.quil/config.toml`, and the environment override is `QUIL_HOME`. There is
  no automatic migration: move `~/.aethel/` to `~/.quil/` to keep an existing
  workspace, and update any MCP client configuration that referenced the old
  binary name.

## [0.13.0] - 2026-04-07

### Added

- **Pane Notes (M7)** — `Alt+E` opens a plain-text notes editor alongside the bound pane. The bound pane auto-expands to fill the available area on the left (other panes hidden, like `Ctrl+E` focus mode) and the editor takes ~40% on the right. `Alt+E` again or `Esc` exits, reverting the original layout
- **Tab/Shift+Tab focus cycle** — while notes mode is active, `Tab` and `Shift+Tab` cycle keyboard focus between the editor (default) and the bound pane. Editor-focused: text input goes to notes, border bright blue, status bar `[notes]`/`[notes*]`. Pane-focused: keys reach the PTY normally, border dim grey, status bar `[notes pane]`
- **Mouse selection in the notes editor** — click positions the cursor; click+drag creates a selection (highlighted in reverse video). Works with the existing `editorExtractText` so `Enter` and right-click both copy. Click in the pane area while notes mode is on hands keyboard focus to the pane (no Tab needed)
- **Right-click copy** — right-click in the notes editor copies the active selection to the clipboard and clears the highlight, mirroring the existing pane right-click behaviour. The notes selection takes priority over a pane selection while notes mode is active
- **Per-pane notes storage** — one markdown file per pane at `~/.quil/notes/<pane-id>.md`. Atomic temp+rename writes via `internal/persist/notes.go` (`os.CreateTemp` for race-free temp filenames, `Lstat` symlink rejection, Windows reserved-name validation). Notes survive pane destruction — orphan notes remain on disk for a future browser
- **Three save safety nets** — 30-second debounce auto-save (reset on every edit), explicit `Ctrl+S` shortcut, and an unconditional flush on exit (toggling off, structural actions, tab switch, TUI quit). Saved files always end with a trailing newline
- **`TextEditor.Highlight` field** — new typed `HighlightMode` (`HighlightTOML` default, `HighlightPlain` for notes) so the existing rune-aware editor can render plain text without TOML syntax colouring
- **`TextEditor.GutterWidth`** — dynamic line-number gutter width derived from `len(Lines)` so files with 1000+ lines render correctly and mouse-to-document coordinate mapping stays accurate
- **`NotesEditor` wrapper** — `internal/tui/notes.go` intercepts `Ctrl+S` and `Esc` before delegating to `TextEditor`, so notes bypass the TOML-specific validation path and `Esc` only exits on a second press (first press clears selection). Public API: `SetCursor`, `BeginSelection`, `ExtendSelection`, `HasSelection`, `ExtractSelection`, `ClearSelection`, `Save`, `Close`

### Changed

- **`Model.handleKey` notes routing** — restructured around `notesKeyExempt` (allow-list of global shortcuts that bypass the editor) and `exitNotesModeInPlace` (canonical teardown delegated to by `exitNotesMode`, `applyWorkspaceState`, `switchTab`)
- **`Model.notesPanelWidth`** — single source of truth for the notes layout math. Both `View()` and `notesEditorBox()` (used by mouse handlers) call it so they cannot drift apart
- **`applyWorkspaceState` notes reconciliation** — detects when the bound pane is pruned (exits notes) AND when the daemon promotes a different pane to active in the bound tab (re-syncs `ActivePane` back to the bound pane so the editor stays next to its target)
- **`Model.exitNotesMode` is pointer-receiver** — discarded calls (`m.exitNotesMode()` as a statement) still mutate the model, eliminating the silent-reinstate footgun the previous review flagged
- **Clipboard write errors logged consistently** — `model.go:294`, `:312`, and `:1086` all wrap `clipboard.Write` in an error-check + `log.Printf`
- `TextEditor` struct gained a `Highlight` field; existing call sites default to TOML highlighting for backward compatibility
- `cmd/quil/main.go` calls `Model.FlushNotes()` on TUI exit as a safety net for unsaved notes

## [0.12.1] - 2026-04-05

### Fixed
- Release notes are now applied to published GitHub releases.

## [0.12.0] - 2026-04-05

### Added

- **Notification Center (M12)** — daemon event queue with process exit detection, output pattern matching via `[[idle_handlers]]` TOML, and bell character detection with 30s cooldown. TUI sidebar toggled via Alt+N (visibility) / F3 (focus+navigate). Pane history stack with Alt+Backspace navigation. Status bar `[N events]` badge
- **Smart idle analysis** — when a pane goes idle (5s no output), last lines are analyzed against plugin `[[idle_handlers]]` patterns. SSH `[Y/n]` → "Waiting for confirmation", Claude Code prompt → "Waiting for input", password prompts detected. AI panes default to "warning" severity
- **OSC 133 command markers** — shell integration hooks extended for bash, zsh, PowerShell to emit command start/end sequences. Daemon parses `OSC 133;D` for precise command completion with exit code
- **MCP notification tools** — `get_notifications` (non-blocking) and `watch_notifications` (blocking up to 5 min, replaces polling). `requestWithTimeout` for long MCP waits
- **Plugin `path` field** — optional `path = "/full/path/to/binary"` in plugin TOML overrides PATH lookup. Fallback search in `~/.local/bin/` for Explorer-launched apps on Windows
- **Plugin `[[idle_handlers]]`** — new TOML section for context-aware idle notifications, parallel to existing `[[error_handlers]]`. Default patterns for terminal, claude-code, and ssh plugins

### Fixed

- **Focus mode mouse selection** — bypasses layout tree traversal when Ctrl+E focus mode is active, uses active pane directly
- **SSH cursor visibility** — added `"ssh"` to terminal-type check so cursor renders in SSH panes
- **Paste cursor position** — delayed re-render (100ms) after paste so cursor updates to end of pasted text
- **DecodePayload error checking** — all 11 pre-existing IPC handlers now check decode errors (was silently ignored)
- **Shutdown double-close panic** — `sync.Once` guards `close(d.shutdown)` against multiple shutdown messages
- **Watcher timer leak** — `time.NewTimer` + `defer timer.Stop()` replaces `time.After` in watch goroutine and MCP bridge
- **Idle detection race** — single `PluginMu` lock span for read+write in `checkIdlePanes` prevents race with `flushPaneOutput`
- **PowerShell 5.1 compat** — shell init uses `[char]0x1b` for escape instead of `` `e `` which only works in PowerShell 7+
- **Zsh exit code capture** — `precmd` saves `$?` to local immediately, inserted first in `precmd_functions` before OSC 7

### Changed

- **IPC server** — `onDisconnect` callback now receives `*Conn` for watcher cleanup on disconnect
- **`flushPaneOutput` refactored** — extracted `detectBellEvent`, `detectOSC133Exit`, `applyPluginHandlers` helpers
- **Notification matching moved to idle time** — patterns run against last 5 lines at idle, not on every output chunk (eliminates false positives from arrow keys, command history)

## [0.11.0] - 2026-03-25

### Added

- **MCP Server (M10)** — `quil mcp` subcommand exposes Quil to AI assistants via Model Context Protocol. 13 tools: `list_panes`, `read_pane_output`, `send_to_pane`, `get_pane_status`, `create_pane`, `send_keys`, `restart_pane`, `screenshot_pane`, `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane`, `close_tui`
- **Official MCP SDK** — uses `modelcontextprotocol/go-sdk` v1.4+ with typed tool handlers and struct-based input schemas
- **Request-response IPC** — backward-compatible `Message.ID` field for correlating MCP requests; daemon responds to specific connection when ID is set, broadcasts when empty
- **Process exit tracking** — `WaitExit()` on PTY `Session` interface with `sync.Once` for safe concurrent access; `Pane.ExitCode` and `Pane.ExitedAt` fields
- **VT-emulated screenshots** — `screenshot_pane` tool feeds ring buffer through `charmbracelet/x/vt` terminal emulator to capture actual screen state; essential for interactive TUI apps
- **Named key sequences** — `send_keys` tool with 50+ key mappings (arrows, function keys, ctrl+a-z); escape sequences sent individually with 50ms pacing for TUI compatibility
- **Orange MCP highlight** — pane border flashes orange (color 208) when AI interacts via MCP; configurable duration via `[mcp] highlight_duration` (default 10s)
- **Per-pane MCP logging** — interaction metadata logged to `~/.quil/mcp-logs/{pane-id}.log`; two-layer redaction: AI markers (`<<REDACT>>...<</REDACT>>`) + regex fallback for common secret patterns
- **MCP server instructions** — tool usage guidelines and sensitive data handling protocol sent to AI clients during initialize handshake
- **TUI cooperation tools** — `set_active_pane` broadcasts to TUI for pane focus; `close_tui` exits TUI while daemon persists
- **Notification center PRD update** — added MCP integration section: `watch_notifications` blocking tool, event hub architecture, AI as event consumer

## [0.10.2] - 2026-03-24

### Fixed
- Fixed the release build, which was failing to produce binaries.

## [0.10.1] - 2026-03-24

### Fixed

- **GoReleaser workflow not triggering** — tags pushed with `GITHUB_TOKEN` don't trigger other workflows; merged goreleaser into `release.yml` as a second job with `needs: release`
- **Dry run executing goreleaser** — boolean vs string comparison bug in job `if:` condition; `DRY_RUN` now forwarded through job outputs as string
- **Actions pinned to commit SHAs** — `actions/checkout`, `actions/setup-go`, `goreleaser/goreleaser-action` pinned to immutable SHAs for supply-chain security
- **Per-job permissions** — `contents: write` moved from workflow-level to per-job blocks for least-privilege

## [0.10.0] - 2026-03-24

### Added

- **Roadmap PRDs** — 11 detailed Product Requirements Documents in `docs/roadmap/`: workspace files, MCP server, command palette, notification center, pre-built binaries, demo GIF, community plugins, process health, tmux migration, cross-pane events, session sharing
- **Restructured ROADMAP.md** — organized into Core/Growth/Advanced categories with priority matrix, strategic pain-layer analysis, and feature synergy notes
- **Notification center concept (M12)** — centralized event sidebar with pane navigation and history stack; PRD covers process exit detection, plugin notification handlers, and incremental integration path
- **Pre-built binaries & release infrastructure** — GoReleaser config for 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64); `release.yml` handles version bump + tag + GoReleaser build, publishes GitHub Release with `.tar.gz`/`.zip` archives and SHA256 checksums
- **One-line install script** — `scripts/install.sh` detects OS/arch, fetches latest release from GitHub API, verifies SHA256 checksum, installs to `~/.local/bin/`; supports `QUIL_VERSION` for pinned installs and `GITHUB_TOKEN` for API auth
- **Daemon version reporting** — `quild version` subcommand, version logged at startup; consistent `-ldflags` injection across all build paths (GoReleaser, dev.sh, dev.ps1, rebuild.ps1, Makefile)

### Fixed

- CI Go version mismatch — updated from 1.24 to 1.25 in `ci.yml` and `release.yml` to match `go.mod`

## [0.9.0] - 2026-03-23

### Added

- **Pane focus mode (M6)** — Ctrl+E toggles active pane to full-screen; other panes keep running in background; `* FOCUS *` border label; `[focus]` status bar indicator; splits/close auto-exit focus
- `focus_pane` keybinding config field (default `ctrl+e`)

## [0.8.0] - 2026-03-22

### Added

- **Editor text selection** — Shift+Arrow (character), Ctrl+Shift+Arrow (word), Ctrl+Alt+Shift+Arrow (3 words), Shift+Home/End (line) in TOML editor
- **Editor clipboard** — Enter copies selection, Ctrl+X cuts, Ctrl+V pastes (async via `editorPasteMsg`), Ctrl+A selects all
- **Editor selection rendering** — reverse video highlight with cursor-within-selection underline
- **Editor selection-aware editing** — typing with selection replaces selected text; backspace/delete removes selection
- **Editor multi-line paste** — Ctrl+V and bracketed paste handle newlines, splitting into editor lines
- **Editor shortcuts in help** — F1 → Shortcuts shows editor selection and clipboard shortcuts
- **Editor paragraph navigation** — Ctrl+Up/Down jumps to next/previous empty line; Ctrl+Shift+Up/Down selects to paragraph boundary
- **Editor word navigation** — Ctrl+Arrow (1-word) and Ctrl+Alt+Arrow (3-word) jump in editor
- **Beta disclaimer dialog** — shown on first launch with random tips/shortcuts; "Don't show again" persists to `config.toml`
- **Config save** — `config.Save()` function for atomic config persistence (used by disclaimer opt-out)
- `ui.show_disclaimer` config field (default `true`)

## [0.7.0] - 2026-03-22

### Added

- **Bubble Tea v2 + Lipgloss v2 migration** — declarative `View()` returning `tea.View`, typed mouse events (`MouseClickMsg`, `MouseWheelMsg`, `MouseMotionMsg`, `MouseReleaseMsg`), `KeyPressMsg` replaces `KeyMsg`, Go 1.25
- **Text selection** — keyboard selection via Shift+Arrow (character), Ctrl+Shift+Arrow (word), Ctrl+Alt+Shift+Arrow (3 words), and mouse click+drag; Enter copies selection to clipboard; Esc clears; right-click copies
- **Platform-native clipboard** — `internal/clipboard/` with Read/Write: Win32 `GetClipboardData`/`SetClipboardData` on Windows, `pbpaste`/`pbcopy` on macOS, `xclip`/`xsel` on Linux; bounded reads (10MB max); cached tool detection on Unix
- **Bracketed paste** — Ctrl+V wraps clipboard content in `ESC[200~...ESC[201~` sequences for safe multi-line paste
- **Paste in dialogs** — Ctrl+V works in dialog input fields (SSH connection form, Settings); control characters sanitized before insertion
- **Ctrl+Arrow word jump** — sends `ESC[1;5C`/`ESC[1;5D` to PTY for shell word navigation
- **Ctrl+Alt+Arrow 3-word jump** — sends triple word-jump escape sequences
- **Stripe dialog wider** — `dialog_width = 75` for long forward URLs
- **SSH dialog wider** — `dialog_width = 100` for long connection details
- **Selection shortcuts in help** — F1 → Shortcuts shows Shift+Arrows, Ctrl+Shift+Arrows, Enter, Right-click, Esc
- `FindPaneRectAt` layout method for mouse-to-pane coordinate mapping
- `scripts/rebuild.ps1` — kill daemon, reset state, rebuild executables

### Changed

- Scripts moved from project root to `scripts/` directory
- `dialogBorder.Width()` uses Lipgloss v2 border-inclusive semantics (`Width(width)` on border, `Width(innerW+2).Height(innerH+1)` on pane body)
- Plugin `dialog_width` override now scoped to instance-specific screens only (instance list and form), not all create-pane dialog steps
- `tea.ClearScreen` fired on dialog open and width-changing transitions to prevent BT v2 diff renderer artifacts
- Ghost buffer VT reset now only for `claude-code` pane type (SSH and other terminal-like panes preserve history)
- Docker images updated from `golang:1.24-alpine` to `golang:1.25-alpine`
- Cursor hidden via `\x1b[?25l` — custom cursor rendered via `insertCursor()`

### Fixed

- Pane border/size wrong after Lipgloss v2 migration — Width/Height now compensate for border-inclusive semantics
- Dialog border broken on first render — `tea.ClearScreen` on pane-to-dialog transitions
- Dialog border broken on width change — `tea.ClearScreen` on plugin selection with custom `dialog_width`
- Edit cursor glyph not rendering on Windows — replaced `▎` (U+258E) with `│` (U+2502)
- Paste broken everywhere after v2 migration — restored platform-native `clipboard.Read()` (OSC 52 read not supported by most terminals)
- SSH ghost buffer not restored after daemon restart — VT reset condition changed from "all non-terminal" to "only claude-code"
- Selection extending into empty terminal lines — bounded by `lastContentLine()`
- Soft-wrap detection in text extraction — detects both VT character wraps and near-edge content

### Removed

- Custom `utf16PtrToString` — replaced with `windows.UTF16PtrToString` from `golang.org/x/sys/windows`

## [0.6.0] - 2026-03-18

### Added

- **Plugin configuration reference** — comprehensive documentation for creating custom plugins covering every TOML section, field, strategy, and behavior with annotated examples (`docs/plugin-reference.md`)
- **Default TOML plugins** — claude-code, ssh, stripe shipped as editable embedded TOML files via `//go:embed`; written to `~/.quil/plugins/` on first run, user edits preserved across upgrades
- **Plugin instance management** — `InstanceStore` persists saved SSH connections, Stripe webhooks, etc. to `~/.quil/instances.json`; form fields + arg templates defined per-plugin
- **Plugin management UI** — F1 → Plugins dialog with view, reload, restore defaults, and in-app TOML editor
- **In-app TOML editor** — full-screen multi-line editor with rune-aware cursor, TOML syntax highlighting (comments grey, sections orange, keys blue, strings green), validation on save
- **Pane creation instance step** — Ctrl+N dialog extended: category → plugin → instance selection → split direction (4 steps)
- **Centralized snapshot queue** — event-driven `snapshotCh` channel with 500ms debounce replaces scattered `snapshotDebounced()` calls; triggers on create/destroy tab/pane, switch tab, update layout, client disconnect
- **Per-plugin ghost buffer toggle** — `GhostBuffer` bool on `PersistenceConfig` controls whether PTY output is saved to disk per plugin type
- **GhostSnap restore** — pure disk-loaded ghost data stored separately from live ring buffer, preventing respawned shell init output (e.g., ConPTY clear screen) from contaminating history replay
- **Diagnostic logging** — comprehensive trace logging across daemon (IPC dispatch, attach, snapshot metrics, ghost replay, spawn, tab/pane lifecycle) and TUI (ghost transitions, workspace state, layout restore); IPC server logs client connect/disconnect
- `MsgReloadPlugins` IPC message for hot-reloading plugin configuration
- `onDisconnect` callback on IPC server — triggers snapshot on client disconnect
- Socket permissions restricted to `0600` after creation
- `InstancesPath()` config path helper

### Changed

- 3 of 4 built-in plugins moved from Go code to embedded TOML defaults — only terminal remains in Go (needs runtime shell detection)
- `NewServer()` accepts `onDisconnect` callback as third parameter
- Ghost buffer replay in `handleAttach` prefers `GhostSnap` (clean disk data) over `OutputBuf` (may contain post-restore shell output)
- `handleUpdateLayout` now triggers snapshot request (was missing — caused layout loss on daemon kill)

### Fixed

- Terminal history not restored after daemon restart — `ResetVT()` no longer called for terminal panes on ghost→live transition; GhostSnap prevents shell init contamination of ghost replay
- Fresh pane on first run incorrectly showing "resuming..." spinner — only set `resuming=true` when tab has saved layout
- Confirm dialog extended for instance deletion (`confirmKind = "instance"`)

## [0.5.0] - 2026-03-16

### Added

- **Plugin system** — typed panes with 4 built-in plugins: Terminal, Claude Code (AI), Stripe (webhook), SSH (remote)
- `internal/plugin/` package — plugin structs, registry, TOML loading, regex scraper, error handler matching
- TOML plugin format — user-created plugins in `~/.quil/plugins/*.toml` with command, persistence, error handlers, and instances
- Plugin registry with `DetectAvailability()` — checks PATH for plugin binaries at startup
- Pane creation dialog (`Ctrl+N`) — three-step flow: category → plugin → split direction (horizontal, vertical, replace)
- Atomic pane replacement via `ReplacePane()` — swap pane type in-place without layout disruption
- **Session resume for Claude Code** — pre-assigned UUID via `--session-id`, resumed with `--resume` after daemon restart
- `preassign_id` persistence strategy — generate UUID at pane creation, store in `PluginState`, resume on restore
- `session_scrape` persistence strategy — regex scraper extracts tokens from PTY output for resume
- `rerun` persistence strategy — re-execute same command + args on restore (SSH, Stripe)
- Error handler system — match PTY output against regex patterns, show help dialogs (e.g., SSH auth failure, missing API key)
- `MsgPluginError` IPC message — daemon-to-TUI error notification with modal dialog display
- Resuming/preparing spinner — animated braille indicator (`⠹ resuming...` / `⠹ preparing...`) on pane border during startup
- Window size persistence — save/restore terminal dimensions via `~/.quil/window.json`
- Platform-specific window restore — Win32 `MoveWindow`/`ShowWindow` on Windows, xterm sequence on Unix
- Maximized window state detection and restoration via `IsZoomed`/`SW_MAXIMIZE`
- `PluginsDir()` and `WindowStatePath()` config path helpers
- Plugin state fields on `Pane` struct — `Type`, `PluginState`, `InstanceName`, `InstanceArgs`
- Workspace JSON backward compatibility — missing `type` defaults to `"terminal"`

### Changed

- `spawnShell()` replaced with generalized `spawnPane()` — dispatches by plugin type and resume strategy
- `respawnShells()` replaced with `respawnPanes()` — fallback to terminal shell on plugin spawn failure
- Ghost buffer replay skipped for TUI app panes (`preassign_id`, `session_scrape`) — prevents cursor state pollution
- Quil cursor overlay disabled for non-terminal panes — TUI apps render their own cursor
- `CreatePanePayload` extended with `Type`, `InstanceName`, `InstanceArgs`, `ReplacePaneID`
- `NewModel()` accepts plugin registry parameter
- Status bar updated with `^N pane` hint

### Fixed

- Regex compilation uses `regexp.Compile` (not `MustCompile`) — invalid TOML patterns log errors instead of crashing daemon
- Nil guard in `ScrapeOutput`/`MatchError` for uncompiled patterns
- Data race on `Pane.PluginState` — protected with `PluginMu` mutex
- `hitTestTab` missing tab index prefix — click targets now match rendered tab widths
- Scraped values truncated in log output — prevents leaking tokens/secrets
- Error handler patterns anchored — `Permission denied (publickey` and `error.*API key` avoid false matches
- `loadPluginTOML` validates strategy, cmd, and error handler action fields
- `loadPluginTOML` defaults `DisplayName` to `Name` and `Category` to `"tools"` when empty
- Layout `resizeNode` nil guard for placeholder nodes during pane replacement
- `ExpandResumeArgs` returns nil when placeholders are unresolved — prevents passing literal `{session_id}` to tools
- `window_windows.go` bounds-checks pixel dimensions and `GetWindowRect` return value
- `saveWindowSize` logs `WriteFile` errors

## [0.4.1] - 2026-03-14

### Added

- Multi-instance support via `QUIL_HOME` env var — run production and dev instances simultaneously
- `--dev` CLI flag — uses `.quil/` in project root for isolated dev data
- Dev launcher scripts: `quil-dev.sh` / `quil-dev.ps1`
- `[dev]` indicator in status bar when running in dev mode
- `TestQuilDir_EnvOverride` test for env var override

### Fixed

- Daemon log file permission changed from `0644` to `0600` for consistency with other sensitive files
- `resizeAllPanes()` nil guard — prevents panic when tab has no panes
- `os.Executable()` error handling in `--dev` flag — exits with clear message instead of silent fallback

## [0.4.0] - 2026-03-14

### Added

- Workspace snapshot persistence — tabs, panes, layout, and CWD saved to `~/.quil/workspace.json`
- Atomic file writes with `.bak` rollback for crash-safe persistence
- Ghost buffer persistence — raw PTY output saved per pane to `~/.quil/buffers/*.bin`
- Automatic workspace restore on daemon restart — tabs, panes, and layouts reconstructed from disk
- Shell respawn with saved CWD — panes reopen in the directory you were last working in
- Periodic snapshot timer (configurable via `snapshot_interval`, default 30s)
- Immediate snapshot on structural changes (tab/pane create/destroy) with 1s debounce
- Orphan buffer cleanup — removes `.bin` files for panes that no longer exist
- Ghost buffer dimming — restored panes show muted border and "restored" label until live output arrives
- Modal dialog system — F1 opens About screen with Settings editor and Shortcuts reference
- Confirmation dialogs for pane close (Ctrl+W) and tab close (Alt+W)
- Tab index numbers in tab bar (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts
- Auto-recovery — deleting last tab or last pane auto-creates a fresh replacement
- PTY output coalescing — 2ms timer batches rapid output to prevent visual tearing with interactive tools
- Version display in status bar and About dialog
- Developer utility scripts: `kill-daemon.sh/.ps1`, `reset-daemon.sh/.ps1`
- Build-time version injection via `-ldflags` in `dev.sh`

### Fixed

- Scrollback rendering now preserves ANSI colors (cell styles were previously dropped)
- Escape key forwarded to PTY — was mapped as `"escape"` but Bubble Tea uses `"esc"`
- Tab switch state broadcast — `handleSwitchTab` now calls `broadcastState()` + `snapshotDebounced()`
- Tab switch evaluation order — separated `switchTab()` from return to prevent stale `activeTab`
- Active tab index clamped after workspace state sync to prevent out-of-bounds

## [0.3.0] - 2026-03-12

### Added

- Daemon process detachment — survives TUI exit on all platforms (Unix: `Setsid`, Windows: `DETACHED_PROCESS`)
- `quil daemon status` command — reports daemon PID and connectivity
- PID file tracking (`~/.quil/quild.pid`) for lifecycle management
- `quild --background` flag — suppresses stdout/stderr for silent auto-start
- Daemon binary co-location lookup — finds `quild` alongside `quil` when not on PATH (fixes Windows Go 1.19+ LookPath)
- Stale socket cleanup — detects dead daemon sockets and removes them before starting fresh

### Fixed

- Daemon dying when TUI exits on Windows (missing `DETACHED_PROCESS` creation flag)
- `os.Exit(0)` in shutdown handler skipping deferred cleanup — replaced with channel-based signaling
- PID file written before `~/.quil/` directory guaranteed to exist

## [0.2.0] - 2026-03-12

### Added

- Client-daemon architecture with IPC over Unix sockets (Named Pipes on Windows)
- Cross-platform PTY management (`creack/pty` on Unix, ConPTY on Windows)
- Bubble Tea TUI with tab bar, bordered panes, and status bar
- Horizontal and vertical pane splitting
- Keyboard navigation between panes and tabs
- TOML configuration with sensible defaults (`~/.quil/config.toml`)
- Daemon auto-start on first client attach
- `quil daemon start/stop` CLI commands
- Length-prefixed JSON IPC protocol with typed messages
- Default shell workspace created on first attach
- Docker-based development workflow (`dev.sh`) — no local Go or make required
- Multi-stage Dockerfile producing minimal scratch-based release images
- `.dockerignore` for optimized Docker build context
- Binary split pane layout with mixed horizontal/vertical splits (tmux-style)
- Layout persistence — pane tree serialized to JSON, restored on reconnect
- Output history replay — ring buffer captures PTY output, replayed to reconnecting clients
- VT100 terminal emulation via `charmbracelet/x/vt` for proper ANSI rendering
- Live CWD tracking — pane border updates via OSC 7 escape sequences
- Automatic shell integration — OSC 7 hooks injected into bash, zsh, PowerShell at spawn time
- Tab renaming (F2) and pane renaming (Alt+F2)
- Tab color cycling (Alt+C) with 8 color options
- Mouse support — click to switch tabs/panes, scroll wheel for terminal history
- Clipboard paste (Ctrl+V) with cross-platform support (Win32 API, pbpaste, xclip)
- Terminal scrollback with page scroll (Alt+PgUp/PgDown) and scrollbar indicator
- Resize debouncing for smooth terminal resizing
- Configurable keybindings via `[keybindings]` in config.toml
- Configurable mouse scroll lines and page scroll lines via `[ui]` in config.toml
- Structured logging for both client and daemon (`~/.quil/*.log`)
