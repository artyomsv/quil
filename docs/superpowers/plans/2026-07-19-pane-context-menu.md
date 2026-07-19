# Pane Context Menu (Right-Click) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Right-click on a pane opens a flyover menu near the cursor with 9 per-pane actions (history, focus, notes, lazygit, rename, mute, attention pin, restart, close), targeting the pane under the cursor and highlighting its border.

**Architecture:** TUI-only. The menu is a compositor overlay (like the notification sidebar) rendered by a new `overlayAt` sibling of `overlayRight`; it is NOT a `dialogScreen`. All actions dispatch into existing keybinding handler logic (extracted to named methods). New pane state: `pinnedAttention` (session-only green border that survives focus) and `ctxTargetHighlight` (transient menu-target border).

**Tech Stack:** Go 1.25 (Docker via `./scripts/dev.sh`), Bubble Tea v2, Lipgloss v2, `charmbracelet/x/ansi`.

**Spec:** `docs/superpowers/specs/2026-07-19-pane-context-menu-design.md` — read it before starting.

## Global Constraints

- No daemon, IPC schema, or persistence changes. Everything lives in `internal/tui/` + one default in `internal/config/config.go`.
- Go/make are NOT installed on the host. Full test suite: `./scripts/dev.sh test` (Docker; works despite the CRLF issue that breaks `dev.sh build`). Targeted run: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run <TestName> -v` (Git Bash).
- Right-click with an active selection MUST keep copying (regression-guarded).
- `quick_actions` default changes `ctrl+a` → `alt+a` (ctrl+a is readline beginning-of-line; stealing it is a regression).
- Commit style: imperative mood, ≤72-char subject, NO AI attribution of any kind, stage files by explicit path (never `git add -A`).
- Go files use tabs; comments match the codebase's dense explanatory style.
- Bubble Tea v2 notes: `Model.Update`/`handleKey` are VALUE receivers returning `(tea.Model, tea.Cmd)`; pointer-receiver helpers (`clearDragState`, `closeCtxMenu`) are callable on the addressable local `m` — this is the established pattern.

---

### Task 0: Branch setup

**Files:** none

- [ ] **Step 1: Create the feature branch from master**

```bash
git checkout master && git pull && git checkout -b feature/pane-context-menu
```

Expected: clean checkout, new branch. (Current work-in-flight branch `feature/auto-update` must not be touched.)

---

### Task 1: `overlayAt` positioned compositor

**Files:**
- Modify: `internal/tui/compose.go`
- Test: `internal/tui/compose_test.go`

**Interfaces:**
- Consumes: `github.com/charmbracelet/x/ansi` (`Truncate`, `TruncateLeft`, `StringWidth`, `Strip` — all in the already-imported module).
- Produces: `overlayAt(base, box string, x, y, totalW int) string` — Task 5's `View()` composition calls this.

- [ ] **Step 1: Confirm `ansi.TruncateLeft` exists in the module version in use**

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go doc github.com/charmbracelet/x/ansi TruncateLeft
```

Expected: doc output for `func TruncateLeft(s string, length int, prefix string) string` (cuts the leftmost `length` cells, preserving ANSI sequences). If it does NOT exist, use `ansi.Cut(s, rightStart, totalW)` instead (same module) and adapt the code below — the semantic needed is "the tail of the line from cell `rightStart` onward, with SGR state intact".

- [ ] **Step 2: Write the failing tests** (append to `internal/tui/compose_test.go`)

```go
func TestOverlayAt_MiddleOverwrite_PreservesTail(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	got := overlayAt(base, "XX\nYY", 3, 1, 10)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	if s := ansi.Strip(lines[0]); s != "aaaaaaaaaa" {
		t.Errorf("row 0 = %q, want untouched", s)
	}
	if s := ansi.Strip(lines[1]); s != "bbbXXbbbbb" {
		t.Errorf("row 1 = %q, want bbbXXbbbbb", s)
	}
	if s := ansi.Strip(lines[2]); s != "cccYYccccc" {
		t.Errorf("row 2 = %q, want cccYYccccc", s)
	}
}

func TestOverlayAt_SGRResetGuards(t *testing.T) {
	base := "\x1b[31mrrrrrrrrrr\x1b[0m"
	got := overlayAt(base, "XX", 4, 0, 10)
	// Reset must close the left segment before the box so red never bleeds in.
	if !strings.Contains(got, "\x1b[0mXX") {
		t.Errorf("box not preceded by SGR reset: %q", got)
	}
	if s := ansi.Strip(got); s != "rrrrXXrrrr" {
		t.Errorf("stripped = %q, want rrrrXXrrrr", s)
	}
}

func TestOverlayAt_WideGlyphAtLeftCut_KeepsTotalWidth(t *testing.T) {
	// 你(0-1)好(2-3)世(4-5)界(6-7)xx(8-9). Cut at x=3 straddles 好.
	base := "你好世界xx"
	got := overlayAt(base, "ZZ", 3, 0, 10)
	if w := ansi.StringWidth(got); w != 10 {
		t.Errorf("width = %d, want 10", w)
	}
	if s := ansi.Strip(got); !strings.HasPrefix(s, "你") || !strings.Contains(s, "ZZ") {
		t.Errorf("stripped = %q, want 你-prefix with ZZ box", s)
	}
}

func TestOverlayAt_BoxRowsBeyondBase_Dropped(t *testing.T) {
	got := overlayAt("aaaaaaaaaa", "XX\nYY\nZZ", 2, 0, 10)
	if n := len(strings.Split(got, "\n")); n != 1 {
		t.Errorf("line count = %d, want 1 (extra box rows dropped)", n)
	}
}

func TestOverlayAt_Degenerate_ReturnsBase(t *testing.T) {
	base := "aaaa\nbbbb"
	for name, tc := range map[string]struct{ x, y, w int }{
		"negative x":       {-1, 0, 4},
		"negative y":       {0, -1, 4},
		"box exceeds base": {3, 0, 4}, // 2-wide box at x=3 needs 5 cells
	} {
		if got := overlayAt(base, "XX", tc.x, tc.y, tc.w); got != base {
			t.Errorf("%s: got %q, want base unchanged", name, got)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: targeted docker command with `-run TestOverlayAt`
Expected: FAIL — `undefined: overlayAt`

- [ ] **Step 4: Implement `overlayAt`** (append to `internal/tui/compose.go`)

```go
// overlayAt composites box onto base with box's top-left cell at column x,
// row y (both 0-based within base). base is a block of totalW-wide lines.
// Same ANSI discipline as overlayRight: segments are cut with ansi.Truncate /
// ansi.TruncateLeft and closed with an SGR reset on BOTH sides of the box so
// base styling never bleeds into it and the box's styling never bleeds into
// the preserved right tail. Used by the pane context menu — a positional
// popup that, like the sidebar, must not reserve layout width (a layout
// change would resize PTYs; see the 2026-07-04 resize-artifacts design).
//
// The caller (ctxMenuPos) is responsible for clamping the box on screen;
// out-of-range inputs return base unchanged as a backstop, and box rows
// below base's last line are dropped.
func overlayAt(base, box string, x, y, totalW int) string {
	if x < 0 || y < 0 || totalW <= 0 {
		return base
	}
	boxLines := strings.Split(box, "\n")
	boxW := 0
	for _, bl := range boxLines {
		if w := ansi.StringWidth(bl); w > boxW {
			boxW = w
		}
	}
	if boxW == 0 || x+boxW > totalW {
		return base
	}
	baseLines := strings.Split(base, "\n")
	for i, bl := range boxLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		left := ansi.Truncate(baseLines[row], x, "")
		pad := ""
		if n := x - ansi.StringWidth(left); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		right := ansi.TruncateLeft(baseLines[row], x+ansi.StringWidth(bl), "")
		baseLines[row] = left + "\x1b[0m" + pad + bl + "\x1b[0m" + right
	}
	return strings.Join(baseLines, "\n")
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: targeted docker command with `-run TestOverlayAt`
Expected: PASS (all 5). If the wide-glyph or tail test fails on exact content, inspect what `ansi.TruncateLeft` produced and adjust the ASSERTION to the library's actual straddle behavior (space-padding at the cut is acceptable) — the invariants that must hold are total width and untouched rows.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/compose.go internal/tui/compose_test.go
git commit -m "feat(tui): add overlayAt positioned compositor"
```

---

### Task 2: Pane visual state — `pinnedAttention` + `ctxTargetHighlight`

**Files:**
- Modify: `internal/tui/pane.go` (PaneModel struct ~line 81/128, `paneRenderKey` ~line 150, `renderKey()` ~line 171, `View()` border colors ~line 747)
- Modify: `internal/tui/workstate.go` (new `tabPinnedAttention` after `tabUnseen` ~line 225)
- Modify: `internal/tui/model.go` (`tabStyle` ~line 3028)
- Test: `internal/tui/workstate_pinned_test.go` (new)

**Interfaces:**
- Produces: `PaneModel.pinnedAttention bool`, `PaneModel.ctxTargetHighlight bool`, `Model.tabPinnedAttention(idx int) bool`. Task 5's open/close/dispatch reads and writes all three.
- Guarantees: `ackFocusedPane` clears ONLY `unseen`, never `pinnedAttention` (no code change needed — the test locks it).

- [ ] **Step 1: Write the failing tests** (new file `internal/tui/workstate_pinned_test.go`)

```go
package tui

