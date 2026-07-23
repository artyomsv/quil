# Command Palette (`Ctrl+Shift+P`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A modal, centered, keyboard-first fuzzy-find palette opened by `command_palette` (default `ctrl+shift+p,alt+shift+p`). Type to filter every Quil action + every open tab/pane; Enter runs the highlighted entry; Esc closes. Every command dispatches into the existing handler the keybinding already calls — a third dispatcher, never a second implementation.

**Architecture:** TUI-only. The palette is a `dialogScreen` (`dialogCommandPalette`) — modal + centered like `dialogCommandHistory`/`dialogMemory`, NOT a compositor overlay. It inherits input routing (the `m.dialog != dialogNone` gate in `handleKey`) and centered rendering (`renderDialog` + `lipgloss.Place`). No daemon, IPC, or persistence change; one config default added.

**Tech Stack:** Go 1.25 (Docker via `./scripts/dev.sh`), Bubble Tea v2, Lipgloss v2, `charmbracelet/x/ansi`.

**Spec:** `docs/superpowers/specs/2026-07-23-command-palette-design.md` — read it before starting.

## Global Constraints

- No daemon, IPC schema, or persistence changes. Everything lives in `internal/tui/` + one default in `internal/config/config.go`.
- Go/make are NOT installed on the host. Full test suite: `./scripts/dev.sh test` (Docker; works despite the CRLF issue that breaks `dev.sh build`). Targeted run (Git Bash):
  `MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run <TestName> -v`
- Commit style: imperative mood, ≤72-char subject, NO AI attribution of any kind, stage files by explicit path (never `git add -A`).
- Go files use tabs; comments match the codebase's dense explanatory style.
- Bubble Tea v2 notes: `Model.Update`/`handleKey`/dialog handlers are VALUE receivers returning `(tea.Model, tea.Cmd)`; pointer-receiver helpers (`openCommandPalette`, `closeCommandPalette`) are callable on the addressable local `m` — the established pattern (see `openCtxMenu`).
- The palette is keyboard-only in v1; no mouse handling inside it.

---

### Task 0: Branch setup

**Files:** none

- [x] **Step 1: Create the feature branch from master** — DONE (`feature/command-palette` off `master`, already checked out). Baseline `go build ./...` confirmed green.

---

### Task 1: Config keybinding — `command_palette`

**Files:**
- Modify: `internal/config/config.go` (`KeybindingsConfig` struct + `Default()` literal)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `KeybindingsConfig.CommandPalette string` with toml tag `command_palette`, default `"ctrl+shift+p,alt+shift+p"`. Task 5 reads `kb.CommandPalette`.

- [ ] **Step 1: Add the failing default-value test** (append to `config_test.go`, alongside the existing keybinding-default assertions)

```go
func TestDefault_CommandPaletteKeybinding(t *testing.T) {
	cfg := Default()
	if got := cfg.Keybindings.CommandPalette; got != "ctrl+shift+p,alt+shift+p" {
		t.Errorf("CommandPalette default = %q, want %q", got, "ctrl+shift+p,alt+shift+p")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `-run TestDefault_CommandPalette` → `cfg.Keybindings.CommandPalette undefined` (compile error).

- [ ] **Step 3: Add the struct field** next to the other single-key bindings in `KeybindingsConfig` (near `QuickActions`/`CommandHistory`):

```go
	CommandPalette  string `toml:"command_palette"`
```

- [ ] **Step 4: Add the default** in the `Keybindings: KeybindingsConfig{...}` literal in `Default()` (near `QuickActions`/`CommandHistory`):

```go
		CommandPalette:  "ctrl+shift+p,alt+shift+p",
```

Rationale comment above the field is not needed in the literal; the struct field carries the toml tag. (Load/Save serialize the whole struct — no other change. If a future rename is needed, follow the legacy-migration precedent already in `Load`.)

- [ ] **Step 5: Run to verify it passes** — `-run TestDefault_CommandPalette` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add command_palette keybinding default"
```

