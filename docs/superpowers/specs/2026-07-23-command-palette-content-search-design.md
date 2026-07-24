# Command Palette — Content Search (Design)

**Date:** 2026-07-23
**Status:** Approved (brainstorm)
**Feature branch:** `feature/palette-content-search`
**Builds on:** the command palette shipped in PR #99 (`internal/tui/palette.go`).

## Problem

The command palette (opened with `alt+shift+p`) navigates to panes by *name/type/CWD*, but a
user often remembers a pane by *what is on its screen* — an error string, a URL, a container id —
not by its label. There is no way to ask "which pane has `connection refused` in its scrollback?"
and jump to it. This was the explicitly-deferred Phase-2 item ("`/` content search across pane
buffers") from the original palette work.

## Goal

Add a content-search mode to the existing palette: type a query, get the list of panes whose
scrollback contains it (with a match count and a preview line), and press Enter to navigate to the
matching pane. Reuse the palette's existing navigation, rendering, and dialog plumbing — this is an
additive mode, not a second dialog.

## Non-goals (v1)

- Regex or whitespace-tokenized-AND matching — literal case-insensitive substring only.
- Per-line navigation / scroll-to-match — the palette navigates to a *pane*, not a scroll offset.
- An MCP `search_panes` tool — clean follow-up, out of scope here.
- Searching dormant panes by spawning them — search only what is already buffered.
- Persistence — search state is transient, never written to disk.

## Key architectural fact

Pane scrollback lives in the **daemon** (`Pane.OutputBuf` ring buffer, plus `Pane.GhostSnap` for
restored panes loaded into `OutputBuf` at restore time). The palette is a **client-side** dialog
that holds only pane *metadata* (built from `workspace_state` broadcasts). Therefore content search
cannot run locally — it requires a daemon round-trip, mirroring how `handleReadPaneOutputReq`
already does `OutputBuf.Bytes()` → `ansi.Strip`.

Rejected alternatives:
- **Ship all content to the TUI, search locally** — N panes × up to `bufSize` (256 KB) per query,
  plus a stale cache duplicating the daemon's authoritative buffers.
- **Search only the `PaneModel`s the TUI already rendered** — misses pending / never-rendered panes
  and defeats the "all panes" promise.

## Design

### 1. Entry & mode

The palette gains a **content-search mode**, entered by a leading `/` in the query:

- Query `"/connection refused"` → content mode, search term = `connection refused`.
- Any query **without** a leading `/` stays in normal command mode (unchanged behavior).
- Header switches to `Search pane content ›` (from the command `> ` prompt) so the mode is
  unmistakable.
- Backspacing past the `/` (empty query) returns to command mode; `esc` closes the palette as today.

No new keybinding — the palette open key (`alt+shift+p`) and all existing behavior (centering,
notes-mode no-op, paste-into-query sanitization) are unchanged.

### 2. IPC (`internal/ipc/protocol.go`)

New async request/response pair:

```go
MsgPaneSearchReq  = "pane_search_req"
MsgPaneSearchResp = "pane_search_resp"

type PaneSearchReqPayload struct {
    Query string `json:"query"`
}

type PaneSearchHit struct {
    PaneID  string `json:"pane_id"`
    Matches int    `json:"matches"`
    Excerpt string `json:"excerpt"` // most-recent matching line, whitespace-collapsed, capped
}

type PaneSearchRespPayload struct {
    Query     string          `json:"query"`               // echoed for staleness check
    Hits      []PaneSearchHit `json:"hits"`                // sorted: matches desc, then pane_id
    Truncated bool            `json:"truncated,omitempty"` // a pane hit the per-pane match cap
}
```

The response carries **only `pane_id` + `matches` + `excerpt`**. The TUI already knows every pane's
`tab.pane · type · name · cwd` label (it builds the Go-to-pane rows from that same data), so it
resolves the display label locally by pane id — the daemon does no label enrichment.

**Staleness:** search fires as the user types, so responses can arrive out of order. The response
echoes `Query`; the TUI applies a response only if its echoed query still equals the current search
term. No request-counter state is needed.

### 3. Daemon handler (`internal/daemon/search.go`, new file)

`handlePaneSearchReq(conn, msg)`:

1. Decode payload; `strings.TrimSpace(query)`. Empty → respond with empty hits (echoing the query).
2. Enumerate every pane across all tabs (same iteration `buildPaneInfos` uses).
3. Per pane (guard nil `OutputBuf`):
   - `stripped := ansi.Strip(string(pane.OutputBuf.Bytes()))`
   - Split into lines; for each line `strings.Contains(strings.ToLower(line), lowerTerm)`.
   - Count total matches; keep the **last** matching line (most-recent output) as the excerpt.
   - Stop counting at a per-pane cap (`maxPaneMatches = 1000`) → set `Truncated`.
4. Excerpt post-processing: collapse runs of whitespace to single spaces, trim, cap to
   `maxExcerptCells` (≈160) with the shared cell-aware truncation.
5. **Never call `ensurePaneSpawned`** (unlike `read_pane_output`). A dormant/pending pane's restored
   ghost is already in `OutputBuf`; a never-populated pane simply yields no hits.
6. Sort hits: `Matches` desc, then `PaneID` for stability.
7. `respondTo(conn, msg.ID, ipc.MsgPaneSearchResp, payload)` — unicasts to the requesting TUI conn
   (works with an empty `Message.ID`, same as the pane-history handlers).

Dispatch case added in `daemon.go` next to `case ipc.MsgReadPaneOutputReq`.

**Locking:** `OutputBuf.Bytes()` returns a copy under the ring buffer's own guard. Muted panes are
searched (mute governs notifications, not visibility). No `PluginMu` label reads are needed because
the daemon returns no labels.

### 4. Palette UX (`internal/tui/palette_search.go`, new file)

Keeps `palette.go` focused; the content-search additions live in their own file.

`paletteState` gains:
- `mode paletteMode` (command | content),
- `term string` (the query minus the leading `/`),
- `hits []paletteHit` (resolved label + detail + count + excerpt),
- `searching bool` (a request is in flight and no fresh response has landed yet).

`paletteHit` is the TUI-side, label-resolved form of `PaneSearchHit`:
```go
type paletteHit struct {
    paneID  string
    label   string // "2.1 · claude-code · myproj", resolved locally by paneID
    detail  string // "3×" (or "3× (capped)")
    excerpt string
}
```

Content-mode render is its own path (does not reuse the `paletteCommand` row list):
- Header row: `Search pane content ›` + term + caret.
- One **two-line entry per pane**: a selectable **label row** (`2.1 · claude-code · myproj   3×`)
  followed by a **non-selectable dim excerpt row**. The excerpt row is skipped by the cursor using
  the same non-selectable machinery headers use, so `↑↓` lands only on label rows.
- Scroll window / `paletteVisibleRows` accounting treats each entry as its label row for cursor math.
- All rows clamped with the existing cell-aware `truncateToWidth` / `lastCellsToWidth`, so wide
  glyphs and long excerpts never wrap the dialog border.

Empty/transition states:
- Term empty (`/` only) → `Type to search across all panes`.
- Request in flight, no hits yet → `Searching…`.
- Response landed, zero hits → `No matches in any pane`.

`Enter` on a hit navigates via the **existing** go-to-pane logic. The go-to-pane block currently
inlined in `executePaletteCommand`'s `palActGoToPane` case is extracted into a shared
`goToPane(paneID) (tea.Model, tea.Cmd)` helper that both the command dispatcher and the content-mode
Enter handler call — one navigation path, not two.

### 5. Debounce + wiring (`internal/tui/model.go`)

- On a content-mode query change, `handleCommandPaletteKey` returns
  `tea.Tick(150ms) → paletteSearchDebounceMsg{term}`. When it fires and `term` still equals the
  current search term, it sends `MsgPaneSearchReq` (fire-and-forget via `m.client.Send`) and sets
  `searching = true`. This coalesces keystrokes so we do not fire a request per character.
- The response arrives through the existing `listenForMessages` loop as `paneSearchRespMsg`. Its
  handler: drop the response if `resp.Query != m.palette.term` (stale); otherwise resolve each
  `PaneSearchHit.PaneID` → label via the existing pane lookup, populate `hits`, clear `searching`,
  reset the cursor to the first selectable row, and **re-arm `m.listenForMessages()`** (the
  mandatory pattern every response branch follows, or the listen loop dies).
- `tea.PasteMsg` already has a `dialogCommandPalette` branch that folds pasted text into the query
  via `sanitizePaletteQuery`; a paste while in content mode extends the term the same way and
  triggers the same debounce.

Nothing is broadcast; nothing is persisted.

### 6. Testing

- **ipc (`protocol_test.go`):** round-trip `PaneSearchReqPayload` / `PaneSearchRespPayload`; add the
  two message-type constants to the known-types list.
- **daemon (`search_test.go`):** table-driven against fake panes with known `OutputBuf` content —
  expected match counts and excerpts; empty/whitespace query → empty hits; no-match; per-pane cap →
  `Truncated`; nil `OutputBuf` skipped; case-insensitivity; ANSI-laden content stripped before match;
  excerpt is the last matching line, whitespace-collapsed and capped.
- **tui:**
  - `/`-prefix parsing → content mode + correct term extraction; backspace-to-empty → command mode.
  - stale-echo response (`resp.Query` != current term) is dropped, hits unchanged.
  - content-hit render width-safety (narrow terminal + wide-glyph pane name + long excerpt) — every
    rendered line ≤ `paletteInnerWidth`, mirroring the existing palette render tests.
  - cursor skips excerpt rows (lands only on label rows).
  - `Enter` on a hit → `goToPane` switches to the correct tab and activates the pane.
  - empty-term / searching / no-hit state strings render.

## Files touched

| File | Change |
|---|---|
| `internal/ipc/protocol.go` | +2 message types, +3 payload structs |
| `internal/ipc/protocol_test.go` | round-trip + known-types |
| `internal/daemon/search.go` | **new** — `handlePaneSearchReq` + helpers |
| `internal/daemon/search_test.go` | **new** — handler tests |
| `internal/daemon/daemon.go` | +1 dispatch case |
| `internal/tui/palette.go` | extract `goToPane` helper; `paletteState` fields; route content mode |
| `internal/tui/palette_search.go` | **new** — content mode: parse, request, render, navigate |
| `internal/tui/palette_search_test.go` | **new** — content-mode tests |
| `internal/tui/model.go` | debounce tick msg + `paneSearchRespMsg` listen branch |
| `docs/keybindings.md`, `docs/features.md`, `docs/roadmap/command-palette.md`, `CHANGELOG.md`, `.claude/CLAUDE.md` | document the `/` content-search mode |

## Success check

Build (`./scripts/dev.sh build`) + `./scripts/dev.sh test` green (new ipc/daemon/tui tests included),
`vet` clean. Manual, in a **dev** instance: open the palette, type `/` + a string known to be in a
background pane's scrollback, see that pane listed with a match count and preview line, press Enter,
and land on it with the term visible on screen.
