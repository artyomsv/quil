# `ansi.Strip` passes bare C0 and C1 controls through

**Criticality:** 3 (medium) — pre-existing, and reaching it needs a hostile or
compromised remote host, or a local process deliberately emitting control bytes.
Not reachable by accident.

**Complexity:** 3 — one helper plus ~4 call sites, but each site has its own
notion of what must survive (newlines in excerpts, for instance), so a blanket
filter is wrong.

## What

`ansi.Strip` (charmbracelet/x/ansi) removes **structured** escape sequences. It
does not remove bare control bytes that are not part of a collected sequence,
and it does not recognise the C1 forms at all. Measured against
`x/ansi` as vendored today:

| Input | `ansi.Strip` output | Verdict |
|---|---|---|
| `a` BEL `b` | `a` BEL `b` | control survives |
| `a` BS `b` | `a` BS `b` | control survives |
| `a` ESC `[31m` `b` | `ab` | correctly stripped |
| `a` U+009B `31m` `b` | `a` U+009B `31m` `b` | **whole CSI sequence survives** |
| `a` ESC `b` | `a` | control removed, **but `b` is eaten too** |

Two things matter here beyond "some bytes survive":

1. **U+009B is the C1 form of CSI.** The last row is not a stray byte, it is a
   complete, functional SGR sequence that `Strip` does not see. This codebase
   already knows its VT emulator acts on raw C1 bytes — that is why
   `internal/tui/oscfilter.go` exists, and why
   `techdebt/3-3-xvt-treats-0x9c-as-st-in-utf8.md` is open. Same family.
2. **A lone ESC silently consumes the next character.** Not a security issue,
   but it is quiet data loss in excerpts.

## Where

- `internal/daemon/daemon.go:2891, 2946, 3211` — notification-sidebar excerpts
  (`paneOutputExcerpt` and friends)
- `internal/daemon/search.go:32` — palette content-search excerpts

Both feed strings that originate as **pane output** into UI chrome. The MCP
`read_pane_output` tool is documented as "ANSI-stripped" and inherits the same
gap.

## Why it surfaced now

Phase 3 (`quil --remote`) moved the daemon to a machine the user may not
control. Pane output has always been attacker-influenced in principle — a
program you run can print anything — but with a remote daemon the *host* is a
different trust domain. The comments around these call sites reason carefully
about what `ansi.Strip` leaves behind (see `daemon.go:2905, 2953, 3008`), so the
gap was partly known; what was not established is that C1 sequences pass through
**intact and functional**.

Found while auditing render sites for Phase 3's `sanitizeRemoteText` work,
verified empirically with a throwaway probe rather than by reading the library.

## Fix sketch

`internal/tui/remotetext.go`'s `sanitizeRemoteText` already does the right
classification (drops C0 including ESC, DEL, and C1 U+0080–U+009F; maps tab to
space; leaves printable non-ASCII byte-identical). The work is not inventing a
filter, it is:

1. deciding per call site what must survive — excerpts legitimately contain
   `\n`, which `sanitizeRemoteText` currently drops, so a variant that preserves
   newlines is needed for those;
2. applying it **after** `ansi.Strip` rather than instead of it — `Strip` still
   does the structured-sequence half correctly;
3. deciding whether the daemon or the renderer owns it. The Phase 3 precedent is
   **sanitize at render, keep raw values in state**, because the raw value is
   often also used as data (a spawn CWD, a search key). Excerpts are display-only,
   so daemon-side may be defensible here — but that is a decision, not an
   obvious default.

## Not in scope of the finding

Pane names, pane CWDs and tab titles from `workspace_state` are also
daemon-supplied and rendered as chrome. They share this exposure and are
deliberately excluded here — they predate Phase 3 and want their own assessment.