---

### Task 2: Fuzzy matching — pure functions

**Files:**
- Create: `internal/tui/palette.go` (types + fuzzy scorer only in this task)
- Test: `internal/tui/palette_test.go`

**Interfaces:**
- Produces: `paletteAction`, `paletteCommand`, `fuzzyScore(query, target string) (int, bool)`, `commandScore(query string, c paletteCommand) (int, bool)`, `filterPalette(query string, commands []paletteCommand) []paletteCommand`. Tasks 3–5 consume all of these.

- [ ] **Step 1: Write the failing tests** (new file `internal/tui/palette_test.go`)

```go
package tui

import (
	"reflect"
	"testing"
)

func cmd(label string, kw ...string) paletteCommand {
	return paletteCommand{action: palActNone, label: label, keywords: kw, enabled: true}
}

func TestFuzzyScore_Subsequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		query, target string
		wantMatch     bool
	}{
		{"empty query matches", "", "anything", true},
		{"exact substring", "split", "Split horizontal", true},
		{"scattered subsequence", "sph", "Split horizontal", true},
		{"case insensitive", "SPLIT", "split horizontal", true},
		{"not a subsequence", "xyz", "Split horizontal", false},
		{"order matters", "hs", "Split horizontal", true},  // h…s across words
		{"reverse order fails", "ts", "st", false},
	} {
		_, ok := fuzzyScore(tc.query, tc.target)
		if ok != tc.wantMatch {
			t.Errorf("%s: matched=%v, want %v", tc.name, ok, tc.wantMatch)
		}
	}
}

func TestFuzzyScore_Ranking(t *testing.T) {
	t.Parallel()
	// Consecutive/prefix beats scattered.
	pre, _ := fuzzyScore("spl", "Split pane")
	scat, _ := fuzzyScore("spl", "special loop")
	if pre <= scat {
		t.Errorf("prefix-consecutive %d should beat scattered %d", pre, scat)
	}
	// Word-boundary match beats mid-word.
	boundary, _ := fuzzyScore("h", "Split horizontal")
	// 'h' at start of the word "horizontal" should score well; sanity: matched.
	if boundary == 0 {
		t.Error("boundary match should have positive score")
	}
}

func TestCommandScore_BestOfLabelAndKeywords(t *testing.T) {
	t.Parallel()
	c := cmd("Split horizontal", "hsplit", "wide")
	// Matches a keyword the label doesn't contain.
	if _, ok := commandScore("hsplit", c); !ok {
		t.Error("should match on keyword")
	}
	// No match anywhere.
	if _, ok := commandScore("zzz", c); ok {
		t.Error("should not match")
	}
}

func TestFilterPalette_EmptyReturnsAllInOrder(t *testing.T) {
	t.Parallel()
	in := []paletteCommand{cmd("Alpha"), cmd("Beta"), cmd("Gamma")}
	got := filterPalette("", in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("empty query must return all in registry order, got %v", got)
	}
}

func TestFilterPalette_RanksAndStableTies(t *testing.T) {
	t.Parallel()
	in := []paletteCommand{
		cmd("Close pane"),        // 'close' matches
		cmd("Close tab"),         // 'close' matches, registry order after pane
		cmd("Split horizontal"),  // no match
	}
	got := filterPalette("close", in)
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	// Equal-ish scores keep registry order (pane before tab).
	if got[0].label != "Close pane" || got[1].label != "Close tab" {
		t.Errorf("stable tie order broken: %q, %q", got[0].label, got[1].label)
	}
}
```

- [ ] **Step 2: Run to verify they fail** — `-run 'TestFuzzy|TestCommandScore|TestFilterPalette'` → `undefined: paletteCommand`.

- [ ] **Step 3: Implement the types + scorer** (new file `internal/tui/palette.go`)