import (
	"strings"
	"testing"
)

// Pinned attention must survive focusing the pane — that is the whole point
// of the pin vs the auto-clearing unseen mark.
func TestPinnedAttention_SurvivesAckFocusedPane(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	p1 := m.tabs[0].Root.Left.Pane
	p1.pinnedAttention = true
	p1.unseen = true
	m.tabs[0].ActivePane = "p1"
	m.ackFocusedPane()
	if p1.unseen {
		t.Error("unseen should be cleared on focus")
	}
	if !p1.pinnedAttention {
		t.Error("pinnedAttention must survive focus")
	}
}

// The green tab label derivation: unlike tabUnseen, a pinned pane colors the
// ACTIVE tab too — unless the pinned pane is the focused pane itself.
func TestTabPinnedAttention(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // tab 0 active, panes p1 (focused) | p2
	p2 := m.tabs[0].Root.Right.Pane

	if m.tabPinnedAttention(0) {
		t.Error("no pins: want false")
	}
	p2.pinnedAttention = true
	if !m.tabPinnedAttention(0) {
		t.Error("pinned unfocused pane on active tab: want true")
	}
	// Focus the pinned pane: the user is looking at it — no label cue.
	m.tabs[0].ActivePane = "p2"
	if m.tabPinnedAttention(0) {
		t.Error("pinned focused pane: want false")
	}
	// Out-of-range index must not panic.
	if m.tabPinnedAttention(5) {
		t.Error("out of range: want false")
	}
}

// Both new flags are View() inputs — they MUST be part of the render key or
// toggling them renders a stale cached frame.
func TestRenderKey_IncludesPinnedAndCtxHighlight(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("k", 1024)
	base := p.renderKey()
	p.pinnedAttention = true
	if p.renderKey() == base {
		t.Error("pinnedAttention must change the render key")
	}
	p.pinnedAttention = false
	p.ctxTargetHighlight = true
	if p.renderKey() == base {
		t.Error("ctxTargetHighlight must change the render key")
	}
}

