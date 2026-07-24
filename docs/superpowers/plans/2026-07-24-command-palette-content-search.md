# Command Palette Content Search — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/`-prefixed content-search mode to the command palette that lists panes whose scrollback contains a literal query and navigates to the chosen pane.

**Architecture:** Pane scrollback lives in the daemon (`Pane.OutputBuf`); the palette is a client-side dialog. A new async IPC pair (`pane_search_req`/`pane_search_resp`) lets the daemon scan every pane's buffer and return per-pane hit counts + a preview excerpt. The TUI debounces keystrokes, drops stale responses by echoed query, resolves pane labels locally, and reuses the existing go-to-pane navigation.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2, length-prefixed JSON IPC, `charmbracelet/x/ansi` for ANSI stripping.

## Global Constraints

- Module path `github.com/artyomsv/quil`; Go 1.25; tabs for Go indentation (`gofmt`).
- Build/test only via Docker (Go/make are not on the host). `./scripts/dev.sh test` and `./scripts/dev.sh vet` WORK. `./scripts/dev.sh build`/`cross` are currently CRLF-broken in the container — do not rely on them; for targeted runs use:
  `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/<pkg>/ -run <Name>`
- Development runs against **dev mode only** (`./quil-dev.exe` / `.quil/` at project root). NEVER touch production `~/.quil/`, its daemon, socket, or the `kill-daemon`/`reset-daemon` scripts.
- Interfaces use `interface` for object shapes only where the codebase already does; follow existing patterns. Acronyms stay uppercase (`IPC`, `PTY`, `CWD`).
- Commits: imperative mood, ≤72-char subject. NO AI/Claude/Anthropic attribution, NO `Co-Authored-By` trailers. Stage files by explicit path — never `git add -A`.
- Do NOT commit the spec/plan docs under `docs/superpowers/` as their own commits (working artifacts). Feature code + user-facing docs commits are fine.
- Literal, case-insensitive substring matching only. Never spawn a dormant pane to search it. Nothing is persisted or broadcast.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/ipc/protocol.go` | +2 message-type constants, +3 payload structs |
| `internal/ipc/protocol_test.go` | known-types entry + payload round-trip |
| `internal/daemon/search.go` (**new**) | `scanPaneMatches` (pure), `Daemon.searchPanes`, `handlePaneSearchReq` |
| `internal/daemon/search_test.go` (**new**) | tests for the above |
| `internal/daemon/daemon.go` | +1 dispatch case near `MsgReadPaneOutputReq` |
| `internal/tui/palette.go` | `paletteState` fields; `paletteMode`; extract `goToPane`; route content render; content keys |
| `internal/tui/palette_search.go` (**new**) | parse `/`, request/debounce cmds, `applyPaneSearch`, content render, label helper |
| `internal/tui/palette_search_test.go` (**new**) | parse, stale-drop, label resolve, render width, navigation |
| `internal/tui/model.go` | `paletteSearchDebounceMsg` + `paneSearchRespMsg` handling; listen dispatch |
| `docs/keybindings.md`, `docs/features.md`, `docs/roadmap/command-palette.md`, `CHANGELOG.md`, `.claude/CLAUDE.md` | document `/` content-search |

---

## Task 1: IPC protocol — message types + payloads

**Files:**
- Modify: `internal/ipc/protocol.go` (constant block near `MsgPaneHistoryEntryResp:91`; payload structs after the pane-history payloads)
- Test: `internal/ipc/protocol_test.go` (`TestMessageTypes:61`)

**Interfaces:**
- Produces: `ipc.MsgPaneSearchReq`, `ipc.MsgPaneSearchResp` (string consts); `ipc.PaneSearchReqPayload{Query string}`; `ipc.PaneSearchHit{PaneID string; Matches int; Excerpt string}`; `ipc.PaneSearchRespPayload{Query string; Hits []PaneSearchHit; Truncated bool}`.

- [ ] **Step 1: Write the failing test**

In `internal/ipc/protocol_test.go`, add the two constants to the `types` slice in `TestMessageTypes` (after `ipc.MsgPaneHistoryEntryResp,`):

```go
		ipc.MsgPaneSearchReq,
		ipc.MsgPaneSearchResp,