```go
package tui

import (
	"sort"
	"strings"
	"unicode"
)

// paletteAction identifies one command. Dispatch in executePaletteCommand
// (Task 5) routes each action into the SAME handler the keybinding case calls —
// a third dispatcher, never a second implementation.
type paletteAction int

const (
	palActNone paletteAction = iota
	palActGoToPane  // arg = paneID
	palActSwitchTab // arg = tabID
	palActSplitH
	palActSplitV
	palActFocus
	palActNotes
	palActRenamePane
	palActMute
	palActEager
	palActHistory
	palActLazygit
	palActRestartPane
	palActClosePane
	palActNewTab
	palActCloseTab
	palActRenameTab
	palActCycleTabColor
	palActNewPane
	palActSettings
	palActShortcuts
	palActPlugins
	palActMemory
	palActAbout
	palActClientLog
	palActDaemonLog
	palActMCPLog
	palActRedraw
	palActStopDaemon
)

// paletteCommand is one row of the palette. Disabled rows render greyed and are
// inert on Enter. arg carries a navigation target id (paneID/tabID); empty for
// static commands. keywords are extra fuzzy targets beyond the label.
type paletteCommand struct {
	action   paletteAction
	label    string
	detail   string
	keywords []string
	enabled  bool
	arg      string
}

// fuzzyScore reports whether query is a case-insensitive subsequence of target
// and, if so, a score (higher = better). Rewards consecutive runs, a match at
// the target start, a match right after a separator, and earlier position.
// Empty query returns (0, true) — everything passes.
func fuzzyScore(query, target string) (int, bool) {
	if query == "" {
		return 0, true
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	score, qi, prevMatch := 0, 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		gain := 1
		if ti == 0 {
			gain += 5 // start of target
		} else if isSeparator(t[ti-1]) {
			gain += 3 // word boundary
		}
		if ti == prevMatch+1 {
			gain += 4 // consecutive run
		}
		gain -= ti / 8 // mild earlier-is-better bias, bounded
		if gain < 1 {
			gain = 1
		}
		score += gain
		prevMatch = ti
		qi++
	}
	if qi < len(q) {
		return 0, false // not a full subsequence
	}
	return score, true
}

func isSeparator(r rune) bool {
	return r == ' ' || r == ':' || r == '-' || r == '_' || r == '.' || r == '/' || unicode.IsSpace(r)
}

// commandScore returns the best fuzzyScore of query against the command's label
// and each keyword; matched iff any target matches.
func commandScore(query string, c paletteCommand) (int, bool) {
	best, matched := fuzzyScore(query, c.label)
	for _, kw := range c.keywords {
		if s, ok := fuzzyScore(query, kw); ok && (!matched || s > best) {
			best, matched = s, true
		}
	}
	return best, matched
}

// filterPalette keeps commands matching query, sorted by score descending with
// a STABLE sort so equal scores preserve registry order. Empty query returns
// all commands in registry order (a fresh slice — never the caller's backing).
func filterPalette(query string, commands []paletteCommand) []paletteCommand {
	type scored struct {
		c paletteCommand
		s int
	}
	matched := make([]scored, 0, len(commands))
	for _, c := range commands {
		if s, ok := commandScore(query, c); ok {
			matched = append(matched, scored{c, s})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].s > matched[j].s })
	out := make([]paletteCommand, len(matched))
	for i, m := range matched {
		out[i] = m.c
	}
	return out
}
```