// Border colors: pinned renders the same green as unseen; the menu-target
// highlight renders the split-drag blue (39). Uses the split fixture's panes
// because they have a live VT emulator and real dimensions (View() needs
// both; a bare NewPaneModel does not get them until a layout Resize).
func TestView_PinnedGreenBorder_CtxHighlightBlue(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	p := m.tabs[0].Root.Left.Pane
	p.Active = false // active purple (57) outranks green — test the idle pane look
	p.pinnedAttention = true
	if !strings.Contains(p.View(), "38;5;28") {
		t.Error("pinned pane should render green (28) border")
	}
	p.ctxTargetHighlight = true
	if !strings.Contains(p.View(), "38;5;39") {
		t.Error("ctx-target pane should render blue (39) border")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: targeted docker command with `-run 'TestPinnedAttention|TestTabPinnedAttention|TestRenderKey_Includes|TestView_Pinned'`
Expected: FAIL — `p.pinnedAttention undefined` (compile error).

- [ ] **Step 3: Implement the pane fields + render wiring**

In `internal/tui/pane.go`, after the `unseen` field (~line 81):

```go
	unseen             bool                // work finished/parked while this pane was not focused; cleared on focus
	pinnedAttention    bool                // context-menu "Mark attention" pin — green border that SURVIVES focus; cleared only by Unmark. TUI-session state, never persisted
```

After the `splitDragHighlight` field (~line 128):

```go
	// ctxTargetHighlight marks this pane's border while the pane context
	// menu is open and targeting it. Transient TUI state, never persisted;
	// set/cleared by Model.openCtxMenu / Model.closeCtxMenu.
	ctxTargetHighlight bool
```

In `paneRenderKey` (~line 157-159), extend:

```go
	splitDragHighlight             bool
	ctxTargetHighlight             bool
	working                        bool
	unseen                         bool
	pinnedAttention                bool
```

In `renderKey()` (~line 184-188), populate:

```go
		splitDragHighlight: p.splitDragHighlight,
		ctxTargetHighlight: p.ctxTargetHighlight,
```
and
```go
		unseen:             p.unseen,
		pinnedAttention:    p.pinnedAttention,
```

In `View()` (~line 747-762), change the green condition and add the ctx highlight between splitDrag and mcp (precedence: menu target above split-drag is moot — they can't coexist — but MCP orange stays on top):

```go
	borderColor := lipgloss.Color("238")
	if p.unseen || p.pinnedAttention {
		borderColor = lipgloss.Color("28") // green — finished/parked/pinned, awaiting attention
	}
	if p.Active {
		borderColor = lipgloss.Color("57")
	}
	if p.ghost || p.resuming || p.preparing {
		borderColor = lipgloss.Color("95") // muted purple — distinct but not jarring
	}
	if p.splitDragHighlight {
		borderColor = lipgloss.Color("39") // bright blue — split drag in progress
	}
	if p.ctxTargetHighlight {
		borderColor = lipgloss.Color("39") // bright blue — context-menu target
	}
	if p.mcpHighlight {
		borderColor = lipgloss.Color("208") // orange — MCP interaction
	}
```

In `internal/tui/workstate.go`, after `tabUnseen` (~line 225):

```go
// tabPinnedAttention reports whether the tab at idx contains a pane with a
// manually pinned attention mark. Unlike tabUnseen, the ACTIVE tab also
// reports true — a pin is an explicit "don't let me forget", not a
// seen/unseen state — except when the pinned pane is the focused pane of
// the active tab (the user is looking straight at it).
func (m Model) tabPinnedAttention(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) || m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Leaves() {
		if p == nil || !p.pinnedAttention {
			continue
		}
		if idx == m.activeTab && p.ID == m.tabs[idx].ActivePane {
			continue
		}
		return true
	}
	return false
}
```

In `internal/tui/model.go` `tabStyle` (~line 3031), change:

```go
	if !active && m.tabUnseen(idx) {
		return unseenTabStyle
	}
```
to:

```go
	// tabUnseen self-excludes the active tab; tabPinnedAttention deliberately
	// does not (a pin colors the active tab's label unless the pinned pane is
	// the one in focus).
	if m.tabUnseen(idx) || m.tabPinnedAttention(idx) {
		return unseenTabStyle
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: targeted docker command with `-run 'TestPinnedAttention|TestTabPinnedAttention|TestRenderKey_Includes|TestView_Pinned'`
Expected: PASS. If `TestView_Pinned` fails on the exact SGR text, print `p.View()` and match the actual color encoding lipgloss emits for `28`/`39` (may be `38;5;28` or a profile-dependent form) — adjust the assertion, not the color.

- [ ] **Step 5: Run the full TUI package to catch renderKey regressions**

Run: targeted docker command with `-run '.*'` on `./internal/tui/`
Expected: PASS (existing render-cache tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/pane.go internal/tui/workstate.go internal/tui/model.go internal/tui/workstate_pinned_test.go
git commit -m "feat(tui): pinned attention mark and menu-target border state"
```

---

### Task 3: Menu widget core — `ctxmenu.go`

**Files:**
- Create: `internal/tui/ctxmenu.go`
- Modify: `internal/tui/model.go` (add `ctxMenu ctxMenuState` field to the `Model` struct, ~line 213)
- Test: `internal/tui/ctxmenu_test.go` (new)

**Interfaces:**
- Consumes: `m.pluginRegistry.Get(name) *plugin.PanePlugin` (fields `.Available`, `.Command.RecordHistory`), `PaneModel.{Muted, Type, pinnedAttention}` (Task 2).
- Produces (Task 4/5 rely on these exact names):
  - `type ctxMenuAction int` with constants `ctxActHistory, ctxActFocus, ctxActNotes, ctxActLazygit, ctxActRename, ctxActMute, ctxActAttention, ctxActRestart, ctxActClose`
  - `type ctxMenuItem struct { id ctxMenuAction; label string; enabled bool }`
  - `type ctxMenuState struct { paneID string; x, y int; cursor int; items []ctxMenuItem }` with method `open() bool`
  - `(m *Model) buildCtxMenuItems(pane *PaneModel) []ctxMenuItem`
  - `ctxMenuBoxSize(items []ctxMenuItem) (w, h int)`
  - `ctxMenuPos(anchorX, anchorY, boxW, boxH, screenW, screenH int) (int, int)`
  - `ctxMenuHitRow(s ctxMenuState, x, y int) (row int, inside bool)`
  - `firstEnabled(items []ctxMenuItem) int`, `nextEnabled(items []ctxMenuItem, cur, dir int) int`
  - `renderCtxMenu(s ctxMenuState) string`

- [ ] **Step 1: Write the failing tests** (new file `internal/tui/ctxmenu_test.go`)

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func testItems() []ctxMenuItem {
	return []ctxMenuItem{
		{ctxActHistory, "Input history", false},
		{ctxActFocus, "Focus mode", true},
		{ctxActClose, "Close pane…", true},
	}
}

func TestCtxMenuPos_Clamping(t *testing.T) {
	t.Parallel()
	// Screen 100x40: content rows 1..38 (row 0 tab bar, row 39 status bar).
	for _, tc := range []struct {
		name                     string
		ax, ay, bw, bh           int
		wantX, wantY             int
	}{
		{"prefers cursor+1", 10, 10, 20, 8, 11, 11},
		{"right edge shifts left", 95, 10, 20, 8, 80, 11},
		{"bottom edge shifts up", 10, 36, 20, 8, 11, 31},
		{"top clamps to row 1", 10, -5, 20, 8, 11, 1},
		{"left clamps to col 0", -5, 10, 20, 8, 0, 11},
	} {
		x, y := ctxMenuPos(tc.ax, tc.ay, tc.bw, tc.bh, 100, 40)
		if x != tc.wantX || y != tc.wantY {
			t.Errorf("%s: got (%d,%d), want (%d,%d)", tc.name, x, y, tc.wantX, tc.wantY)
		}
	}
}

func TestCtxMenuBoxSize(t *testing.T) {
	t.Parallel()
	w, h := ctxMenuBoxSize(testItems())
	// Longest label "Input history" = 13 cells; +2 padding +2 border = 17.
	if w != 17 {
		t.Errorf("w = %d, want 17", w)
	}
	if h != 5 { // 3 items + 2 border rows
		t.Errorf("h = %d, want 5", h)
	}
}

func TestCtxMenuHitRow(t *testing.T) {
	t.Parallel()
	s := ctxMenuState{paneID: "p", x: 10, y: 5, items: testItems()}
	for _, tc := range []struct {
		name    string
		px, py  int
		row     int
		inside  bool
	}{
		{"outside left", 9, 6, -1, false},
		{"top border", 12, 5, -1, true},
		{"first item", 12, 6, 0, true},
		{"third item", 12, 8, 2, true},
		{"bottom border", 12, 9, -1, true},
		{"left border col", 10, 6, -1, true},
		{"below box", 12, 10, -1, false},
	} {
		row, inside := ctxMenuHitRow(s, tc.px, tc.py)
		if row != tc.row || inside != tc.inside {
			t.Errorf("%s: got (%d,%v), want (%d,%v)", tc.name, row, inside, tc.row, tc.inside)
		}
	}
}

func TestNextEnabled_SkipsDisabledAndWraps(t *testing.T) {
	t.Parallel()
	items := testItems() // 0 disabled, 1+2 enabled
	if got := firstEnabled(items); got != 1 {
		t.Errorf("firstEnabled = %d, want 1", got)
	}
	if got := nextEnabled(items, 1, +1); got != 2 {
		t.Errorf("down from 1 = %d, want 2", got)
	}
	if got := nextEnabled(items, 2, +1); got != 1 {
		t.Errorf("down from 2 wraps past disabled 0 to 1, got %d", got)
	}
	if got := nextEnabled(items, 1, -1); got != 2 {
		t.Errorf("up from 1 wraps past disabled 0 to 2, got %d", got)
	}
	none := []ctxMenuItem{{ctxActFocus, "x", false}}
	if got := firstEnabled(none); got != -1 {
		t.Errorf("all disabled: firstEnabled = %d, want -1", got)
	}
}

func TestBuildCtxMenuItems_LabelsAndGates(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.tabs[0].Root.Left.Pane
	pane.Muted = true
	pane.pinnedAttention = true

	items := m.buildCtxMenuItems(pane)
	if len(items) != 9 {
		t.Fatalf("item count = %d, want 9", len(items))
	}
	byID := map[ctxMenuAction]ctxMenuItem{}
	for _, it := range items {
		byID[it.id] = it
	}
	if byID[ctxActMute].label != "Unmute notifications" {
		t.Errorf("mute label = %q", byID[ctxActMute].label)
	}
	if byID[ctxActAttention].label != "Unmark attention" {
		t.Errorf("attention label = %q", byID[ctxActAttention].label)
	}
	// Test model has no plugin registry → both gated items disabled.
	if byID[ctxActHistory].enabled {
		t.Error("history should be disabled without RecordHistory plugin")
	}
	if byID[ctxActLazygit].enabled {
		t.Error("lazygit should be disabled without an available plugin")
	}
	if !byID[ctxActClose].enabled || !byID[ctxActFocus].enabled {
		t.Error("close/focus must always be enabled")
	}
}

func TestRenderCtxMenu_Dimensions(t *testing.T) {
	t.Parallel()
	s := ctxMenuState{paneID: "p", cursor: 1, items: testItems()}
	out := renderCtxMenu(s)
	lines := strings.Split(out, "\n")
	w, h := ctxMenuBoxSize(s.items)
	if len(lines) != h {
		t.Fatalf("rendered height = %d, want %d", len(lines), h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Errorf("line %d width = %d, want %d", i, got, w)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: targeted docker command with `-run 'TestCtxMenu|TestNextEnabled|TestBuildCtxMenu|TestRenderCtxMenu'`
Expected: FAIL — `undefined: ctxMenuItem` (compile error).

- [ ] **Step 3: Implement** (new file `internal/tui/ctxmenu.go`)

```go
package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ctxMenuAction identifies one entry in the pane context menu. Dispatch in
// executeCtxMenuItem (Task 5) routes each id into the SAME handler logic the
// keybinding cases use — the menu is a second dispatcher, never a second
// implementation.
type ctxMenuAction int

const (
	ctxActNone ctxMenuAction = iota
	ctxActHistory
	ctxActFocus
	ctxActNotes
	ctxActLazygit
	ctxActRename
	ctxActMute
	ctxActAttention
	ctxActRestart
	ctxActClose
)

// ctxMenuItem is one row of the menu. Disabled rows render greyed, are
// skipped by cursor movement, and are inert to clicks.
type ctxMenuItem struct {
	id      ctxMenuAction
	label   string
	enabled bool
}

// ctxMenuState is the live state of the pane context menu — a compositor
// overlay (overlayAt), NOT a dialogScreen: dialogs are modal and centered,
// this popup is positional and dismiss-on-outside-click. Zero value = closed.
type ctxMenuState struct {
	paneID string // target pane; "" = closed
	x, y   int    // clamped top-left of the rendered box (screen coords)
	cursor int    // index into items; always on an enabled item (or -1)
	items  []ctxMenuItem
}

func (s ctxMenuState) open() bool { return s.paneID != "" }

var (
	ctxMenuBorderStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39"))
	ctxMenuItemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	ctxMenuCursorStyle   = lipgloss.NewStyle().Reverse(true)
	ctxMenuDisabledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // same grey as uninstalled plugins in Ctrl+N
)

// buildCtxMenuItems resolves the 9 menu rows for a target pane. Labels are
// state-dependent (mute/attention toggles); gates mirror the keybinding
// handlers exactly: history needs the plugin's record_history opt-in (the
// kb.CommandHistory probe), lazygit needs an installed binary (the
// handleToggleLazygit availability gate).
func (m *Model) buildCtxMenuItems(pane *PaneModel) []ctxMenuItem {
	historyOK := false
	lazygitOK := false
	if m.pluginRegistry != nil {
		if p := m.pluginRegistry.Get(pane.Type); p != nil {
			historyOK = p.Command.RecordHistory
		}
		if p := m.pluginRegistry.Get("lazygit"); p != nil {
			lazygitOK = p.Available
		}
	}
	muteLabel := "Mute notifications"
	if pane.Muted {
		muteLabel = "Unmute notifications"
	}
	attnLabel := "Mark attention"
	if pane.pinnedAttention {
		attnLabel = "Unmark attention"
	}
	return []ctxMenuItem{
		{ctxActHistory, "Input history", historyOK},
		{ctxActFocus, "Focus mode", true},
		{ctxActNotes, "Open notes", true},
		{ctxActLazygit, "Open lazygit", lazygitOK},
		{ctxActRename, "Rename pane", true},
		{ctxActMute, muteLabel, true},
		{ctxActAttention, attnLabel, true},
		{ctxActRestart, "Restart pane…", true},
		{ctxActClose, "Close pane…", true},
	}
}

// ctxMenuInnerWidth is the content width: longest label + one space of
// padding on each side. lipgloss.Width is rune/wide-glyph aware.
func ctxMenuInnerWidth(items []ctxMenuItem) int {
	w := 0
	for _, it := range items {
		if lw := lipgloss.Width(it.label); lw > w {
			w = lw
		}
	}
	return w + 2
}

// ctxMenuBoxSize returns the rendered box dimensions including the border.
// MUST stay in lockstep with renderCtxMenu — ctxMenuPos and ctxMenuHitRow
// both derive geometry from it.
func ctxMenuBoxSize(items []ctxMenuItem) (w, h int) {
	return ctxMenuInnerWidth(items) + 2, len(items) + 2
}

// ctxMenuPos clamps the menu's top-left so the whole box stays inside the
// content area — rows 1..screenH-2 (row 0 is the tab bar, row screenH-1 the
// status bar), columns 0..screenW-1. Preferred position is one cell right and
// below the anchor so the mouse pointer does not cover the first item.
func ctxMenuPos(anchorX, anchorY, boxW, boxH, screenW, screenH int) (int, int) {
	x := anchorX + 1
	y := anchorY + 1
	if x+boxW > screenW {
		x = screenW - boxW
	}
	if x < 0 {
		x = 0
	}
	if maxY := screenH - 1 - boxH; y > maxY {
		y = maxY
	}
	if y < 1 {
		y = 1
	}
	return x, y
}

// ctxMenuHitRow maps a screen coordinate to a menu row. inside=false means
// the point is outside the box entirely; (-1, true) means inside the box but
// on the border, not on an item row.
func ctxMenuHitRow(s ctxMenuState, x, y int) (int, bool) {
	w, h := ctxMenuBoxSize(s.items)
	if x < s.x || x >= s.x+w || y < s.y || y >= s.y+h {
		return -1, false
	}
	row := y - s.y - 1
	if row < 0 || row >= len(s.items) || x == s.x || x == s.x+w-1 {
		return -1, true
	}
	return row, true
}

// firstEnabled returns the index of the first enabled item, or -1.
func firstEnabled(items []ctxMenuItem) int {
	for i, it := range items {
		if it.enabled {
			return i
		}
	}
	return -1
}

// nextEnabled returns the index of the next enabled item from cur in
// direction dir (+1 down, -1 up), wrapping past the ends and skipping
// disabled rows. A cursor of -1 (nothing enabled at open) resolves to
// firstEnabled; if no OTHER item is enabled the cursor stays put.
func nextEnabled(items []ctxMenuItem, cur, dir int) int {
	if len(items) == 0 {
		return -1
	}
	if cur < 0 {
		return firstEnabled(items)
	}
	for i := 1; i <= len(items); i++ {
		idx := ((cur+dir*i)%len(items) + len(items)) % len(items)
		if items[idx].enabled {
			return idx
		}
	}
	return cur
}

// renderCtxMenu draws the menu box. Every content line is padded to exactly
// ctxMenuInnerWidth so the border renders a straight right edge and
// ctxMenuBoxSize's geometry matches the output cell-for-cell.
func renderCtxMenu(s ctxMenuState) string {
	innerW := ctxMenuInnerWidth(s.items)
	rows := make([]string, len(s.items))
	for i, it := range s.items {
		label := " " + it.label + strings.Repeat(" ", innerW-lipgloss.Width(it.label)-2) + " "
		switch {
		case !it.enabled:
			rows[i] = ctxMenuDisabledStyle.Render(label)
		case i == s.cursor:
			rows[i] = ctxMenuCursorStyle.Render(label)
		default:
			rows[i] = ctxMenuItemStyle.Render(label)
		}
	}
	return ctxMenuBorderStyle.Render(strings.Join(rows, "\n"))
}
```

- [ ] **Step 4: Add the Model field**

In `internal/tui/model.go`, inside `type Model struct` (~line 213, near the other transient interaction state such as `splitDragNode` — search for `splitDragNode` in the struct and add adjacent):

```go
	// ctxMenu is the pane context menu overlay (right-click / quick_actions).
	// Zero value = closed. Not a dialogScreen — see ctxmenu.go.
	ctxMenu ctxMenuState
```

- [ ] **Step 5: Run tests to verify they pass**

Run: targeted docker command with `-run 'TestCtxMenu|TestNextEnabled|TestBuildCtxMenu|TestRenderCtxMenu'`
Expected: PASS. If `TestCtxMenuBoxSize` fails: `lipgloss.Width` of "…" (ellipsis) is 1 cell — recount the longest label and fix the expected constant in the TEST, not the implementation. If `TestBuildCtxMenuItems_LabelsAndGates` panics on a non-nil-but-empty registry in `newModelForTest`, replace the two `enabled` assertions with the actual gate outcome for that fixture (the invariant under test is "gates follow the registry", not a specific fixture shape).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/ctxmenu.go internal/tui/ctxmenu_test.go internal/tui/model.go
git commit -m "feat(tui): context menu widget core (items, layout, render)"
```

---

### Task 4: Extract per-pane action handlers (pure refactor)

**Files:**
- Modify: `internal/tui/model.go` (case bodies at ~2137 ClosePane, ~2148 RestartPane, ~2187 RenamePane, ~2291 FocusPane, ~2056 CommandHistory)

**Interfaces:**
- Produces (Task 5's dispatch calls these — exact signatures):
  - `(m Model) openClosePaneConfirm() (tea.Model, tea.Cmd)`
  - `(m Model) openRestartPaneConfirm() (tea.Model, tea.Cmd)`
  - `(m Model) beginPaneRename() (tea.Model, tea.Cmd)`
  - `(m Model) toggleFocusForActiveTab() (tea.Model, tea.Cmd)`
  - `(m Model) openHistoryForActivePane() (tea.Model, tea.Cmd)`
- All operate on the ACTIVE pane/tab — the menu dispatcher sets `tab.ActivePane` to the target before calling them.

- [ ] **Step 1: Extract the five methods**

Add near the other Model action methods in `internal/tui/model.go` (e.g. below `toggleNotesMode`, ~line 1650). Each body is MOVED verbatim from its case; the case becomes a one-line delegation. The extraction exists so the context menu (Task 5) reuses the exact same logic.

```go
// openClosePaneConfirm opens the close-pane confirm dialog for the active
// pane. Extracted from the kb.ClosePane case; shared with the context menu.
func (m Model) openClosePaneConfirm() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			m.dialog = dialogConfirm
			m.confirmKind = "pane"
			m.confirmID = pane.ID
			m.confirmName = paneDisplayName(pane)
		}
	}
	return m, tea.ClearScreen
}

// openRestartPaneConfirm opens the restart confirm dialog for the active
// pane. Extracted from the kb.RestartPane case; shared with the context menu.
func (m Model) openRestartPaneConfirm() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			m.dialog = dialogConfirm
			m.confirmKind = confirmKindRestartPane
			m.confirmID = pane.ID
			m.confirmName = paneDisplayName(pane)
		}
	}
	return m, tea.ClearScreen
}

