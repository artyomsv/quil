---
headline: A version-drifted remote host is offered the upgrade again
---
- **A remote host left behind by a client update is offered the upgrade again.** When
  this client is newer than the daemon on a configured host, Quil refuses to attach —
  by design — and offers to upgrade that host from inside the tool. The offer was
  raised at launch and immediately discarded, because the launch path records the
  offline host before it wires the provisioner that would perform the install. The
  result was a project row wearing a ⚡ and nothing else: the host stayed unusable for
  the whole session, and every later client update widened the gap.

- **The offer survives the dialogs a fresh launch puts in front of it.** It is raised
  before the screen is free, and only the offer's own dismissal used to collect it
  again — so on the one launch where it matters most, right after a client update, the
  what's-new dialog swallowed it. Every dialog now hands it back on the way out. A host
  that reconnects before you answer is no longer offered an upgrade it does not need,
  which matters because accepting one restarts that daemon and takes its panes with it.

- **A host that has never run Quil is asked the right question.** The same prompt served
  both cases and was worded for an upgrade, so a first install warned about killing
  panes on a machine that had none.

- **A project whose host is offline says so, instead of claiming it has no tabs.** The
  pane area rendered the same "No tabs in … — Ctrl+T opens one" it shows for an empty
  live project, which reads as the tabs having been deleted. The remote daemon still
  holds every one of them; this client simply did not attach. It now names the host,
  says whether the problem is a version drift, a missing install, or a link still
  reconnecting, and carries the command that fixes it.