- [ ] **Step 4: Run to verify they pass** — `-run 'TestFuzzy|TestCommandScore|TestFilterPalette'` → PASS. If a ranking assertion is off, inspect the actual scores and adjust the WEIGHTS (start/boundary/consecutive) — the invariants that must hold: full-subsequence-only matches, empty→all-in-order, stable ties. Do not weaken the subsequence rule.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_test.go
git commit -m "feat(tui): command palette fuzzy scorer and registry types"
```

---

### Task 3: Palette state, build, and render

**Files:**
- Modify: `internal/tui/palette.go` (append state + build + render)
- Modify: `internal/tui/model.go` (add `palette paletteState` field to `Model`; add `dialogCommandPalette` iota value)
- Test: `internal/tui/palette_test.go` (append build + render tests)

**Interfaces:**
- Produces: `paletteState`, `(m *Model) buildPaletteCommands() []paletteCommand`, `paletteVisibleRows` const, `renderCommandPalette(m Model) string`. Task 5 wires these to open/render.
- Consumes: `m.tabs`, `tab.Leaves()`, `paneDisplayName`, `m.pluginRegistry` gates, `kbDisplay` (verify signature in Task 5 prep), active-pane `Muted`/`Eager`.

> **NOTE:** `buildPaletteCommands`' exact static-command list + the `kbDisplay(kb.X)` detail strings are finalized against the verified handler/keybinding names in Task 5 prep. This task lands the STATE + RENDER + navigation/gated-command build; the static command rows may be filled incrementally. Render + fuzzy already work on whatever the builder returns.

- [ ] **Step 1: Write the failing tests** (append to `palette_test.go`)

```go
func TestBuildPaletteCommands_NavigationAndGates(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // panes p1, p2 on tab 0
	cmds := m.buildPaletteCommands()

	var gotoP1, gotoP2, history, lazygit bool
	for _, c := range cmds {
		switch {
		case c.action == palActGoToPane && c.arg == "p1":
			gotoP1 = true
		case c.action == palActGoToPane && c.arg == "p2":
			gotoP2 = true
		case c.action == palActHistory:
			history = c.enabled
		case c.action == palActLazygit:
			lazygit = c.enabled
		}
	}
	if !gotoP1 || !gotoP2 {
		t.Errorf("both panes must have Go-to commands (p1=%v p2=%v)", gotoP1, gotoP2)
	}
	if history {
		t.Error("history must be disabled without a record_history plugin")
	}
	if lazygit {
		t.Error("lazygit must be disabled without an available plugin")
	}
	// Core static commands always present + enabled.
	if !hasEnabledAction(cmds, palActClosePane) || !hasEnabledAction(cmds, palActSplitH) {
		t.Error("close-pane and split-horizontal must always be present and enabled")
	}
}

func hasEnabledAction(cmds []paletteCommand, a paletteAction) bool {
	for _, c := range cmds {
		if c.action == a && c.enabled {
			return true
		}
	}
	return false
}

func TestRenderCommandPalette_WidthAndCursor(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.palette = paletteState{query: "close"}
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("close", m.palette.commands)
	out := renderCommandPalette(*m)
	if out == "" {
		t.Fatal("render produced empty output")
	}
	// Query row present.
	if !strings.Contains(out, "close") {
		t.Error("query text should appear in the rendered palette")
	}
}

func TestRenderCommandPalette_EmptyResults(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.palette = paletteState{query: "zzzzzz"}
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("zzzzzz", m.palette.commands)
	out := renderCommandPalette(*m)
	if !strings.Contains(out, "No matching") {
		t.Errorf("empty results should show a 'No matching' row, got:\n%s", out)
	}
}
```

(Add `"strings"` to the test imports.)

- [ ] **Step 2: Run to verify they fail** — `undefined: paletteState` etc.

- [ ] **Step 3: Implement state + build + render** (append to `palette.go`). Full code is derived during implementation from the spec's Components 1 & 3; key shape:

```go
const paletteVisibleRows = 12
const paletteWidth = 72 // outer dialog width; renderDialog clamps to m.width-2

// paletteState holds the command-palette buffer + result cursor. There is NO
// `open` field — m.dialog == dialogCommandPalette is the sole open/closed
// authority (mirrors ctxMenu needing no such flag). Zero value = empty.
type paletteState struct {
	query    string
	cursor   int
	commands []paletteCommand
	filtered []paletteCommand
}