// beginPaneRename enters inline pane-rename mode for the active pane.
// Extracted from the kb.RenamePane case; shared with the context menu.
func (m Model) beginPaneRename() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			m.renamingPane = true
			m.paneRenameInput = pane.Name
		}
	}
	return m, nil
}

// toggleFocusForActiveTab toggles focus mode on the active tab. Extracted
// from the kb.FocusPane case; shared with the context menu.
func (m Model) toggleFocusForActiveTab() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil && tab.Root != nil {
		tab.ToggleFocus()
		m.resizeTabs()
		return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
	}
	return m, nil
}

// openHistoryForActivePane opens the input-history modal for the active
// pane, gated on the plugin's record_history opt-in. Extracted from the
// kb.CommandHistory case; shared with the context menu.
func (m Model) openHistoryForActivePane() (tea.Model, tea.Cmd) {
	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}
	supported := false
	if p := m.pluginRegistry.Get(pane.Type); p != nil {
		supported = p.Command.RecordHistory
	}
	m = m.openHistoryDialog(pane.ID, pane.Type, supported)
	if supported {
		return m, m.requestHistory(pane.ID)
	}
	return m, nil
}
```

- [ ] **Step 2: Replace the five case bodies with delegations**

```go
	case kbMatches(key, kb.ClosePane):
		return m.openClosePaneConfirm()
