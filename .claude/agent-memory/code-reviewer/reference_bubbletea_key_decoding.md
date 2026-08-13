---
name: reference-bubbletea-key-decoding
description: Where bubbletea v2 actually decodes key bytes into KeyPressMsg — needed to answer "what does msg.String() render for this physical key"
metadata:
  type: reference
---

Bubble Tea v2 does NOT decode keys itself. The parser lives in
`github.com/charmbracelet/ultraviolet` (module cache: `/go/pkg/mod/github.com/charmbracelet/ultraviolet@*/decoder.go`);
`bubbletea/v2/key.go` only defines the types and `String()`.

Two facts worth having on hand for any keybinding review in this repo:

- **Legacy ESC-prefix Meta** (`decoder.go`, the `default:` arm of the ESC switch):
  `ESC` + byte re-decodes the byte, clears `Text`, and ORs in `ModAlt`. So
  `Option+Shift+M` under macOS "Use Option as Meta" arrives as
  `KeyPressMsg{Code:'M', Mod:ModAlt}` — case in `Code`, NO `ModShift` —
  and `String()` renders `"alt+M"`, not `"alt+shift+m"`.
- **`Text` beats the chord in `String()`**: `{Code:'M', Mod:ModAlt, Text:"M"}`
  renders `"M"`. Test fixtures that set `Text` assert about a different key
  than they look like they do (see `keyPress` in `internal/tui/keydispatch_test.go`,
  which guards against exactly this).

**How to apply:** to settle "does this physical key produce that chord string",
build the `tea.KeyPressMsg` in a throwaway probe and log `String()` — do not
reason from the chord spelling. See [[project-verify-claims-in-container]].