func (m *Model) buildPaletteCommands() []paletteCommand {
	var cmds []paletteCommand
	// Navigation: one Go-to per pane across all tabs, one Switch-to per tab.
	for i, tab := range m.tabs {
		cmds = append(cmds, paletteCommand{
			action: palActSwitchTab, arg: tab.ID, enabled: true,
			label:  "Switch to tab: " + tabIndexName(i, tab),
			detail: "tab",
		})
		for _, p := range tab.Leaves() {
			if p == nil {
				continue
			}
			cmds = append(cmds, paletteCommand{
				action: palActGoToPane, arg: p.ID, enabled: true,
				label:  "Go to: " + tabIndexName(i, tab) + " / " + paneDisplayName(p),
				detail: p.Type,
			})
		}
	}
	// Gated pane commands.
	historyOK, lazygitOK := false, false
	activeMuted, activeEager := false, false
	if tab := m.activeTabModel(); tab != nil {
		if p := tab.ActivePaneModel(); p != nil {
			activeMuted, activeEager = p.Muted, p.Eager
			if m.pluginRegistry != nil {
				if pl := m.pluginRegistry.Get(p.Type); pl != nil {
					historyOK = pl.Command.RecordHistory
				}
			}
		}
	}
	if m.pluginRegistry != nil {
		if pl := m.pluginRegistry.Get("lazygit"); pl != nil {
			lazygitOK = pl.Available
		}
	}
	// Static commands — detail strings via kbDisplay(kb.X) filled in Task 5 prep.
	// (append palActSplitH/V, Focus, Notes, RenamePane, Mute(label per activeMuted),
	//  Eager(label per activeEager), History(enabled=historyOK), Lazygit(enabled=lazygitOK),
	//  RestartPane, ClosePane, NewTab, CloseTab, RenameTab, CycleTabColor, NewPane,
	//  Settings, Shortcuts, Plugins, Memory, About, ClientLog, DaemonLog, MCPLog,
	//  Redraw, StopDaemon.)
	return cmds
}

// tabIndexName renders the 1-based index + name, matching the tab bar.
func tabIndexName(i int, tab *TabModel) string { /* fmt "%d:%s" */ }

func renderCommandPalette(m Model) string {
	// query row + blank + up-to-paletteVisibleRows result rows (scroll window
	// keeping cursor visible) + blank + footer hint. Width clamp per spec.
	// Empty filtered → single greyed "No matching commands" row.
}
```

- [ ] **Step 4: Add the Model field + dialog value.** In `model.go`, add `dialogCommandPalette` to the `dialogScreen` iota (at the end, before/after `dialogUpdateNotice` — pick the tail per the verified list). Add to the `Model` struct near `ctxMenu`:

```go
	// palette is the command-palette state (dialogCommandPalette). Zero value
	// = closed; m.dialog is the source of truth for open/closed.
	palette paletteState
```

- [ ] **Step 5: Run to verify they pass** — `-run 'TestBuildPalette|TestRenderCommandPalette'` → PASS. Full `./internal/tui/` still green.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_test.go internal/tui/model.go
git commit -m "feat(tui): command palette state, registry build, and render"
```

---

### Task 4: Extract inline handlers for reuse (pure refactor)

**Files:**
- Modify: `internal/tui/model.go` (`handleKey` cases: CloseTab ~2317, RenameTab ~2338, `ctrl+n` ~2449, Redraw ~2351)

**Interfaces:** Produces (Task 5 dispatch calls these):
- `(m Model) openCloseTabConfirm() (tea.Model, tea.Cmd)`
- `(m Model) beginTabRename() (tea.Model, tea.Cmd)`
- `(m Model) openCreatePaneDialog() (tea.Model, tea.Cmd)`
- `(m Model) forceRedraw() (tea.Model, tea.Cmd)`

> `Stop daemon` is NOT extracted — it is dropped from v1 (spec correction 2; it
> lives in `handleAboutKey`, not `handleKey`, and carries About-return coupling).

- [ ] **Step 1: Extract the four methods.** Add near the other extracted action
methods (below `openHistoryForActivePane`, ~model.go:1785+). Each body is MOVED
verbatim from its case; the case becomes a one-line delegation. Zero behavior
change.