```
```go
	case kbMatches(key, kb.RestartPane):
		return m.openRestartPaneConfirm()
```
```go
	case kbMatches(key, kb.RenamePane):
		return m.beginPaneRename()
```
```go
	case kbMatches(key, kb.FocusPane):
		return m.toggleFocusForActiveTab()
```
```go
	case kbMatches(key, kb.CommandHistory):
		return m.openHistoryForActivePane()
```

- [ ] **Step 3: Run the full TUI package — refactor must be behavior-neutral**

Run: targeted docker command with `-run '.*'` on `./internal/tui/`
Expected: PASS, zero test changes.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/model.go
git commit -m "refactor(tui): extract per-pane action handlers for reuse"
```

---

### Task 5: Integration — open/close, routing, dispatch, View composition

**Files:**
- Modify: `internal/tui/ctxmenu.go` (open/close/dispatch/key-handler methods)
- Modify: `internal/tui/model.go` (Update mouse cases ~550-730, handleKey ~1941-1952, first key switch ~2056, View ~1905, resizeTickMsg ~532, top-of-Update choke point at the `ackFocusedPane()` call, `paneRectAt` helper near `activePaneRect` ~1304)
- Modify: `internal/config/config.go` (line 233: QuickActions default)
- Modify: `internal/tui/dialog.go` (shortcuts list ~line 263)
- Test: `internal/tui/ctxmenu_integration_test.go` (new)

