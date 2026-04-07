# Notes mode focus swap needs stronger visual cue

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Small |
| Location | `internal/tui/model.go` (mouse handlers around L333-L360, View() footer rendering, status bar marker) |
| Found during | Code review of PR #15 (M7 Pane Notes), security-officer agent finding L1 |
| Date | 2026-04-07 |

## Issue

While notes mode is active, clicking in the pane area silently sets
`m.notesPaneFocused = true`, which redirects subsequent keystrokes from the
notes editor to the bound pane's PTY. The user-visible cues for this
focus transition are subtle:

1. The notes editor border changes from `lipgloss.Color("63")` (bright
   blue) to `lipgloss.Color("240")` (dim grey).
2. The status bar marker switches between `[notes]` / `[notes*]` and
   `[notes pane]`.
3. The footer hint inside the editor box switches between
   `Tab pane  Ctrl+S save  Esc exit  Alt+E` and `Tab notes  Alt+E exit`.

All three cues are *passive* — the user must actively look at the screen
to notice. There is no transient flash, no audible cue, and no persistent
non-subtle indicator that focus has moved.

## Risks

This is a **defence-in-depth** observation, not an exploitable
vulnerability today. Exploitation would require all three preconditions:

1. A pane process capable of synthesising xterm mouse-reporting escape
   sequences (`\x1b[?1000h`, `\x1b[M...`) — e.g., a malicious tool
   running inside an SSH session.
2. The host terminal emulator re-emitting those sequences as stdin
   (depends on the host terminal's handling of mouse-reporting modes
   while another program — Aethel itself — is consuming mouse events).
3. The user failing to notice the border-colour and status-bar
   transitions, then continuing to type sensitive content (passwords,
   API keys, private notes) under the assumption that input still goes
   to the notes editor.

If all three line up, the synthesised mouse click silently redirects the
user's keystrokes from the in-process notes buffer to a (potentially
attacker-controlled) PTY child process. Sensitive content ends up in the
shell's input stream rather than the local notes file.

This risk is the **same class** of issue that affects every terminal
multiplexer that processes mouse events while child processes can also
emit mouse-reporting sequences. It is **not introduced** by the Pane
Notes feature — the underlying mouse-event-routing path was present
before. However, the focus-swap behaviour added by `c67fb40` *amplifies*
the consequence: previously a synthesised click only opened a pane
selection (visible on screen and harmless if ignored), now it
additionally redirects keyboard input.

## Suggested Solutions

All three are mutually compatible — the user can pick one or combine
them.

### Option A: Briefly flash the editor border on focus swap (Trivial)
On every transition of `m.notesPaneFocused`, schedule a Bubble Tea tick
that renders the editor border in a high-contrast colour (bright yellow,
red) for 200-300ms before settling into the normal focused/unfocused
colour. The flash is impossible to miss in peripheral vision but does
not steal attention long-term. Implementation: a single `flashUntil
time.Time` field on `Model`, checked in `notesEditor.View()` to override
the border colour when `time.Now() < flashUntil`.

### Option B: Persistent reverse-video focus marker (Trivial)
Add a small reverse-video badge (`⏵` or `[FOCUS]`) to the upper-right
corner of whichever side currently has focus. The marker is persistent
(no animation), so the user gets an unambiguous visual answer to "where
will my next keystroke go?" without having to interpret border colour.
Implementation: extend `notesEditor.View()` and the active pane's render
path to draw the marker conditional on `m.notesPaneFocused`.

### Option C: Disable mouse-driven focus swap; require Tab (Small, UX regression)
Treat clicks in the pane area while notes mode is on as plain pane drag
selection only — do not change `m.notesPaneFocused`. The user must press
`Tab` (or `Shift+Tab`) to swap focus. This eliminates the synthesised-
click attack vector entirely but loses the natural "click to focus"
ergonomics. Worth doing only if option A/B prove insufficient.

### Option D: Audible bell on focus swap (Trivial, intrusive)
Emit a short BEL (`\x07`) on every focus transition. Most terminal
emulators flash the screen or play a system sound. Effective but likely
too intrusive for normal workflow — recommended only for users running
in environments where the synthesised-click risk is realistic.

### Recommendation
Ship **option A** (flash) as the default — it's the least intrusive
high-noticeability cue. Add **option C** as a config flag for users who
want hard isolation (`[notes] strict_focus_mode = true`).