```go
// openCloseTabConfirm opens the close-tab confirm for the active tab.
// Extracted from the kb.CloseTab case; shared with the command palette.
func (m Model) openCloseTabConfirm() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		m.dialog = dialogConfirm
		m.confirmKind = "tab"
		m.confirmID = tab.ID
		m.confirmName = tab.Name
	}
	return m, tea.ClearScreen
}

// beginTabRename enters inline tab-rename mode for the active tab.
// Extracted from the kb.RenameTab case; shared with the command palette.
func (m Model) beginTabRename() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		m.renaming = true
		m.renameInput = tab.Name
	}
	return m, nil
}

// openCreatePaneDialog opens the create-pane dialog at step 0 (the Ctrl+N flow).
// Extracted from the `key == "ctrl+n"` case; shared with the command palette.
func (m Model) openCreatePaneDialog() (tea.Model, tea.Cmd) {
	m.dialog = dialogCreatePane
	m.dialogCursor = 0
	m.createPaneStep = 0
	m.selectedCategory = 0
	return m, tea.ClearScreen
}
```

For `forceRedraw`: **open `model.go:2351-2369` and MOVE the entire `kb.Redraw`
case body verbatim** into `func (m Model) forceRedraw() (tea.Model, tea.Cmd)`
(it mutates `m.tabs` to invalidate render caches before returning
`tea.Batch(tea.ClearScreen, …)` — keep that loop intact; it is NOT a bare
`func() tea.Cmd`). The `ctrl+n` and `kb.Redraw` cases keep matching exactly as
before.

- [ ] **Step 2: Replace the four case bodies with delegations**

```go
	case kbMatches(key, kb.CloseTab):
		return m.openCloseTabConfirm()
```
```go
	case kbMatches(key, kb.RenameTab):
		return m.beginTabRename()
```
```go
	case key == "ctrl+n":
		return m.openCreatePaneDialog()
```
```go
	case kbMatches(key, kb.Redraw):
		return m.forceRedraw()
```

- [ ] **Step 3: Run the full TUI package — refactor must be behavior-neutral**

Run: targeted docker command with `-run '.*'` on `./internal/tui/`
Expected: PASS, zero test changes.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/model.go
git commit -m "refactor(tui): extract tab/create/redraw handlers for reuse"
```

---

### Task 5: Integration — open, route, dispatch, wire dialog + shortcuts

**Files:**
- Modify: `internal/tui/palette.go` (open/close/key/dispatch methods)
- Modify: `internal/tui/model.go` (`kb.CommandPalette` case in `handleKey`, near the other dialog-openers ~2449)
- Modify: `internal/tui/dialog.go` (`handleDialogKey` case ~360; `renderDialog` case ~760 with width; Shortcuts-help list ~255)
- Test: `internal/tui/palette_integration_test.go` (new)

**Interfaces:** Consumes Tasks 1–4. Produces `openCommandPalette`,
`closeCommandPalette`, `handleCommandPaletteKey`, `executePaletteCommand`.

- [ ] **Step 1: Write the failing integration tests** (new file). Cover: key
opens `dialogCommandPalette`; Esc closes; `msg.Text` typing filters + resets
cursor; backspace; up/down + ctrl+n/ctrl+p clamp; Enter on `Go to: p2` sets
`activeTab`/`ActivePane` cross-tab; Enter on `Close pane…` arms the pane confirm;
Enter on `Split horizontal` returns a cmd; disabled `Input history` inert;
**notes-mode guard** (open is a no-op when `m.notesMode`). Drive via
`m.Update(tea.KeyPressMsg{...})` and the handler methods on `newSplitDragTestModel`
(returns `*Model`; deref where a value `Model` is needed).

```go
func TestPalette_OpensAndCloses(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl | tea.ModShift})
	got := updated.(Model)
	if got.dialog != dialogCommandPalette {
		t.Fatalf("dialog = %v, want dialogCommandPalette", got.dialog)
	}
	updated, _ = got.handleCommandPaletteKey(keyMsg("esc"))
	if updated.(Model).dialog != dialogNone {
		t.Error("esc should close the palette")
	}
}