**Interfaces:**
- Consumes: everything produced by Tasks 1-4.
- Produces:
  - `(m *Model) openCtxMenu(pane *PaneModel, anchorX, anchorY int)`
  - `(m *Model) closeCtxMenu()`
  - `(m Model) handleCtxMenuKey(key string) (tea.Model, tea.Cmd)`
  - `(m Model) executeCtxMenuItem(item ctxMenuItem) (tea.Model, tea.Cmd)`
  - `(m Model) openQuickActionsMenu() (tea.Model, tea.Cmd)`
  - `(m *Model) paneRectAt(x, y int) *PaneRect`

- [ ] **Step 1: Write the failing integration tests** (new file `internal/tui/ctxmenu_integration_test.go`)

```go
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Fixture: newSplitDragTestModel — window 100x40, tab area rows 1..38,
// H-split p1 (cols 0-49) | p2 (cols 50-99), ActivePane p1.

func TestCtxMenu_RightClickOpensForPaneUnderCursor(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should open on right-click with no selection")
	}
	if got.ctxMenu.paneID != "p2" {
		t.Errorf("target = %q, want p2 (pane under cursor, not active pane)", got.ctxMenu.paneID)
	}
	if !got.tabs[0].Root.Right.Pane.ctxTargetHighlight {
		t.Error("target pane border highlight should be set")
	}
	// Position is clamped inside the content area.
	w, h := ctxMenuBoxSize(got.ctxMenu.items)
	if got.ctxMenu.x+w > 100 || got.ctxMenu.y+h > 39 || got.ctxMenu.y < 1 {
		t.Errorf("menu box (%d,%d,%dx%d) escapes the content area", got.ctxMenu.x, got.ctxMenu.y, w, h)
	}
}

func TestCtxMenu_RightClickWithSelectionCopiesInstead(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.selection = &Selection{PaneID: "p1"}
	updated, _ := m.Update(tea.MouseClickMsg{X: 30, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu must NOT open while a selection is active (copy wins)")
	}
	if got.selection != nil {
		t.Error("right-click should consume the selection (copy path)")
	}
}

func TestCtxMenu_LeftClickOutsideCloses(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.Update(tea.MouseClickMsg{X: 90, Y: 30, Button: tea.MouseLeft})
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("outside left-click should close the menu")
	}
	if got.mouseDown {
		t.Error("the closing click must be swallowed, not arm a selection drag")
	}
	if got.tabs[0].Root.Left.Pane.ctxTargetHighlight {
		t.Error("target highlight should clear on close")
	}
}

func TestCtxMenu_RightClickElsewhereRetargets(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.Update(tea.MouseClickMsg{X: 70, Y: 20, Button: tea.MouseRight})
	got = updated.(Model)
	if got.ctxMenu.paneID != "p2" {
		t.Errorf("retarget: paneID = %q, want p2", got.ctxMenu.paneID)
	}
	if got.tabs[0].Root.Left.Pane.ctxTargetHighlight {
		t.Error("old target highlight should be cleared on retarget")
	}
	if !got.tabs[0].Root.Right.Pane.ctxTargetHighlight {
		t.Error("new target highlight should be set")
	}
}

func TestCtxMenu_KeyNavigationAndEsc(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	start := got.ctxMenu.cursor
	updated, _ = got.handleCtxMenuKey("down")
	got = updated.(Model)
	if got.ctxMenu.cursor == start {
		t.Error("down should move the cursor")
	}
	updated, _ = got.handleCtxMenuKey("esc")
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("esc should close the menu")
	}
}

func TestCtxMenu_QuitPassesThrough(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	_, cmd := got.handleCtxMenuKey("ctrl+q")
	if cmd == nil {
		t.Fatal("quit must never be swallowed by the menu")
	}
}

func TestCtxMenu_ExecuteClose_SwitchesTargetAndOpensConfirm(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActClose, label: "Close pane…", enabled: true})
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu should close on execute")
	}
	if got.tabs[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2 (dispatch focuses the target first)", got.tabs[0].ActivePane)
	}
	if got.dialog != dialogConfirm || got.confirmKind != "pane" || got.confirmID != "p2" {
		t.Errorf("close confirm not armed for p2: dialog=%v kind=%q id=%q", got.dialog, got.confirmKind, got.confirmID)
	}
}

func TestCtxMenu_ExecuteAttention_TogglesPin(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActAttention, label: "Mark attention", enabled: true})
	got = updated.(Model)
	if !got.tabs[0].Root.Right.Pane.pinnedAttention {
		t.Error("attention pin should be set on p2")
	}
}

func TestCtxMenu_QuickActionsOpensForActivePane(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.openQuickActionsMenu()
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != "p1" {
		t.Errorf("quick actions should target the active pane, got %q", got.ctxMenu.paneID)
	}
	// Suppressed in notes mode.
	m2 := newSplitDragTestModel(t)
	m2.notesMode = true
	updated, _ = m2.openQuickActionsMenu()
	if updated.(Model).ctxMenu.open() {
		t.Error("quick actions must be a no-op in notes mode")
	}
}

func TestCtxMenu_VanishedTargetClosesOnNextMessage(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	// Simulate daemon reconciliation pruning p2.
	got.tabs[0].Root = NewLeaf(got.tabs[0].Root.Left.Pane)
	got.tabs[0].ActivePane = "p1"
	updated, _ = got.Update(tea.MouseMotionMsg{X: 1, Y: 1})
	if updated.(Model).ctxMenu.open() {
		t.Error("menu must close when its target pane no longer exists")
	}
}

func TestCtxMenu_WheelAndMotionSwallowedWhileOpen(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	before := got.ctxMenu
	updated, _ = got.Update(tea.MouseMotionMsg{X: 90, Y: 30}) // outside box
	got = updated.(Model)
	if !got.ctxMenu.open() {
		t.Error("motion outside must not close the menu")
	}
	if got.mouseDown || got.scrollDragPaneID != "" {
		t.Error("motion while open must not feed any drag")
	}
	_ = before
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: targeted docker command with `-run 'TestCtxMenu_'`
Expected: FAIL — `undefined: openQuickActionsMenu` etc. (compile errors), then behavioral failures.

- [ ] **Step 3: Implement open/close/dispatch/key-handler** (append to `internal/tui/ctxmenu.go`)

```go
// openCtxMenu opens (or re-targets) the context menu for pane, anchored at
// the given screen coordinate. Closes any previous menu first (clearing the
// old target's highlight), kills in-flight drags (one interaction at a
// time), and drops any live selection — the menu owns the mouse now.
func (m *Model) openCtxMenu(pane *PaneModel, anchorX, anchorY int) {
	m.closeCtxMenu()
	m.clearDragState()
	m.selection = nil
	items := m.buildCtxMenuItems(pane)
	w, h := ctxMenuBoxSize(items)
	x, y := ctxMenuPos(anchorX, anchorY, w, h, m.width, m.height)
	m.ctxMenu = ctxMenuState{
		paneID: pane.ID,
		x:      x,
		y:      y,
		cursor: firstEnabled(items),
		items:  items,
	}
	pane.ctxTargetHighlight = true
}

