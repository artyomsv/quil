# x/vt treats byte 0x9C as OSC String Terminator inside UTF-8

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | upstream `github.com/charmbracelet/x/vt` (OSC/string parsing); worked around in `internal/tui/oscfilter.go` |
| Found during | macOS claude-code render corruption ("AAAude Code" input leak + doubled logo) |
| Date | 2026-07-03 |

## Issue

The `charmbracelet/x/vt` emulator (and the `x/ansi` parser under it) terminates an
OSC string when it encounters byte `0x9C` — the C1 "String Terminator" (ST) —
even when that `0x9C` is a UTF-8 continuation byte, not a real control. In a
UTF-8 terminal, raw C1 bytes (`0x80`–`0x9F`) must NOT be treated as controls;
they only occur as parts of multi-byte characters.

Reproduced minimally: `\x1b]0;✳LEAKED\x07` (✳ = U+2733 = `E2 9C B3`) leaks
`LEAKED` into the grid, while the same OSC with any non-`0x9C` char (`▐`, emoji,
`▓`, `♫`) is clean. Confirmed still present on `x/vt@v0.0.0-20260629` /
`ultraviolet@v0.0.0-20260703` (bumping the deps did NOT fix it).

A targeted workaround is in place: `internal/tui/oscfilter.go` (`oscTitleFilter`)
strips OSC 0/1/2 (window/icon title) before the emulator, which is lossless
because Quil renders its own tab titles. This does NOT cover the general case.

## Risks

- **Grid corruption for any non-title OSC that reaches the emulator with a
  `0x9C`-bearing UTF-8 char** — e.g. an OSC 8 hyperlink label or OSC 7 cwd
  containing such a character. Both are rare, but the workaround passes them
  through, so they would still leak their tail into the visible pane.
- **Silent divergence from real terminals**: apps render correctly in a real
  terminal but corrupt inside Quil, which is hard to diagnose (it presents as an
  app bug, not an emulator bug — cost us a long investigation this time).
- The workaround adds a per-pane byte-stream pass that a correct upstream parser
  would make unnecessary.

## Suggested Solutions

1. **Report upstream / fix in `x/ansi`**: the OSC/DCS string collector must not
   treat raw `0x80`–`0x9F` as C1 controls when the parser is in UTF-8 mode
   (matches xterm's UTF-8 behavior). This is the correct, general fix; the Quil
   filter (`oscfilter.go`) can then be removed.
2. If upstream is slow, broaden the workaround to sanitize `0x9C` inside any OSC
   string (not just titles) before feeding the emulator.