func TestPalette_NoOpInNotesMode(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.notesMode = true
	updated, _ := m.openCommandPalette()
	if updated.(Model).dialog == dialogCommandPalette {
		t.Error("palette must not open in notes mode")
	}
}

func TestPalette_GoToPaneCrossTabFocus(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("", m.palette.commands)
	updated, _ := m.executePaletteCommand(paletteCommand{action: palActGoToPane, arg: "p2", enabled: true})
	got := updated.(Model)
	if got.dialog != dialogNone {
		t.Error("palette should close on execute")
	}
	if got.tabs[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2", got.tabs[0].ActivePane)
	}
}

func TestPalette_ExecuteClosePaneArmsConfirm(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	updated, _ := m.executePaletteCommand(paletteCommand{action: palActClosePane, enabled: true})
	got := updated.(Model)
	if got.dialog != dialogConfirm || got.confirmKind != "pane" {
		t.Errorf("close-pane confirm not armed: dialog=%v kind=%q", got.dialog, got.confirmKind)
	}
}
```

Add a `keyMsg(s string) tea.KeyPressMsg` helper if one is not already shared in
the test package (check `ctxmenu_dispatch_test.go` — reuse its message-building
idiom; several tests build `tea.KeyPressMsg` directly). Confirm the exact
`msg.String()` the fixture must produce for `ctrl+shift+p` by running the open
test and, if it fails to match, logging `msg.String()` — then set the fixture to
whatever Bubble Tea v2 actually emits (and note it for the runtime default).

- [ ] **Step 2: Run to verify they fail** — `undefined: openCommandPalette`.

- [ ] **Step 3: Implement open/close/key/dispatch** (append to `palette.go`).

```go
// openCommandPalette builds the command registry and opens the palette. No-op
// in notes mode (its actions restructure the layout under the editor) — the
// explicit guard, mirroring openQuickActionsMenu; notesKeyExempt does NOT cover
// the pane-focused notes path, so absence from it is not enough.
func (m Model) openCommandPalette() (tea.Model, tea.Cmd) {
	if m.notesMode {
		return m, nil
	}
	m.palette = paletteState{}
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("", m.palette.commands)
	m.dialog = dialogCommandPalette
	return m, tea.ClearScreen
}

func (m *Model) closeCommandPalette() {
	m.dialog = dialogNone
	m.palette = paletteState{}
}

// handleCommandPaletteKey routes keys while the palette is open. Value receiver,
// like the sibling handleXKey dialog handlers.
func (m Model) handleCommandPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		m.closeCommandPalette()
		return m, nil
	case key == "enter":
		if c := m.palette.cursor; c >= 0 && c < len(m.palette.filtered) && m.palette.filtered[c].enabled {
			return m.executePaletteCommand(m.palette.filtered[c])
		}
		return m, nil
	case key == "up" || key == "ctrl+p":
		if m.palette.cursor > 0 {
			m.palette.cursor--
		}
		return m, nil
	case key == "down" || key == "ctrl+n":
		if m.palette.cursor < len(m.palette.filtered)-1 {
			m.palette.cursor++
		}
		return m, nil
	case key == "backspace":
		if q := []rune(m.palette.query); len(q) > 0 {
			m.palette.query = string(q[:len(q)-1])
			m.refilterPalette()
		}
		return m, nil
	case key == "space":
		m.palette.query += " "
		m.refilterPalette()
		return m, nil
	case msg.Text != "":
		m.palette.query += msg.Text
		m.refilterPalette()
		return m, nil
	}
	return m, nil
}