// closeCtxMenu closes the menu and clears the target-pane highlight. Safe to
// call when already closed; nil-safe when the target pane has vanished.
func (m *Model) closeCtxMenu() {
	if m.ctxMenu.paneID != "" {
		if pane, _ := m.findPaneAndTab(m.ctxMenu.paneID); pane != nil {
			pane.ctxTargetHighlight = false
		}
	}
	m.ctxMenu = ctxMenuState{}
}

// openQuickActionsMenu is the keyboard entry point (kb.QuickActions): same
// menu as right-click, for the ACTIVE pane, anchored at its content
// top-left. No-op in notes mode — the key is notes-exempt so it reaches
// here, but the menu's actions restructure the layout out from under the
// editor.
func (m Model) openQuickActionsMenu() (tea.Model, tea.Cmd) {
	if m.notesMode {
		return m, nil
	}
	if rect := m.activePaneRect(); rect != nil && rect.Pane != nil {
		m.openCtxMenu(rect.Pane, rect.OX+1, rect.OY+1)
	}
	return m, nil
}

// handleCtxMenuKey captures keyboard input while the menu is open. Quit is
// the only global that passes through — everything else is either menu
// navigation or swallowed (the menu is short-lived; no exempt list).
func (m Model) handleCtxMenuKey(key string) (tea.Model, tea.Cmd) {
	kb := m.cfg.Keybindings
	switch {
	case key == "esc":
		m.closeCtxMenu()
		return m, nil
	case key == "up" || key == "k":
		m.ctxMenu.cursor = nextEnabled(m.ctxMenu.items, m.ctxMenu.cursor, -1)
		return m, nil
	case key == "down" || key == "j":
		m.ctxMenu.cursor = nextEnabled(m.ctxMenu.items, m.ctxMenu.cursor, +1)
		return m, nil
	case key == "enter":
		if c := m.ctxMenu.cursor; c >= 0 && c < len(m.ctxMenu.items) && m.ctxMenu.items[c].enabled {
			return m.executeCtxMenuItem(m.ctxMenu.items[c])
		}
		return m, nil
	case kbMatches(key, kb.Quit):
		m.closeCtxMenu()
		return m, tea.Quit
	}
	return m, nil
}

// executeCtxMenuItem closes the menu, focuses the target pane (TUI-local,
// mirroring the setActivePaneMsg handler), and dispatches to the SAME
// handler logic the keybinding cases use. Destructive items keep their
// confirm dialogs.
func (m Model) executeCtxMenuItem(item ctxMenuItem) (tea.Model, tea.Cmd) {
	paneID := m.ctxMenu.paneID
	m.closeCtxMenu()
	if !item.enabled || paneID == "" {
		return m, nil
	}
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || tab.Root.FindLeaf(paneID) == nil {
		return m, nil // target vanished between open and execute
	}
	tab.ActivePane = paneID

	switch item.id {
	case ctxActHistory:
		return m.openHistoryForActivePane()
	case ctxActFocus:
		return m.toggleFocusForActiveTab()
	case ctxActNotes:
		return m.toggleNotesMode()
	case ctxActLazygit:
		return m, m.handleToggleLazygit()
	case ctxActRename:
		return m.beginPaneRename()
	case ctxActMute:
		return m, m.toggleActivePaneMute()
	case ctxActAttention:
		if pane, _ := m.findPaneAndTab(paneID); pane != nil {
			pane.pinnedAttention = !pane.pinnedAttention
		}
		return m, nil
	case ctxActRestart:
		return m.openRestartPaneConfirm()
	case ctxActClose:
		return m.openClosePaneConfirm()
	}
	return m, nil
}
```

Add the imports `ctxmenu.go` now needs: `tea "charm.land/bubbletea/v2"`.

- [ ] **Step 4: Add `paneRectAt`** (in `internal/tui/model.go`, directly below `activePaneRect` ~line 1322)

```go
// paneRectAt returns the rendered pane rect containing screen coordinate
// (x, y) in the active tab, or nil. Focus mode resolves to the single
// full-area rect; split layouts walk the same CollectRects geometry the
// scrollbar and border hit-tests use.
func (m *Model) paneRectAt(x, y int) *PaneRect {
	if r := m.activePaneRectFocus(); r != nil {
		if x >= r.OX && x < r.OX+r.W && y >= r.OY && y < r.OY+r.H {
			return r
		}
		return nil
	}
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil {
		return nil
	}
	tabH := m.height - chromeHeight
	notesW := m.notesPanelWidth()
	var rects []PaneRect
	tab.Root.CollectRects(0, 1, m.width-notesW, tabH, &rects)
	for i := range rects {
		r := &rects[i]
		if r.Pane != nil && x >= r.OX && x < r.OX+r.W && y >= r.OY && y < r.OY+r.H {
			return r
		}
	}
	return nil
}
```

- [ ] **Step 5: Wire the mouse routing in `Update`**

(a) In `case tea.MouseClickMsg:` — insert AFTER the `sidebarSwallowsMouse` swallow (~line 566) and BEFORE the right-click copy block:

```go
		// Context menu open: it owns the mouse. Click on an enabled row
		// executes; anywhere else inside the box is swallowed; outside
		// closes — and an outside RIGHT-click falls through to the open
		// path below so it re-targets in one gesture (OS-menu convention).
		if m.ctxMenu.open() {
			if row, inside := ctxMenuHitRow(m.ctxMenu, msg.X, msg.Y); inside {
				if msg.Button == tea.MouseLeft && row >= 0 && m.ctxMenu.items[row].enabled {
					return m.executeCtxMenuItem(m.ctxMenu.items[row])
				}
				return m, nil
			}
			m.closeCtxMenu()
			if msg.Button != tea.MouseRight {
				return m, nil // closing click is consumed, never arms a drag
			}
		}