```

Then add a round-trip test:

```go
func TestPaneSearchPayload_RoundTrip(t *testing.T) {
	req := PaneSearchReqPayload{Query: "connection refused"}
	msg, err := NewMessage(MsgPaneSearchReq, req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	var gotReq PaneSearchReqPayload
	if err := msg.DecodePayload(&gotReq); err != nil {
		t.Fatalf("decode req: %v", err)
	}
	if gotReq.Query != req.Query {
		t.Errorf("query = %q, want %q", gotReq.Query, req.Query)
	}

	resp := PaneSearchRespPayload{
		Query:     "refused",
		Hits:      []PaneSearchHit{{PaneID: "p1", Matches: 3, Excerpt: "connection refused"}},
		Truncated: true,
	}
	msg2, err := NewMessage(MsgPaneSearchResp, resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var gotResp PaneSearchRespPayload
	if err := msg2.DecodePayload(&gotResp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if len(gotResp.Hits) != 1 || gotResp.Hits[0].Matches != 3 || !gotResp.Truncated {
		t.Errorf("resp round-trip mismatch: %+v", gotResp)
	}
}
```

> Note: `protocol_test.go` is `package ipc` (white-box) — it refers to `PaneSearchReqPayload` unqualified but `ipc.MsgPaneSearchReq` in the `TestMessageTypes` list. Match the file's existing style (it already mixes both).

- [ ] **Step 2: Run test to verify it fails**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/ipc/ 2>&1 | tail -20`
Expected: FAIL — `undefined: ipc.MsgPaneSearchReq` / `undefined: PaneSearchReqPayload`.

- [ ] **Step 3: Write minimal implementation**

In `internal/ipc/protocol.go`, add to the message-type const block (right after `MsgPaneHistoryEntryResp = "pane_history_entry_resp"`):

```go
	MsgPaneSearchReq  = "pane_search_req"
	MsgPaneSearchResp = "pane_search_resp"
```

After the pane-history payload structs, add:

```go
// PaneSearchReqPayload asks the daemon to scan every pane's scrollback for a
// literal, case-insensitive substring. Query is the raw search term (no leading
// slash — the TUI strips the "/" mode sigil before sending).
type PaneSearchReqPayload struct {
	Query string `json:"query"`
}

// PaneSearchHit is one matching pane. The TUI resolves the display label itself
// from PaneID (it already holds tab/pane metadata), so the daemon returns only
// the id, the total match count, and a single preview line.
type PaneSearchHit struct {
	PaneID  string `json:"pane_id"`
	Matches int    `json:"matches"`
	Excerpt string `json:"excerpt"`
}

// PaneSearchRespPayload carries the hits for one search. Query echoes the
// request term so the TUI can drop responses that arrived after the user typed
// more (stale). Truncated is set when any pane hit the per-pane match cap.
type PaneSearchRespPayload struct {
	Query     string          `json:"query"`
	Hits      []PaneSearchHit `json:"hits"`
	Truncated bool            `json:"truncated,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/ipc/ 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/protocol.go internal/ipc/protocol_test.go
git commit -m "feat(ipc): add pane_search request/response messages"
```

---

## Task 2: Daemon search core — `scanPaneMatches` (pure)

**Files:**
- Create: `internal/daemon/search.go`
- Test: `internal/daemon/search_test.go`

**Interfaces:**
- Consumes: `charmbracelet/x/ansi` (already a dependency — see `handleReadPaneOutputReq` importing `ansi`).
- Produces: `func scanPaneMatches(raw []byte, lowerTerm string) (matches int, excerpt string, truncated bool)` — strips ANSI, counts lines containing `lowerTerm` (caller lower-cases the term once), keeps the LAST matching line as the excerpt (whitespace-collapsed, capped), stops counting at `maxPaneMatches`. Package consts `maxPaneMatches = 1000`, `maxExcerptCells = 160`.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/search_test.go`:

```go
package daemon

import "testing"

func TestScanPaneMatches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		lowerTerm string
		wantN     int
		wantExc   string
		wantTrunc bool
	}{
		{"no match", "hello world\nfoo bar\n", "zzz", 0, "", false},
		{"single", "alpha\nconnection refused\nbeta\n", "refused", 1, "connection refused", false},
		{"case insensitive", "ERROR: Connection Refused now\n", "refused", 1, "ERROR: Connection Refused now", false},
		{"excerpt is last match", "err one\nmid\nerr two\n", "err", 2, "err two", false},
		{"whitespace collapsed", "a\t\t err   here \n", "err", 1, "a err here", false},
		{"ansi stripped", "\x1b[31mred error\x1b[0m line\n", "error", 1, "red error line", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, exc, trunc := scanPaneMatches([]byte(tc.raw), tc.lowerTerm)
			if n != tc.wantN {
				t.Errorf("matches = %d, want %d", n, tc.wantN)
			}
			if exc != tc.wantExc {
				t.Errorf("excerpt = %q, want %q", exc, tc.wantExc)
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated = %v, want %v", trunc, tc.wantTrunc)
			}
		})
	}
}

func TestScanPaneMatches_Cap(t *testing.T) {
	var sb []byte
	for i := 0; i < maxPaneMatches+50; i++ {
		sb = append(sb, []byte("needle line\n")...)
	}
	n, _, trunc := scanPaneMatches(sb, "needle")
	if n != maxPaneMatches || !trunc {
		t.Errorf("cap: matches=%d truncated=%v, want %d,true", n, trunc, maxPaneMatches)
	}
}

func TestSearchPanes_AcrossTabs(t *testing.T) {
	d := newTestDaemon(t)
	mkPane := func(id, tabID, content string) *Pane {
		p := &Pane{ID: id, TabID: tabID, Type: "terminal", OutputBuf: ringbuf.NewRingBuffer(8192)}
		p.OutputBuf.Write([]byte(content))
		return p
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}},
		[]*Pane{mkPane("pane-0000000a", "tab-0000000a", "boot ok\nconnection refused twice\nconnection refused\n")},
	)
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000b", Name: "B", Panes: []string{"pane-0000000b"}},
		[]*Pane{mkPane("pane-0000000b", "tab-0000000b", "all good here\n")},
	)

	hits, trunc := d.searchPanes("refused")
	if trunc {
		t.Errorf("truncated = true, want false")
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].PaneID != "pane-0000000a" || hits[0].Matches != 2 {
		t.Errorf("hit = %+v, want pane-0000000a x2", hits[0])
	}
	if hits[0].Excerpt != "connection refused" {
		t.Errorf("excerpt = %q, want last match", hits[0].Excerpt)
	}
}

func TestSearchPanes_EmptyQuery(t *testing.T) {
	d := newTestDaemon(t)
	if hits, _ := d.searchPanes("   "); hits != nil {
		t.Errorf("blank query should yield no hits, got %+v", hits)
	}
}

func TestSearchPanes_SkipsNilBuffer(t *testing.T) {
	d := newTestDaemon(t)
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000c", Name: "C", Panes: []string{"pane-0000000c"}},
		[]*Pane{{ID: "pane-0000000c", TabID: "tab-0000000c", Type: "terminal"}}, // no OutputBuf
	)
	if hits, _ := d.searchPanes("anything"); len(hits) != 0 {
		t.Errorf("nil OutputBuf must be skipped, got %+v", hits)
	}
}
```

Test file imports: `testing`, plus `"github.com/artyomsv/quil/internal/ringbuf"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/daemon/ -run 'TestScanPaneMatches|TestSearchPanes' 2>&1 | tail -20`
Expected: FAIL — `undefined: scanPaneMatches`, `d.searchPanes undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/daemon/search.go`:

```go
package daemon

import (
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/ipc"
)

const (
	// maxPaneMatches bounds per-pane counting so a huge buffer full of the term
	// cannot make one search walk unboundedly; hitting it sets Truncated.
	maxPaneMatches = 1000
	// maxExcerptCells caps the preview line width (display cells, ASCII-safe here
	// since we only rune-count the collapsed line's length as a coarse bound).
	maxExcerptCells = 160
)

// scanPaneMatches strips ANSI from raw pane output, counts lines that contain
// lowerTerm (case-insensitive; caller pre-lowers the term), and returns the LAST
// matching line as a whitespace-collapsed, length-capped excerpt. matches stops
// accumulating at maxPaneMatches, in which case truncated is true. An empty
// lowerTerm yields (0, "", false).
func scanPaneMatches(raw []byte, lowerTerm string) (matches int, excerpt string, truncated bool) {
	if lowerTerm == "" || len(raw) == 0 {
		return 0, "", false
	}
	stripped := ansi.Strip(string(raw))
	var lastLine string
	for _, line := range strings.Split(stripped, "\n") {
		if strings.Contains(strings.ToLower(line), lowerTerm) {
			matches++
			lastLine = line
			if matches >= maxPaneMatches {
				truncated = true
				break
			}
		}
	}
	if matches == 0 {
		return 0, "", false
	}
	return matches, collapseExcerpt(lastLine), truncated
}

// collapseExcerpt trims a preview line, collapses internal whitespace runs to
// single spaces, and caps its rune length with an ellipsis.
func collapseExcerpt(line string) string {
	collapsed := strings.Join(strings.Fields(line), " ")
	runes := []rune(collapsed)
	if len(runes) > maxExcerptCells {
		return string(runes[:maxExcerptCells-1]) + "…"
	}
	return collapsed
}

// searchPanes scans every pane's loaded OutputBuf across all tabs for term and
// returns hits sorted by match count (desc), then pane id. It never spawns a
// dormant pane — only already-buffered content is searched. truncated is true
// if any pane hit maxPaneMatches.
func (d *Daemon) searchPanes(term string) (hits []ipc.PaneSearchHit, truncated bool) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, false
	}
	lower := strings.ToLower(term)
	_, tabs, panesByTab := d.session.SnapshotState()
	for _, tab := range tabs {
		for _, pane := range panesByTab[tab.ID] {
			if pane.OutputBuf == nil {
				continue
			}
			n, excerpt, trunc := scanPaneMatches(pane.OutputBuf.Bytes(), lower)
			if n == 0 {
				continue
			}
			if trunc {
				truncated = true
			}
			hits = append(hits, ipc.PaneSearchHit{PaneID: pane.ID, Matches: n, Excerpt: excerpt})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Matches != hits[j].Matches {
			return hits[i].Matches > hits[j].Matches
		}
		return hits[i].PaneID < hits[j].PaneID
	})
	return hits, truncated
}
```

> Verify the ANSI import path: `handleReadPaneOutputReq` in `daemon.go` already imports `ansi` — copy its exact import line (`github.com/charmbracelet/x/ansi`). If the file's existing alias differs, match it.

- [ ] **Step 4: Run test to verify it passes**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/daemon/ -run 'TestScanPaneMatches|TestSearchPanes' 2>&1 | tail -20`
Expected: PASS (all five: `TestScanPaneMatches`, `_Cap`, `TestSearchPanes_AcrossTabs`, `_EmptyQuery`, `_SkipsNilBuffer`).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/search.go internal/daemon/search_test.go
git commit -m "feat(daemon): add pane scrollback scan + searchPanes"
```

---

## Task 3: Daemon handler + dispatch

**Files:**
- Modify: `internal/daemon/search.go` (add `handlePaneSearchReq`)
- Modify: `internal/daemon/daemon.go` (dispatch case near `case ipc.MsgReadPaneOutputReq:` ~`:801`)
- Test: `internal/daemon/search_test.go` (add `TestSearchPanes_*`)

**Interfaces:**
- Consumes: `Daemon.searchPanes`, `respondTo`, `d.session.RestoreTab`, `ringbuf.NewRingBuffer`.
- Produces: `func (d *Daemon) handlePaneSearchReq(conn *ipc.Conn, msg *ipc.Message)`.

> `searchPanes`'s behavior is already covered by the tests added in Task 2
> (`TestSearchPanes_AcrossTabs` / `_EmptyQuery` / `_SkipsNilBuffer`). This task
> adds only the IPC wire path on top of it: the handler and the dispatch case.
> Its verification is the build + the full daemon suite staying green.

- [ ] **Step 1: Add the handler**

Append to `internal/daemon/search.go`:

```go
// handlePaneSearchReq answers a content search: scan all panes, return hits
// (unicast to the requesting conn). Never spawns panes; muted panes are
// included (mute governs notifications, not search).
func (d *Daemon) handlePaneSearchReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.PaneSearchReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handlePaneSearchReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgPaneSearchResp, ipc.PaneSearchRespPayload{})
		return
	}
	hits, truncated := d.searchPanes(req.Query)
	respondTo(conn, msg.ID, ipc.MsgPaneSearchResp, ipc.PaneSearchRespPayload{
		Query:     strings.TrimSpace(req.Query),
		Hits:      hits,
		Truncated: truncated,
	})
}
```

Add `"log"` to `search.go`'s import block (alongside `sort`, `strings`).

- [ ] **Step 2: Add the dispatch case**

In `internal/daemon/daemon.go`, immediately after the `case ipc.MsgReadPaneOutputReq:` block (~`:801`), add:

```go
	case ipc.MsgPaneSearchReq:
		d.handlePaneSearchReq(conn, msg)
```

- [ ] **Step 3: Verify it builds + tests pass**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine sh -c "go vet ./internal/daemon/ && go test ./internal/daemon/ 2>&1 | tail -20"`
Expected: vet clean; the FULL daemon suite PASSES (the handler must not regress existing tests).

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/search.go internal/daemon/search_test.go internal/daemon/daemon.go
git commit -m "feat(daemon): handle pane_search requests"
```

---

## Task 4: TUI — palette state + `/` parse (pure)

**Files:**
- Modify: `internal/tui/palette.go` (`paletteState` struct `:23`)
- Create: `internal/tui/palette_search.go`
- Test: `internal/tui/palette_search_test.go`

**Interfaces:**
- Produces: `type paletteMode int` with `paletteModeCommand`, `paletteModeContent`; `type paletteHit struct{ paneID, label, detail, excerpt string }`; new `paletteState` fields `mode paletteMode`, `term string`, `hits []paletteHit`, `searching bool`, `truncated bool`; `func parsePaletteQuery(query string) (paletteMode, string)`.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/palette_search_test.go`:

```go
package tui

import "testing"

func TestParsePaletteQuery(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantMode paletteMode
		wantTerm string
	}{
		{"", paletteModeCommand, ""},
		{"close", paletteModeCommand, "close"},
		{"/", paletteModeContent, ""},
		{"/refused", paletteModeContent, "refused"},
		{"/ two words", paletteModeContent, " two words"},
	} {
		mode, term := parsePaletteQuery(tc.in)
		if mode != tc.wantMode || term != tc.wantTerm {
			t.Errorf("parse(%q) = (%v,%q), want (%v,%q)", tc.in, mode, term, tc.wantMode, tc.wantTerm)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestParsePaletteQuery 2>&1 | tail -20`
Expected: FAIL — `undefined: parsePaletteQuery` / `undefined: paletteModeCommand`.

- [ ] **Step 3: Write minimal implementation**

Add the new fields to `paletteState` in `internal/tui/palette.go` (extend the struct — keep existing fields):

```go
type paletteState struct {
	query    string
	cursor   int
	commands []paletteCommand
	filtered []paletteCommand

	// Content-search mode (entered by a leading "/").
	mode      paletteMode
	term      string       // query minus the "/" sigil
	hits      []paletteHit // resolved matches, daemon-sorted
	searching bool         // a request is in flight, no fresh response yet
	truncated bool         // some pane hit the per-pane match cap
}
```

Create `internal/tui/palette_search.go` (start it — later tasks append):

```go
package tui

import "strings"

// paletteMode selects how the palette interprets its query.
type paletteMode int

const (
	paletteModeCommand paletteMode = iota // fuzzy command list (default)
	paletteModeContent                    // literal search across pane scrollback ("/")
)

// paletteHit is the TUI-side, label-resolved form of an ipc.PaneSearchHit. The
// daemon returns only paneID+count+excerpt; label/detail are resolved locally.
type paletteHit struct {
	paneID  string
	label   string // "2.1 · claude-code · myproj"
	detail  string // "3×" or "3× capped"
	excerpt string
}

// parsePaletteQuery classifies a raw query. A leading "/" switches to content
// mode with the remainder as the search term (leading slash consumed, nothing
// else trimmed — the term may intentionally start with a space). Anything else
// is command mode with the query verbatim.
func parsePaletteQuery(query string) (paletteMode, string) {
	if strings.HasPrefix(query, "/") {
		return paletteModeContent, query[1:]
	}
	return paletteModeCommand, query
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestParsePaletteQuery 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_search.go internal/tui/palette_search_test.go
git commit -m "feat(tui): palette content-mode state + query parse"
```

---

## Task 5: TUI — label resolution + request/response plumbing

**Files:**
- Modify: `internal/tui/palette.go` (extract label format used by `buildPaletteCommands` go-to loop)
- Modify: `internal/tui/palette_search.go` (`paneNavLabel`, `requestPaneSearch`, `paletteSearchDebounce`, `applyPaneSearch`)
- Modify: `internal/tui/model.go` (`listenForMessages` dispatch `:3683`; `Update` near `case memoryReportMsg:` `:1175`; message types)
- Test: `internal/tui/palette_search_test.go`

**Interfaces:**
- Consumes: `m.findPaneAndTab` (`internal/tui/workstate.go:39`), `m.tabs`, `TabModel.Leaves()`, `m.client.Send`, `m.listenForMessages`, `ipc.PaneSearchRespPayload`.
- Produces:
  - `func formatPaneNav(tabIdx, paneIdx int, p *PaneModel) string` — the shared `"i.j · type[· name]"` label.
  - `func (m *Model) paneNavLabel(paneID string) (label, cwd string, ok bool)`.
  - `func (m Model) requestPaneSearch(term string) tea.Cmd`.
  - `func paletteSearchDebounce(term string) tea.Cmd`.
  - `func (m Model) applyPaneSearch(resp ipc.PaneSearchRespPayload) Model`.
  - `type paletteSearchDebounceMsg struct{ term string }`; `type paneSearchRespMsg struct{ Resp ipc.PaneSearchRespPayload }`.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/palette_search_test.go` (imports: add `"github.com/artyomsv/quil/internal/ipc"`):

```go
func TestApplyPaneSearch_ResolvesLabels(t *testing.T) {
	m := newSplitDragTestModel(t) // panes p1, p2 on tab 0
	m.palette.query = "/p"
	m.palette.mode = paletteModeContent
	m.palette.term = "p"
	m.palette.searching = true

	resp := ipc.PaneSearchRespPayload{
		Query: "p",
		Hits: []ipc.PaneSearchHit{
			{PaneID: "p1", Matches: 3, Excerpt: "prompt here"},
			{PaneID: "p2", Matches: 1, Excerpt: "another"},
		},
	}
	m2 := m.applyPaneSearch(resp)
	if m2.palette.searching {
		t.Error("searching should clear after a response")
	}
	if len(m2.palette.hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(m2.palette.hits))
	}
	if m2.palette.hits[0].paneID != "p1" || m2.palette.hits[0].detail != "3×" {
		t.Errorf("hit0 = %+v", m2.palette.hits[0])
	}
	if !strings.Contains(m2.palette.hits[0].label, "p1") && !strings.Contains(m2.palette.hits[0].label, "1.1") {
		t.Errorf("label should identify the pane: %q", m2.palette.hits[0].label)
	}
}

func TestApplyPaneSearch_DropsStale(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.palette.mode = paletteModeContent
	m.palette.term = "current"
	m.palette.hits = []paletteHit{{paneID: "p1", label: "old"}}

	stale := ipc.PaneSearchRespPayload{Query: "old-term", Hits: []ipc.PaneSearchHit{{PaneID: "p2", Matches: 9}}}
	m2 := m.applyPaneSearch(stale)
	if len(m2.palette.hits) != 1 || m2.palette.hits[0].paneID != "p1" {
		t.Errorf("stale response must not replace hits, got %+v", m2.palette.hits)
	}
}
```

(Add `"strings"` to the test file imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestApplyPaneSearch 2>&1 | tail -20`
Expected: FAIL — `undefined: applyPaneSearch`.

- [ ] **Step 3: Extract the shared label format**

In `internal/tui/palette.go`, replace the inline label build inside `buildPaletteCommands`'s go-to loop (the `parts := []string{...}; ... label: strings.Join(parts, " · ")`) with a call to a new helper. Add this helper (near `tabIndexName`):

```go
// formatPaneNav renders the shared palette navigation label for a pane:
// "i.j · type[· name]" with 1-based tab/pane indices. Empty type falls back to
// "terminal" (the daemon default); an unnamed pane omits the trailing segment
// so there is never a dangling separator.
func formatPaneNav(tabIdx, paneIdx int, p *PaneModel) string {
	paneType := p.Type
	if paneType == "" {
		paneType = "terminal"
	}
	parts := []string{fmt.Sprintf("%d.%d", tabIdx+1, paneIdx+1), paneType}
	if p.Name != "" {
		parts = append(parts, p.Name)
	}
	return strings.Join(parts, " · ")
}
```

Then in the go-to loop, set `label: formatPaneNav(i, j, p),` (keep the existing `detail`, `keywords`, `arg`, `action`, `enabled`). This keeps `TestBuildPaletteCommands_NavigationAndGates` green.

- [ ] **Step 4: Add label resolver + request/debounce/apply**

Append to `internal/tui/palette_search.go` (extend imports: `"fmt"`, `"log"`, `"os"`, `"time"`, `tea "charm.land/bubbletea/v2"`, `"github.com/artyomsv/quil/internal/ipc"`):

```go
// paneNavLabel resolves a pane id to its navigation label and short CWD, or
// ok=false if the pane is gone. Iterates tabs/leaves so it can compute the
// same 1-based i.j indices formatPaneNav uses.
func (m *Model) paneNavLabel(paneID string) (label, cwd string, ok bool) {
	home, _ := os.UserHomeDir()
	for i, tab := range m.tabs {
		if tab == nil {
			continue
		}
		for j, p := range tab.Leaves() {
			if p != nil && p.ID == paneID {
				return formatPaneNav(i, j, p), shortCWD(p.CWD, home), true
			}
		}
	}
	return "", "", false
}

// requestPaneSearch fires MsgPaneSearchReq for term (fire-and-forget); the
// response arrives via listenForMessages → paneSearchRespMsg. Mirrors
// requestHistory.
func (m Model) requestPaneSearch(term string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneSearchReq, ipc.PaneSearchReqPayload{Query: term})
		if err != nil {
			log.Printf("requestPaneSearch: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("search-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestPaneSearch: send: %v", err)
		}
		return nil
	}
}

// paletteSearchDebounce schedules a debounce tick; when it fires the Update
// handler checks the term is still current before issuing the request. 150ms
// coalesces keystrokes so we do not search on every character.
func paletteSearchDebounce(term string) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return paletteSearchDebounceMsg{term: term}
	})
}

// applyPaneSearch stores a fresh result set, resolving each hit's label locally.
// Stale responses (echoed Query != current term) are ignored — the same guard
// applyHistoryList uses against the active dialog.
func (m Model) applyPaneSearch(resp ipc.PaneSearchRespPayload) Model {
	if m.palette.mode != paletteModeContent || resp.Query != m.palette.term {
		return m
	}
	hits := make([]paletteHit, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		label, _, ok := m.paneNavLabel(h.PaneID)
		if !ok {
			continue // pane vanished since the daemon scanned
		}
		detail := fmt.Sprintf("%d×", h.Matches)
		if resp.Truncated {
			detail += " capped"
		}
		hits = append(hits, paletteHit{paneID: h.PaneID, label: label, detail: detail, excerpt: h.Excerpt})
	}
	m.palette.hits = hits
	m.palette.searching = false
	m.palette.truncated = resp.Truncated
	if m.palette.cursor >= len(hits) {
		m.palette.cursor = len(hits) - 1
	}
	if m.palette.cursor < 0 {
		m.palette.cursor = 0
	}
	return m
}
```

Add the message types (put them in `palette_search.go` below the imports):

```go
// paletteSearchDebounceMsg fires after the keystroke debounce; term is the
// search term captured when the tick was scheduled.
type paletteSearchDebounceMsg struct{ term string }

// paneSearchRespMsg carries a daemon content-search response into Update.
type paneSearchRespMsg struct{ Resp ipc.PaneSearchRespPayload }
```

- [ ] **Step 5: Wire listen dispatch + Update**

In `internal/tui/model.go` `listenForMessages` (after the `case ipc.MsgPaneHistoryEntryResp:` block ~`:3689`), add:

```go
		case ipc.MsgPaneSearchResp:
			var payload ipc.PaneSearchRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode pane_search_resp: %v", err)
				return listenContinueMsg{}
			}
			return paneSearchRespMsg{Resp: payload}
```

In `Update` (near `case memoryReportMsg:` ~`:1175`, each such case ends `return m, m.listenForMessages()` or similar), add:

```go
	case paletteSearchDebounceMsg:
		// Only fire if still open in content mode on the same term.
		if m.dialog == dialogCommandPalette && m.palette.mode == paletteModeContent && m.palette.term == msg.term && strings.TrimSpace(msg.term) != "" {
			m.palette.searching = true
			return m, m.requestPaneSearch(msg.term)
		}
		return m, nil

	case paneSearchRespMsg:
		m = m.applyPaneSearch(msg.Resp)
		return m, m.listenForMessages()
```

> `strings` is already imported in `model.go`. The `paletteSearchDebounceMsg` case does NOT re-arm `listenForMessages` (it consumed no daemon message); the `paneSearchRespMsg` case DOES (it consumed one Receive) — this preserves the one-outstanding-Receive invariant.

- [ ] **Step 6: Run tests + vet**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine sh -c "go vet ./internal/tui/ && go test ./internal/tui/ -run 'TestApplyPaneSearch|TestBuildPaletteCommands|TestParsePaletteQuery' 2>&1 | tail -20"`
Expected: vet clean; all listed tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_search.go internal/tui/palette_search_test.go internal/tui/model.go
git commit -m "feat(tui): palette content-search request/response plumbing"
```

---

## Task 6: TUI — content-mode render + keys + navigation

**Files:**
- Modify: `internal/tui/palette.go` (`renderCommandPalette` route; `handleCommandPaletteKey`; extract `goToPane`)
- Modify: `internal/tui/palette_search.go` (`renderPaletteContent`, `paletteHitWindow`)
- Test: `internal/tui/palette_search_test.go`

**Interfaces:**
- Consumes: `m.paletteInnerWidth`, `truncateToWidth`, `dialogTitle`, `dialogSubtle`, `dialogSelected`, `dialogNormal`, `ctxMenuDisabledStyle`, `lastCellsToWidth`, `m.switchTab`, `m.activeTabModel`.
- Produces:
  - `func (m *Model) goToPane(paneID string) (tea.Model, tea.Cmd)` — extracted navigation.
  - `func renderPaletteContent(m Model, inner int) string`.
  - `const paletteVisibleHits = 6`; `func paletteHitWindow(cursor, n int) (int, int)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/palette_search_test.go`:

```go
func TestPaletteHitWindow(t *testing.T) {
	if s, e := paletteHitWindow(0, 3); s != 0 || e != 3 {
		t.Errorf("small: got [%d,%d), want [0,3)", s, e)
	}
	s, e := paletteHitWindow(paletteVisibleHits+2, 40)
	if cursor := paletteVisibleHits + 2; cursor < s || cursor >= e {
		t.Errorf("cursor %d not in window [%d,%d)", cursor, s, e)
	}
}

func TestRenderPaletteContent_States(t *testing.T) {
	m := newSplitDragTestModel(t)
	// Empty term.
	m.palette.mode = paletteModeContent
	m.palette.term = ""
	if out := renderPaletteContent(*m, m.paletteInnerWidth()); !strings.Contains(out, "Type to search") {
		t.Errorf("empty term hint missing:\n%s", out)
	}
	// Searching.
	m.palette.term = "x"
	m.palette.searching = true
	if out := renderPaletteContent(*m, m.paletteInnerWidth()); !strings.Contains(out, "Searching") {
		t.Errorf("searching hint missing:\n%s", out)
	}
	// No hits.
	m.palette.searching = false
	m.palette.hits = nil
	if out := renderPaletteContent(*m, m.paletteInnerWidth()); !strings.Contains(out, "No matches") {
		t.Errorf("no-match hint missing:\n%s", out)
	}
}

func TestRenderPaletteContent_WidthSafe(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.width = 30
	m.palette.mode = paletteModeContent
	m.palette.term = "x"
	m.palette.hits = []paletteHit{{
		paneID:  "p1",
		label:   "🚀🚀🚀🚀 very long pane name here",
		detail:  "999×",
		excerpt: strings.Repeat("long excerpt ", 20),
	}}
	inner := m.paletteInnerWidth()
	for i, line := range strings.Split(renderPaletteContent(*m, inner), "\n") {
		if w := lipgloss.Width(line); w > inner {
			t.Errorf("line %d width %d exceeds inner %d: %q", i, w, inner, line)
		}
	}
}

func TestPaletteContent_EnterNavigates(t *testing.T) {
	m := newSplitDragTestModel(t) // active tab 0, active pane p1
	m.dialog = dialogCommandPalette
	m.palette.mode = paletteModeContent
	m.palette.term = "x"
	m.palette.hits = []paletteHit{{paneID: "p2", label: "1.2 · terminal"}}
	m.palette.cursor = 0

	updated, _ := m.handleCommandPaletteKey(keyPress("enter"))
	m2 := updated.(Model)
	if m2.dialog != dialogNone {
		t.Errorf("palette should close after navigation, dialog=%v", m2.dialog)
	}
	if tab := m2.activeTabModel(); tab == nil || tab.ActivePane != "p2" {
		t.Errorf("active pane should be p2 after enter")
	}
}
```

> `keyPress` helper: if the test package lacks one, add it in this file:
> ```go
> func keyPress(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: keyCodeFor(s)} }
> ```
> BUT first check whether an existing helper builds `tea.KeyPressMsg` from a string (grep `KeyPressMsg{` in `internal/tui/*_test.go`). If one exists, use it and delete this note. If Enter is hard to synthesize, instead test `m.executePaletteContentEnter()` directly (see Step 3) — call the navigation path without a synthetic key. Prefer the direct-call form to avoid key-synthesis fragility.

Because key synthesis varies, the ROBUST version of the navigation test calls the handler helper directly:

```go
func TestPaletteContent_EnterNavigatesDirect(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	m.palette.mode = paletteModeContent
	m.palette.hits = []paletteHit{{paneID: "p2", label: "1.2 · terminal"}}
	m.palette.cursor = 0

	updated, _ := m.goToPane("p2")
	m2 := updated.(Model)
	if tab := m2.activeTabModel(); tab == nil || tab.ActivePane != "p2" {
		t.Errorf("goToPane should activate p2")
	}
}
```

Keep `TestPaletteContent_EnterNavigatesDirect` (always valid) and drop `TestPaletteContent_EnterNavigates` if no key-synthesis helper exists.

- [ ] **Step 2: Run test to verify it fails**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run 'TestRenderPaletteContent|TestPaletteHitWindow|TestPaletteContent' 2>&1 | tail -20`
Expected: FAIL — `undefined: renderPaletteContent` / `undefined: goToPane` / `undefined: paletteHitWindow`.

- [ ] **Step 3: Extract `goToPane`**

In `internal/tui/palette.go`, replace the body of the `case palActGoToPane:` in `executePaletteCommand` with a call, and add the helper. New case body:

```go
	case palActGoToPane:
		return m.goToPane(c.arg)
```

Add the helper (value→pointer receiver to match sibling helpers; `executePaletteCommand` has a value receiver `m`, so `goToPane` takes a value receiver too and returns the updated model):

```go
// goToPane switches to the tab containing paneID and makes it the active pane.
// Shared by the command palette's "Go to pane" rows and content-search Enter.
// The old tab's active-pane flag is cleared BEFORE switchTab moves m.activeTab
// (ordering is load-bearing for the border repaint).
func (m Model) goToPane(paneID string) (tea.Model, tea.Cmd) {
	pane, idx := m.findPaneAndTab(paneID)
	if pane == nil || idx < 0 || idx >= len(m.tabs) {
		return m, nil
	}
	if cur := m.activeTabModel(); cur != nil {
		if old := cur.ActivePaneModel(); old != nil {
			old.Active = false
		}
	}
	m.tabs[idx].ActivePane = paneID
	pane.Active = true
	return m, m.switchTab(idx)
}
```

> `executePaletteCommand` closes the palette (`m.closeCommandPalette()`) before dispatch, so `goToPane` does not close it. For the content-mode Enter path (Step 5) close the palette explicitly before calling `goToPane`.

- [ ] **Step 4: Add the content renderer**

Append to `internal/tui/palette_search.go` (add `"fmt"`, `"strings"` if not already imported, and `"charm.land/lipgloss/v2"`):

```go
const paletteVisibleHits = 6 // pane hits shown before the list scrolls (2 lines each)

// paletteHitWindow returns the [start,end) slice of hits to render, sized to
// paletteVisibleHits and shifted to keep cursor visible.
func paletteHitWindow(cursor, n int) (int, int) {
	if n <= paletteVisibleHits {
		return 0, n
	}
	start := 0
	if cursor >= paletteVisibleHits {
		start = cursor - paletteVisibleHits + 1
	}
	if max := n - paletteVisibleHits; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	return start, start + paletteVisibleHits
}

// renderPaletteContent renders the content-search view: a "Search pane content"
// header + term, then one two-line entry per matching pane (a selectable label
// row and a dim excerpt row), or a state hint. Every line is clamped to inner.
func renderPaletteContent(m Model, inner int) string {
	var b strings.Builder
	subtle := func(s string) string { return dialogSubtle.Render(truncateToWidth(s, inner)) }

	// Header: "/ " prompt + term + caret.
	qAvail := inner - 3
	if qAvail < 1 {
		qAvail = 1
	}
	b.WriteString(dialogTitle.Render("/ "))
	b.WriteString(dialogEditStyle.Render(lastCellsToWidth(m.palette.term, qAvail) + "│"))
	b.WriteByte('\n')
	b.WriteString(subtle("Search pane content"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	const hint = "↑↓ nav · Enter go · Esc close"

	switch {
	case strings.TrimSpace(m.palette.term) == "":
		b.WriteString(subtle("  Type to search across all panes"))
		b.WriteString("\n\n")
		b.WriteString(subtle(hint))
		return b.String()
	case len(m.palette.hits) == 0 && m.palette.searching:
		b.WriteString(subtle("  Searching…"))
		b.WriteString("\n\n")
		b.WriteString(subtle(hint))
		return b.String()
	case len(m.palette.hits) == 0:
		b.WriteString(subtle("  No matches in any pane"))
		b.WriteString("\n\n")
		b.WriteString(subtle(hint))
		return b.String()
	}

	start, end := paletteHitWindow(m.palette.cursor, len(m.palette.hits))
	if start > 0 {
		b.WriteString(subtle(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		h := m.palette.hits[i]
		b.WriteString(renderPaletteHitRow(h, i == m.palette.cursor, inner))
		b.WriteByte('\n')
		b.WriteString(ctxMenuDisabledStyle.Render(truncateToWidth("    "+h.excerpt, inner)))
		b.WriteByte('\n')
	}
	if end < len(m.palette.hits) {
		b.WriteString(subtle(fmt.Sprintf("  ↓ %d more", len(m.palette.hits)-end)))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(subtle(hint))
	return b.String()
}

// renderPaletteHitRow renders a hit's label row through the SHARED row
// renderer (see Step 4b) so command rows and hit rows cannot drift apart.
func renderPaletteHitRow(h paletteHit, cursor bool, inner int) string {
	return renderPaletteLine(h.label, h.detail, cursor, false, inner)
}
```

- [ ] **Step 4b: Extract the shared row renderer (DRY — supersedes a duplicated copy)**

The row layout math must exist ONCE: two copies would let the command-mode and
content-mode box borders drift apart. In `internal/tui/palette.go`, replace the
body of the existing `renderPaletteRow` with a delegation and add the shared
helper beside it. `renderPaletteRow`'s signature is UNCHANGED, so the existing
`TestRenderPaletteRow_WideLabelNoOverflow` keeps guarding it:

```go
// renderPaletteLine lays out one palette row: "› "/"  " cursor prefix, label
// left, detail right-aligned, padded to inner width. Shared by command rows and
// content-search hit rows — the single source of the width-clamp math. Both the
// detail and the label are bounded cell-aware (wide glyphs never wrap the box):
// the detail is clamped first so a long shortcut cannot starve the label.
func renderPaletteLine(label, detail string, cursor, disabled bool, inner int) string {
	prefix := "  "
	if cursor {
		prefix = "› "
	}
	contentW := inner - 2 // prefix takes 2 cells
	if contentW < 1 {
		contentW = 1
	}
	if maxDetail := contentW - 2; maxDetail >= 0 && lipgloss.Width(detail) > maxDetail {
		detail = truncateToWidth(detail, maxDetail)
	}
	detailW := lipgloss.Width(detail)
	labelMax := contentW - detailW - 1 // ≥1 space gap
	if labelMax < 1 {
		labelMax = 1
	}
	if lipgloss.Width(label) > labelMax {
		label = truncateToWidth(label, labelMax)
	}
	gap := contentW - lipgloss.Width(label) - detailW
	if gap < 1 {
		gap = 1
	}
	labelStyle := dialogNormal
	switch {
	case disabled:
		labelStyle = ctxMenuDisabledStyle
	case cursor:
		labelStyle = dialogSelected
	}
	row := prefix + labelStyle.Render(label) + strings.Repeat(" ", gap)
	if detail != "" {
		row += dialogSubtle.Render(detail)
	}
	return row
}

// renderPaletteRow renders one command row via the shared layout.
func renderPaletteRow(c paletteCommand, cursor bool, inner int) string {
	return renderPaletteLine(c.label, c.detail, cursor, !c.enabled, inner)
}
```

Verify the refactor preserved behavior: `TestRenderPaletteRow_WideLabelNoOverflow`,
`TestRenderCommandPalette_NarrowTerminalNoOverflow`, and
`TestRenderCommandPalette_ShortcutNotWrapped` must all still pass unchanged.

> If `dialogEditStyle` / `dialogTitle` / `dialogSelected` / `dialogNormal` / `dialogSubtle` are not visible from `palette_search.go`, they are package-level styles in the same `tui` package — no import needed. Confirm the exact names against `palette.go` (it already uses all of them).

- [ ] **Step 5: Route render + content keys**

In `internal/tui/palette.go`, at the top of `renderCommandPalette`, branch to content mode:

```go
func renderCommandPalette(m Model) string {
	inner := m.paletteInnerWidth()
	if m.palette.mode == paletteModeContent {
		return renderPaletteContent(m, inner)
	}
	// ... existing command-mode body unchanged ...
```

In `handleCommandPaletteKey`, handle content-mode navigation + Enter, and make typing recompute the mode/term + schedule the debounce. Replace the `case key == "enter":`, the up/down cases, and the text/backspace/space cases so they branch on mode. Concretely, restructure the switch:

```go
func (m Model) handleCommandPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	content := m.palette.mode == paletteModeContent
	switch {
	case key == "esc":
		m.closeCommandPalette()
		return m, nil
	case key == "enter":
		if content {
			if c := m.palette.cursor; c >= 0 && c < len(m.palette.hits) {
				paneID := m.palette.hits[c].paneID
				m.closeCommandPalette()
				return m.goToPane(paneID)
			}
			return m, nil
		}
		if c := m.palette.cursor; c >= 0 && c < len(m.palette.filtered) && m.palette.filtered[c].selectable() {
			return m.executePaletteCommand(m.palette.filtered[c])
		}
		return m, nil
	case key == "up" || key == "ctrl+p":
		if content {
			if m.palette.cursor > 0 {
				m.palette.cursor--
			}
			return m, nil
		}
		m.palette.cursor = nextSelectable(m.palette.filtered, m.palette.cursor, -1)
		return m, nil
	case key == "down" || key == "ctrl+n":
		if content {
			if m.palette.cursor < len(m.palette.hits)-1 {
				m.palette.cursor++
			}
			return m, nil
		}
		m.palette.cursor = nextSelectable(m.palette.filtered, m.palette.cursor, +1)
		return m, nil
	case key == "backspace":
		if q := []rune(m.palette.query); len(q) > 0 {
			m.palette.query = string(q[:len(q)-1])
			return m.afterPaletteQueryChange()
		}
		return m, nil
	case key == "space":
		m.palette.query += " "
		return m.afterPaletteQueryChange()
	case msg.Text != "" && isPrintableText(msg.Text):
		m.palette.query += msg.Text
		return m.afterPaletteQueryChange()
	}
	return m, nil
}

// afterPaletteQueryChange re-parses the query into (mode, term), refreshes the
// command filter or resets content state, and returns a debounce cmd when in
// content mode with a non-empty term.
func (m Model) afterPaletteQueryChange() (tea.Model, tea.Cmd) {
	m.palette.mode, m.palette.term = parsePaletteQuery(m.palette.query)
	if m.palette.mode == paletteModeContent {
		if strings.TrimSpace(m.palette.term) == "" {
			m.palette.hits = nil
			m.palette.searching = false
			m.palette.cursor = 0
			return m, nil
		}
		m.palette.cursor = 0
		return m, paletteSearchDebounce(m.palette.term)
	}
	// Command mode: reuse the existing filter path.
	m.refilterPalette()
	return m, nil
}
```

> The paste branch in `model.go` (`tea.PasteMsg` → `dialogCommandPalette`) currently does `m.palette.query += sanitizePaletteQuery(...)` then `m.refilterPalette()`. Change that branch to call `m.afterPaletteQueryChange()` instead so a pasted `/query` also switches mode and debounces. Find it via `grep -n "sanitizePaletteQuery" internal/tui/model.go`.

- [ ] **Step 6: Run tests + vet**

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine sh -c "go vet ./internal/tui/ && go test ./internal/tui/ 2>&1 | tail -30"`
Expected: vet clean; ALL `internal/tui` tests PASS (existing palette tests + new content tests).

- [ ] **Step 7: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_search.go internal/tui/palette_search_test.go internal/tui/model.go
git commit -m "feat(tui): palette content-search render, keys, navigation"
```

---

## Task 7: Full build + docs

**Files:**
- Modify: `docs/keybindings.md`, `docs/features.md`, `docs/roadmap/command-palette.md`, `CHANGELOG.md`, `.claude/CLAUDE.md`

- [ ] **Step 1: Full test + vet + build**

Run: `./scripts/dev.sh test 2>&1 | tail -30`
Expected: all packages PASS.

Run: `./scripts/dev.sh vet 2>&1 | tail -20`
Expected: clean.

Compile check (do NOT use `./scripts/dev.sh build` — CRLF-broken in the container):

Run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -m "$(pwd)"):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go build ./... 2>&1 | tail -20`
Expected: no output (clean compile of all packages).

> Producing the actual dev binaries is a controller-level step after the branch is reviewed, not part of this task.

- [ ] **Step 2: Update `docs/keybindings.md`**

Find the command-palette entry (grep `command_palette` / `alt+shift+p`). Add, right after its description:

```markdown
Inside the palette, type a leading `/` to switch to **content search**: the query
after the `/` is matched (literal, case-insensitive) against every pane's
scrollback. Matching panes are listed with a match count and a preview line;
press Enter to jump to the pane. Backspacing away the `/` returns to command mode.
```

- [ ] **Step 3: Update `docs/features.md`**

Under the command-palette feature bullet, add a sub-point:

```markdown
- **Content search** — type `/<text>` in the palette to find which panes have
  that text in their scrollback, with match counts and a preview, then Enter to
  navigate. Literal, case-insensitive; searches all tabs including background
  panes.
```

- [ ] **Step 4: Update `docs/roadmap/command-palette.md`**

Move the "content search across pane buffers" item from Phase 2 / deferred to a Done/Shipped section with a one-line note that it landed as the `/` mode.

- [ ] **Step 5: Update `CHANGELOG.md`**

Under the `## [Unreleased]` → `### Added` section (create if absent, Keep a Changelog format), add:

```markdown
- Command palette content search: type `/<text>` to list panes whose scrollback
  contains the text (match count + preview) and press Enter to navigate to one.
```

- [ ] **Step 6: Update `.claude/CLAUDE.md`**

Extend the existing "Command palette (M11)" bullet: append a sentence describing the `/` content-search mode — new IPC pair `MsgPaneSearchReq`/`MsgPaneSearchResp` handled in `internal/daemon/search.go` (`scanPaneMatches`/`searchPanes`, never spawns dormant panes), TUI side in `internal/tui/palette_search.go` (`parsePaletteQuery`, 150 ms debounce via `paletteSearchDebounce`, stale-drop by echoed query in `applyPaneSearch`, two-line-per-pane render, Enter reuses the shared `goToPane` helper).

- [ ] **Step 7: Commit**

```bash
git add docs/keybindings.md docs/features.md docs/roadmap/command-palette.md CHANGELOG.md .claude/CLAUDE.md
git commit -m "docs: document command palette content search"
```

---

## Manual verification (dev instance)

1. Rebuild: `./scripts/dev.sh build`.
2. If a prior dev daemon is running, stop it by PID from `./.quil/quild.pid` (NOT `~/.quil/`).
3. Launch `./scripts/quil-dev.ps1`; confirm `[dev]` in the status bar.
4. Create 2–3 panes, run commands that print distinct strings in some of them.
5. Open the palette (`alt+shift+p`), type `/` + a string present in a **background** pane's scrollback.
6. Verify that pane is listed with a match count + preview; press Enter; confirm you land on it with the term visible.
7. Verify: no-`/` queries still fuzzy-match commands; backspacing the `/` returns to command mode; a no-match term shows "No matches in any pane".

## Success check

`./scripts/dev.sh test` + `vet` green, `build` produces 6 binaries, and the manual flow above navigates from a `/`-query to the matching background pane.