func (m *Model) refilterPalette() {
	m.palette.filtered = filterPalette(m.palette.query, m.palette.commands)
	m.palette.cursor = 0
}
```

`executePaletteCommand`: close the palette first, then `switch cmd.action`. Use
the verified signatures — **[value receiver, returns `(tea.Model, tea.Cmd)`]:**
`toggleFocusForActiveTab, toggleNotesMode, beginPaneRename, openHistoryForActivePane,
openRestartPaneConfirm, openClosePaneConfirm, openCloseTabConfirm, beginTabRename,
openCreatePaneDialog, forceRedraw, openLogViewer, openMCPLogsViewer` → return
directly. **[value receiver, returns bare `tea.Cmd`]:** `splitPane(dir)`,
`toggleActivePaneMute, toggleActivePaneEager, handleToggleLazygit, createTab,
cycleTabColor, switchTab(idx)` → `return m, m.x()`. **Special:**
- `palActMemory` → `m = m.openMemoryDialog(); return m, m.refreshMemory()` (bare `Model` return).
- `palActSettings/Shortcuts/Plugins/About` → `m.dialog = dialogX; m.dialogCursor = 0; return m, tea.ClearScreen` (inline; reset cursor — those dialogs render from `dialogCursor`).
- `palActClientLog/DaemonLog/MCPLog` → copy the exact `openLogViewer(label, path)` / `openMCPLogsViewer()` calls from `handleAboutKey` (dialog.go ~429-436).
- `palActGoToPane`: `pane, idx := m.findPaneAndTab(cmd.arg); if pane == nil { return m, nil }`; clear the CURRENT active tab's active pane `.Active` (before switching); `m.activeTab = idx`; `m.tabs[idx].ActivePane = cmd.arg`; `pane.Active = true`; `return m, m.switchTab(idx)`. (Ordering per spec correction 7.)
- `palActSwitchTab`: find idx by `tab.ID == cmd.arg`; `return m, m.switchTab(idx)`.

Backstop: `if !cmd.enabled { return m, nil }` at the top after close.

- [ ] **Step 4: Wire the keybinding** in `handleKey` (near the create-pane/F1
openers ~model.go:2449):

```go
	case kbMatches(key, kb.CommandPalette):
		return m.openCommandPalette()
```

- [ ] **Step 5: Wire the dialog** in `dialog.go`:
  - `handleDialogKey` switch (~360): `case dialogCommandPalette: return m.handleCommandPaletteKey(msg)`.
  - `renderDialog` switch (~760, mirror the `dialogMemory`/`dialogCommandHistory`
    arms): `case dialogCommandPalette: content = renderCommandPalette(m); width = paletteWidth` (renderDialog already clamps `width` to `m.width-2`). Ensure `renderCommandPalette` lays out its rows to `min(paletteWidth, m.width-2)-<border/pad>` so the detail column right-aligns to the same width.
  - Shortcuts-help list (~255): add a row `{"Command palette", kbDisplay(m.cfg.Keybindings.CommandPalette)}` in the same format as the existing rows.

- [ ] **Step 6: Run the palette + full suite** — `-run 'Palette|Fuzzy'` then full
`./internal/tui/`. Expected PASS. Fix compile/behavior per the verified
signatures.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_integration_test.go internal/tui/model.go internal/tui/dialog.go
git commit -m "feat(tui): command palette open, routing, and dispatch"
```

---

## Verification (whole feature)

- `./scripts/dev.sh test` (or targeted `-run 'Palette|Fuzzy'` + full `./internal/tui/`) green.
- `./scripts/dev.sh vet` clean.
- Manual (dev binary): `ctrl+shift+p` (and `alt+shift+p` fallback) opens; typing filters; Enter on `Go to: …` jumps cross-tab; Enter on `Close pane…` arms the confirm; Esc closes; palette does not open in notes mode.
- Debug-log key trace confirms the actual `msg.String()` for both bindings on the target terminal; adjust the default only if `ctrl+shift+p` proves undeliverable AND `alt+shift+p` also needs a different string form.