```

(b) At the END of the existing `if msg.Button == tea.MouseRight {` block (~line 603, after the `if m.selection != nil` block falls through), append:

```go
			// No selection anywhere: open the pane context menu for the
			// pane under the cursor. Suppressed while a modal dialog,
			// rename edit, or notes mode owns input (the lazygit overlay
			// and sidebar swallows already returned above).
			if m.dialog == dialogNone && !m.notesMode && !m.renaming && !m.renamingPane {
				if rect := m.paneRectAt(msg.X, msg.Y); rect != nil && rect.Pane != nil {
					m.openCtxMenu(rect.Pane, msg.X, msg.Y)
				}
			}
			return m, nil
```

(c) In `case tea.MouseMotionMsg:` — insert after the overlay swallow (~line 677):

```go
		// Context menu open: hover moves the cursor; everything else is
		// swallowed so no drag can advance underneath the popup.
		if m.ctxMenu.open() {
			if row, inside := ctxMenuHitRow(m.ctxMenu, msg.X, msg.Y); inside && row >= 0 && m.ctxMenu.items[row].enabled {
				m.ctxMenu.cursor = row
			}
			return m, nil
		}
```

(d) In `case tea.MouseReleaseMsg:` — insert after the overlay swallow (~line 727):

```go
		if m.ctxMenu.open() {
			return m, nil // no drags can be live while the menu is open
		}
```

(e) In the `tea.MouseWheelMsg` case — insert at the top (find `case tea.MouseWheelMsg:`; add before any forwarding/scroll logic):

```go
		if m.ctxMenu.open() {
			return m, nil // wheel is swallowed while the menu is open
		}
```

- [ ] **Step 6: Wire the keyboard routing**

(a) In `handleKey` (~line 1952), AFTER the `m.renamingPane` branch and BEFORE the notes-mode branch:

```go
	// Context menu open: it captures navigation until closed. Quit passes
	// through inside the handler (never swallow quit).
	if m.ctxMenu.open() {
		return m.handleCtxMenuKey(key)
	}
```

(b) In the FIRST key switch, after the `kb.CommandHistory` case (~line 2073):

```go
	case kbMatches(key, kb.QuickActions):
		return m.openQuickActionsMenu()
```

- [ ] **Step 7: Wire lifecycle closes**

(a) Top-of-Update choke point — find the `m.ackFocusedPane()` call at the top of `Update` and add directly after it:

```go
	// A context menu whose target pane vanished (daemon reconciliation,
	// pane destroy) closes itself. Single choke point — no need to audit
	// every pruning path. findPaneAndTab is nil-safe.
	if m.ctxMenu.open() {
		if pane, _ := m.findPaneAndTab(m.ctxMenu.paneID); pane == nil {
			m.closeCtxMenu()
		}
	}
```

(b) `case resizeTickMsg:` (~line 532) — after the stale-seq guard, add:

```go
		// Anchor coordinates are stale after a reflow — cheapest correct
		// answer is to close.
		m.closeCtxMenu()
```

- [ ] **Step 8: Wire View composition** (in `View()`, ~line 1908, directly after the sidebar `overlayRight` block, before `sections = append(sections, tabContent)`)

```go
			if m.ctxMenu.open() {
				// ctxMenu coords are screen rows; tabContent starts at
				// screen row 1 (tab bar above), so shift by -1.
				tabContent = overlayAt(tabContent, renderCtxMenu(m.ctxMenu), m.ctxMenu.x, m.ctxMenu.y-1, m.width)
			}
```

- [ ] **Step 9: Rebind the default + shortcuts help row**

(a) `internal/config/config.go` line 233 — change:

```go
			QuickActions:       "ctrl+a",
```
to:

```go
			// alt+a (NOT the historical ctrl+a placeholder): ctrl+a is
			// readline beginning-of-line in every shell and the common tmux
			// prefix — stealing it from the PTY would be a regression. The
			// Alt layer matches the other pane-level shortcuts.
			QuickActions: "alt+a",
```

(b) `internal/tui/dialog.go` shortcuts list (~line 263, next to the Close pane row) — add:

```go
		{kbDisplay(kb.QuickActions), "Pane context menu (also mouse right-click)"},
```

- [ ] **Step 10: Run the integration tests**

Run: targeted docker command with `-run 'TestCtxMenu_'`
Expected: PASS (all 11). Common failures and their meaning:
- `RightClickOpensForPaneUnderCursor` targets p1 instead of p2 → `paneRectAt` used stale geometry; confirm the fixture called `tab.Resize(100, 38)` and rects are collected with `oy=1`.
- `VanishedTargetCloses` fails → the choke-point guard is below the message switch instead of at the top of `Update`.

- [ ] **Step 11: Run the full suite**

Run: `./scripts/dev.sh test`
Expected: PASS across all packages (config default change can affect config tests — if a test asserts `ctrl+a`, update that expectation to `alt+a`).

- [ ] **Step 12: Commit**

```bash
git add internal/tui/ctxmenu.go internal/tui/ctxmenu_integration_test.go internal/tui/model.go internal/config/config.go internal/tui/dialog.go
git commit -m "feat(tui): right-click pane context menu with quick-actions key"
```

---

### Task 6: Docs + project notes + final verification

**Files:**
- Modify: `docs/keybindings.md`
- Modify: `docs/features.md`
- Modify: `.claude/CLAUDE.md`

- [ ] **Step 1: Document the keybinding + mouse gesture**

In `docs/keybindings.md`: add `quick_actions` (`alt+a`) to the keybinding table ("Open the pane context menu for the active pane"), and in the mouse section (or create a "Mouse" note near the right-click/copy description) document: *Right-click on a pane — with a text selection active, copies it (unchanged); with no selection, opens the pane context menu for the pane under the cursor. ↑/↓/Enter or click to execute, Esc or click outside to close, right-click another pane to re-target.* Match the file's existing table/format conventions.

- [ ] **Step 2: Document the feature**

In `docs/features.md`, add a short entry under the mouse/pane-management area: the 9 actions, the target-pane highlight, greyed items (input history on non-AI panes, lazygit without the binary), and the pinned attention mark (green border that persists until unmarked; session-only).

- [ ] **Step 3: Update project notes**

In `.claude/CLAUDE.md` Key Conventions, append one bullet:

```markdown
- Pane context menu: right-click on a pane (no selection active) or `quick_actions` (default `alt+a`; the M1 `ctrl+a` placeholder was rebound — ctrl+a is readline home) opens a per-pane action popup (`internal/tui/ctxmenu.go`) — a compositor overlay via `overlayAt` (compose.go), NOT a dialogScreen. Targets the pane under the cursor (blue `ctxTargetHighlight` border), dispatches into the existing keybinding handler methods (extracted: `openClosePaneConfirm`/`openRestartPaneConfirm`/`beginPaneRename`/`toggleFocusForActiveTab`/`openHistoryForActivePane`), disabled rows greyed (history without `record_history`, lazygit without binary). "Mark attention" sets `PaneModel.pinnedAttention` — same green border as `unseen` but survives focus (`ackFocusedPane` never clears it; `tabPinnedAttention` colors the tab label, including the active tab). Menu state is `Model.ctxMenu` (zero value = closed), vanished-target close guarded at the top of `Update`, closed on window resize. Right-click with a selection still copies (split-by-selection).
```

- [ ] **Step 4: Full verification**

Run: `./scripts/dev.sh test && ./scripts/dev.sh vet`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/keybindings.md docs/features.md .claude/CLAUDE.md
git commit -m "docs: pane context menu and quick_actions rebind"
```

---

## Post-plan checks (executor)

- Manual smoke (optional but recommended): `./scripts/dev.sh build` is broken by CRLF on this machine — use the direct docker build from the `dev-sh-crlf-build-blocker` memory, then `./scripts/quil-dev.ps1`, verify `[dev]` in the status bar, right-click a pane: menu appears near cursor, target border turns blue, Esc closes, `alt+a` opens for the active pane, close/restart still confirm.
- Out of scope (spec): persistence of the pin, tab-bar context menu, split items, per-plugin TOML menu items, right-click forwarding to mouse-tracking apps.
