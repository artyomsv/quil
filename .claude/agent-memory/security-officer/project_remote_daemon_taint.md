---
name: remote-daemon-string-taint
description: Every string in a daemon's workspace_state broadcast (project name, pane name, tab name, CWD, git branch, blockedReason) is attacker-controlled when the daemon is remote; sanitizeRemoteText at render is the only mitigation and it is opt-in per call site
metadata:
  type: project
---

A multi-daemon client renders strings that originated on a host the user may
not control. `sanitizeRemoteText` (`internal/tui/remotetext.go`) is the
project's mitigation — it strips C0/DEL/C1 (incl. U+009B, the single-rune CSI
introducer) and bidi overrides/isolates — but it is applied **per call site at
render time**, never on the wire, so every new render path is a new gap by
default.

**Why:** the raw value is load-bearing downstream (a browsed path becomes a
pane's spawn CWD, a repo path becomes lazygit's `--path`), so the codebase
deliberately refuses to sanitize on ingest. That decision makes call-site
coverage the whole control.

**What an attacker controls:** anything in a daemon's `workspace_state` —
`projects[].name`, `projects[].root_dir`, pane/tab names, pane CWD, git branch
(from the checked-out repo), `blockedReason` (from hook spool `Data["tool"]`,
see [[hookevents-taint-boundary]] and [[osc7-cwd-taint]]). Reachable by anyone
who can write that host's `~/.quil/workspace.json` or speak to its unix socket
— not only by a full host compromise.

**Non-string display state is in scope too, and `pinned_attention` is the
example to reason from** (added c26bf1a, PR #146). It used to be TUI-session
state a daemon could not touch; it is now daemon-declared, `syncPaneMeta`
copies it UNCONDITIONALLY, and the client's own "Unmark"/"Clear attention"
only SEND — so a daemon that ignores the send re-asserts the mark on the next
broadcast (the git ticker guarantees one every 5 s) and the pin is unclearable
from the client by design. Impact is display-only (nothing outside rendering
reads it; `attention.go`'s queue keys on `blockedSince`). **How to apply:**
when a client-owned flag moves daemon-side, ask whether any client action is
still able to win against a peer that ignores it — "the user's own mark" is a
claim about a daemon the user controls.

**How to apply:** when reviewing any TUI render path, ask whether the string
came from `applyWorkspaceState` or an IPC response. Note that `fmt.Sprintf`
with `%q` incidentally neutralises this (strconv.Quote escapes non-`IsPrint`
runes, U+202E included) while `truncateToWidth`, `ansi.Truncate` and
`lipgloss.Render` all **preserve** escape sequences — a width check is not a
sanitiser, because an escape sequence measures zero cells.

Confirmed gaps at review of `feat/projects-sidebar` (2026-08-03, round 1):
project ctx-menu title, the Rename form's Name field, and the command
palette's project-qualified pane labels. The sidebar itself was correct.

**The sidebar cutters bound CELLS, never BYTES — and a width sweep cannot see
the difference.** `truncateCells` (`internal/tui/sidebar.go`) opens with
`if lipgloss.Width(s) <= w { return s }`, so a name made of printable
ZERO-WIDTH codepoints (U+200B et al., which `sanitizeRemoteText` preserves by
design) measures 0 and is returned **whole**, however many megabytes it is.
Every row then measures exactly `w` cells, so `TestSidebarRows_MeasureExactlyTheirWidth`,
`renderSidebar`'s closing `.Width(w)` and every cell-budget assertion all pass
while the frame carries the flood. The 2026-07 fix made the cutters LINEAR
(no more quadratic re-measurement); it did not make them BOUNDING.
`formMsgNameCap` (`internal/tui/projectdialog.go`) is the only surface that
bounds length, and it covers the project form's message line alone.

**How to apply:** when reviewing a TUI render path, "it is truncated" and "it
is bounded" are separate questions — ask the second one explicitly. A
cell-width test is not evidence of the second.
