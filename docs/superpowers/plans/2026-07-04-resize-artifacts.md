# Pane Resize Artifacts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop Quil from garbling claude-code panes on resize: sidebar becomes a compositor overlay (no pane resizes), daemon skips same-size PTY resizes, and AI panes get a clean screen on width changes.

**Architecture:** Three independent fixes from `docs/superpowers/specs/2026-07-04-resize-artifacts-design.md`. Fix 1 replaces the sidebar's `JoinHorizontal` layout slot with an ANSI-aware right-edge compositor (`overlayRight`) so `paneAreaWidth` stays constant. Fix 2 adds a last-applied-size guard in the daemon's `handleResizePane`, zeroed on PTY install. Fix 3 pushes an AI pane's visible screen into scrollback before a width-changing `ResizeVT` so the child's repaint lands on a blank screen.

**Tech Stack:** Go 1.25, Bubble Tea v2, `charmbracelet/x/ansi` (v0.11.7, already a dependency), `charmbracelet/x/vt`. Build/test via `./scripts/dev.sh` (Docker; no local Go).

## Global Constraints

- Build: `./scripts/dev.sh test` / `./scripts/dev.sh vet` (Docker-based; host has no Go).
- Commit style: imperative, ≤72-char first line, no AI attribution of any kind.
- **Dirty working tree:** `internal/tui/model.go` and `cmd/quil/main.go` carry unrelated WIP (input-ordering work, `internal/tui/input_order_test.go` untracked). Before committing any task that touches `internal/tui/model.go`, run `git diff internal/tui/model.go` and stage ONLY if the pre-existing hunks are absent; otherwise defer that file's commit and flag to the user at the end. Never `git add -A`.
- Do NOT commit `docs/superpowers/` spec/plan files (user rule: no intermediate/spec commits).
- Go: tabs, gofmt, table-driven tests, `TestFunc_Scenario_Expected` naming.

---

### Task 1: `overlayRight` compositor

**Files:**
- Create: `internal/tui/compose.go`
- Test: `internal/tui/compose_test.go`

**Interfaces:**
- Produces: `overlayRight(base, overlay string, totalW, overlayW int) string` — used by Task 2's `View()` wiring.

- [ ] **Step 1: Write the failing tests**

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOverlayRight_PlainLines(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"   // 10 wide
	overlay := "XXX\nYYY"              // 3 wide
	got := overlayRight(base, overlay, 10, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if w := ansi.StringWidth(lines[0]); w != 10 {
		t.Errorf("line 0 width = %d, want 10", w)
	}
	if !strings.HasSuffix(lines[0], "XXX") {
		t.Errorf("line 0 = %q, want suffix XXX", lines[0])
	}
	if stripped := ansi.Strip(lines[0]); stripped != "aaaaaaaXXX" {
		t.Errorf("line 0 stripped = %q, want aaaaaaaXXX", stripped)
	}
}

func TestOverlayRight_ANSIStyledBase_NoBleed(t *testing.T) {
	// Red-styled base line: the style must not bleed into the overlay text.
	base := "\x1b[31m" + strings.Repeat("r", 10) + "\x1b[0m"
	got := overlayRight(base, "XX", 10, 2)
	if !strings.Contains(got, "\x1b[0mXX") {
		t.Errorf("overlay not preceded by SGR reset: %q", got)
	}
	if stripped := ansi.Strip(got); stripped != "rrrrrrrrXX" {
		t.Errorf("stripped = %q, want rrrrrrrrXX", stripped)
	}
}

func TestOverlayRight_WideGlyphAtCut_PadsToWidth(t *testing.T) {
	// 4 CJK glyphs = 8 cells; cut at 7 leaves 6 cells + 1 pad space.
	base := "你好世界xx" // 10 cells
	got := overlayRight(base, "ZZZ", 10, 3)
	if w := ansi.StringWidth(got); w != 10 {
		t.Errorf("width = %d, want 10", w)
	}
	if stripped := ansi.Strip(got); stripped != "你好世 ZZZ" {
		t.Errorf("stripped = %q, want %q", stripped, "你好世 ZZZ")
	}
}

func TestOverlayRight_ShortBaseLine_PaddedBeforeOverlay(t *testing.T) {
	got := overlayRight("ab", "XX", 10, 2)
	if stripped := ansi.Strip(got); stripped != "ab      XX" {
		t.Errorf("stripped = %q, want %q", stripped, "ab      XX")
	}
}

func TestOverlayRight_OverlayShorterThanBase_BlankFill(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	got := overlayRight(base, "XX", 10, 2)
	lines := strings.Split(got, "\n")
	if stripped := ansi.Strip(lines[2]); stripped != "cccccccc  " {
		t.Errorf("line 2 stripped = %q, want blank-covered tail", stripped)
	}
}

func TestOverlayRight_DegenerateWidths_ReturnsBase(t *testing.T) {
	base := "aaaa"
	if got := overlayRight(base, "X", 4, 0); got != base {
		t.Errorf("overlayW=0: got %q, want base", got)
	}
	if got := overlayRight(base, "X", 4, 4); got != base {
		t.Errorf("overlayW=totalW: got %q, want base", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test 2>&1 | grep -A2 "overlayRight\|OverlayRight\|FAIL"`
Expected: compile FAIL — `undefined: overlayRight`.

- [ ] **Step 3: Implement `internal/tui/compose.go`**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// overlayRight composites overlay onto the right edge of base. base is a
// block of totalW-wide lines; the rightmost overlayW columns of every base
// line are replaced by the matching overlay line (blank-filled when the
// overlay block is shorter). Used to draw the notification sidebar on TOP
// of the tab content instead of reserving layout width — panes keep full
// width, so toggling the sidebar never resizes a PTY (the root amplifier
// of the claude-code repaint artifacts; see
// docs/superpowers/specs/2026-07-04-resize-artifacts-design.md).
//
// ANSI-aware: the base line is truncated with ansi.Truncate (wide glyphs
// that straddle the cut are dropped and padded with a space) and closed
// with an SGR reset so base styling never bleeds into the overlay.
func overlayRight(base, overlay string, totalW, overlayW int) string {
	if overlayW <= 0 || overlayW >= totalW {
		return base
	}
	keepW := totalW - overlayW
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	blank := strings.Repeat(" ", overlayW)

	out := make([]string, len(baseLines))
	for i, bl := range baseLines {
		left := ansi.Truncate(bl, keepW, "")
		if pad := keepW - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := blank
		if i < len(overlayLines) {
			right = overlayLines[i]
		}
		out[i] = left + "\x1b[0m" + right
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -B2 -A2 "OverlayRight\|FAIL\|ok.*internal/tui"`
Expected: all `TestOverlayRight_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/compose.go internal/tui/compose_test.go
git commit -m "feat(tui): add ANSI-aware right-edge overlay compositor"
```

---

### Task 2: Sidebar becomes an overlay

**Files:**
- Modify: `internal/tui/model.go` (paneAreaWidth ~1074, notesSidebarWidth/notesPanelWidth ~1290-1324, notesEditorBox ~1328, View ~1534-1562, Alt+N handler ~1674-1688, MouseClickMsg ~516, MouseWheelMsg case)
- Modify: `internal/tui/notes_test.go:990-1035` (reservation tests)
- Test: `internal/tui/sidebar_overlay_test.go` (new)

**Interfaces:**
- Consumes: `overlayRight` from Task 1.
- Produces: `Model.sidebarOverlayWidth() int`, `Model.sidebarSwallowsMouse(x, y int) bool`; `Model.notesPanelWidth()` signature changes from `(notesW, sidebarW int)` to `int`.

- [ ] **Step 1: Write the failing tests** (`internal/tui/sidebar_overlay_test.go`)

```go
package tui

import "testing"

// newSidebarTestModel builds a minimal attached model at 200x50 with one tab.
func newSidebarTestModel() Model {
	m := NewModel(nil) // follow existing test constructor usage in model tests
	m.width = 200
	m.height = 50
	m.tabs = []*TabModel{NewTabModel("tab-1", "Shell")}
	m.activeTab = 0
	return m
}

func TestPaneAreaWidth_SidebarVisible_FullWidth(t *testing.T) {
	m := newSidebarTestModel()
	m.notifications.visible = true
	if got := m.paneAreaWidth(); got != 200 {
		t.Errorf("paneAreaWidth = %d, want 200 (sidebar must not reserve width)", got)
	}
}

func TestSidebarOverlayWidth_States(t *testing.T) {
	m := newSidebarTestModel()
	if got := m.sidebarOverlayWidth(); got != 0 {
		t.Errorf("hidden: got %d, want 0", got)
	}
	m.notifications.visible = true
	if got := m.sidebarOverlayWidth(); got != m.notifications.width {
		t.Errorf("visible: got %d, want %d", got, m.notifications.width)
	}
	m.dialog = dialogAbout
	if got := m.sidebarOverlayWidth(); got != 0 {
		t.Errorf("dialog open: got %d, want 0", got)
	}
	m.dialog = dialogNone
	m.width = m.notifications.width + minTermWidth - 1
	if got := m.sidebarOverlayWidth(); got != 0 {
		t.Errorf("too narrow: got %d, want 0", got)
	}
}

func TestSidebarOverlayWidth_FocusMode_StillVisible(t *testing.T) {
	m := newSidebarTestModel()
	m.notifications.visible = true
	m.tabs[0].focusMode = true
	if got := m.sidebarOverlayWidth(); got != m.notifications.width {
		t.Errorf("focus mode: got %d, want %d (overlay draws in focus mode too)", got, m.notifications.width)
	}
}

func TestSidebarSwallowsMouse_Regions(t *testing.T) {
	m := newSidebarTestModel()
	m.notifications.visible = true
	sw := m.notifications.width
	edge := m.width - sw
	cases := []struct {
		name string
		x, y int
		want bool
	}{
		{"inside sidebar", edge + 1, 5, true},
		{"left edge of sidebar", edge, 5, true},
		{"pane area", edge - 1, 5, false},
		{"tab bar row", edge + 1, 0, false},
		{"status bar row", edge + 1, m.height - 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.sidebarSwallowsMouse(tc.x, tc.y); got != tc.want {
				t.Errorf("sidebarSwallowsMouse(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
			}
		})
	}
	m.notifications.visible = false
	if m.sidebarSwallowsMouse(edge+1, 5) {
		t.Error("hidden sidebar must not swallow mouse")
	}
}
```

Adjust `newSidebarTestModel` to whatever constructor the existing model tests use (`grep -n "func TestUpdate_WindowSizeMsg" internal/tui/model_sizepoll_test.go` and copy its setup) — the plan's version is the shape, not gospel.

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test 2>&1 | grep -E "SidebarOverlay|SidebarSwallows|PaneAreaWidth|undefined|FAIL" | head -20`
Expected: compile FAIL — `undefined: m.sidebarOverlayWidth` / `m.sidebarSwallowsMouse`.

- [ ] **Step 3: Implement the model changes** (all in `internal/tui/model.go`)

3a. Replace `paneAreaWidth` (~line 1074):

```go
// paneAreaWidth returns the width available for pane content. The
// notification sidebar is a compositor overlay (overlayRight) — it does
// NOT reserve layout width, so panes never resize when it toggles. This
// constant width is what kills the sidebar-driven resize churn that made
// background claude panes repaint and garble their scrollback.
func (m Model) paneAreaWidth() int {
	return m.width
}
```

3b. Add `sidebarOverlayWidth` + `sidebarSwallowsMouse` right below it:

```go
// sidebarOverlayWidth returns the drawn width of the notification sidebar
// overlay, or 0 when it isn't drawn (hidden, a dialog is open, or the
// terminal is too narrow). Unlike the old reservation logic there is no
// focus-mode suppression: visible ⇒ drawn, over whatever is beneath.
func (m Model) sidebarOverlayWidth() int {
	if !m.notifications.visible || m.dialog != dialogNone {
		return 0
	}
	if m.width-m.notifications.width < minTermWidth {
		return 0
	}
	return m.notifications.width
}

// sidebarSwallowsMouse reports whether a mouse press/wheel at (x, y) lands
// on the sidebar overlay. Such events must not reach the pane rendered
// beneath it. Row 0 (tab bar) and the last row (status bar) are exempt;
// release events are also exempt at the call sites so an in-flight drag
// can always terminate.
func (m Model) sidebarSwallowsMouse(x, y int) bool {
	sw := m.sidebarOverlayWidth()
	return sw > 0 && x >= m.width-sw && y >= 1 && y < m.height-1
}
```

3c. Delete `notesSidebarWidth` (~1290-1305) and shrink `notesPanelWidth` (~1307-1324) to:

```go
// notesPanelWidth returns the notes panel width for the current model
// state. Returns 0 when notes mode is inactive or the terminal is too
// narrow to render the editor. The notification sidebar is an overlay and
// no longer reserves width here. Single source of truth for the layout
// math used by both View() and notesEditorBox.
func (m Model) notesPanelWidth() int {
	if !m.notesMode || m.notesEditor == nil {
		return 0
	}
	notesW := m.width * notesPanelWidthNumerator / notesPanelWidthDenominator
	if notesW < notesPanelMinWidth {
		notesW = notesPanelMinWidth
	}
	if m.width-notesW < minTermWidth {
		return 0
	}
	return notesW
}
```

3d. `notesEditorBox` (~1328): replace the two-value call and sidebar offsets:

```go
	notesW := m.notesPanelWidth()
	if notesW == 0 {
		return 0, 0, 0, 0, false
	}
	boxX0 = m.width - notesW
	boxY0 = 1 // y=0 is the tab bar
	boxX1 = m.width
	boxY1 = m.height - 1 // last row is the status bar
```

3e. `View()` (~1534-1562): update the comment block and composition:

```go
		// Active tab content + optional notes editor; the notification
		// sidebar is composited OVER the right edge afterwards
		// (overlayRight) — it takes no layout width. Layout math single
		// source of truth: notesPanelWidth / sidebarOverlayWidth.
		tabH := m.height - chromeHeight
		notesW := m.notesPanelWidth()
		if m.activeTab < len(m.tabs) {
			tab := m.tabs[m.activeTab]

			tab.Resize(m.width-notesW, tabH)
			// Pass per-frame state to panes for rendering
			if tab.Root != nil {
				for _, pane := range tab.Leaves() {
					pane.activeSel = m.selection
					pane.focusMode = tab.FocusMode() && pane.ID == tab.ActivePane
					pane.mcpHighlight = m.mcpHighlights[pane.ID]
				}
			}
			tabContent := tab.View()
			if notesW > 0 {
				editorFocused := !m.notesPaneFocused
				tabContent = lipgloss.JoinHorizontal(lipgloss.Top, tabContent, m.notesEditor.View(notesW, tabH, editorFocused))
			}
			if sw := m.sidebarOverlayWidth(); sw > 0 {
				m.notifications.focused = m.sidebarFocused
				tabContent = overlayRight(tabContent, m.notifications.View(tabH), m.width, sw)
			}
			sections = append(sections, tabContent)
		}
```

3f. Alt+N / Ctrl+Alt+N handlers (~1674-1688): drop `m.resizeAllPanes()`:

```go
	case kbMatches(key, kb.NotificationToggle):
		// Alt+N: toggle visibility only, never focus. The sidebar is an
		// overlay — no pane resize needed, only a full repaint.
		m.notifications.visible = !m.notifications.visible
		m.sidebarFocused = false
		if m.notifications.visible {
			return m, tea.Batch(tea.ClearScreen, m.startSidebarTick())
		}
		return m, tea.ClearScreen
	case kbMatches(key, kb.NotificationFocus):
		// Ctrl+Alt+N: open (if hidden) and focus sidebar
		if !m.notifications.visible {
			m.notifications.visible = true
		}
		m.sidebarFocused = true
		return m, tea.Batch(tea.ClearScreen, m.startSidebarTick())
```

3g. Mouse swallow — in `case tea.MouseClickMsg:` (~516), directly AFTER the overlay-visible swallow block (~522-525) and BEFORE the right-click branch:

```go
		// Sidebar overlay region: the press belongs to the sidebar, not
		// the pane rendered beneath it. Clear drag flags so no half-armed
		// drag survives the swallowed press.
		if m.sidebarSwallowsMouse(msg.X, msg.Y) {
			m.clearDragState()
			return m, nil
		}
```

In `case tea.MouseWheelMsg:` (grep `MouseWheelMsg` in model.go), first line of the case:

```go
		if m.sidebarSwallowsMouse(msg.X, msg.Y) {
			return m, nil
		}
```

Do NOT touch `MouseMotionMsg`/`MouseReleaseMsg` — captured drags must keep receiving motion and always see release.

3h. Fix `notes_test.go` call sites (995, 1014, 1032): `notesW, sidebarW := m.notesPanelWidth()` → `notesW := m.notesPanelWidth()`. The test around 1012 that asserts sidebar reservation changes meaning: with the overlay, `notesPanelWidth` must return the SAME value whether `m.notifications.visible` is true or false — rewrite the assertion to exactly that.

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test 2>&1 | tail -30`
Expected: full suite PASS, including new `TestSidebarOverlay*`/`TestSidebarSwallows*`/`TestPaneAreaWidth_*` and updated notes tests.

- [ ] **Step 5: Commit (respecting the dirty-tree constraint)**

```bash
git diff --stat internal/tui/model.go   # verify only this task's hunks would be staged
git add internal/tui/sidebar_overlay_test.go internal/tui/notes_test.go
# model.go: ONLY if free of unrelated WIP hunks — else defer, flag to user:
git add internal/tui/model.go
git commit -m "feat(tui): draw notification sidebar as overlay, not layout slot"
```

---

### Task 3: Daemon same-size resize guard

**Files:**
- Modify: `internal/daemon/session.go:50-51` (Pane struct, after Rows)
- Modify: `internal/daemon/daemon.go:1332-1347` (handleResizePane), `:2254-2256` (spawnPane PTY install)
- Test: `internal/daemon/resize_guard_test.go` (new)

**Interfaces:**
- Consumes: existing `fakeSession` (spawn_pane_test.go) with its `resizes [][2]uint16` recorder.
- Produces: unexported `Pane.appliedCols/appliedRows int` (PluginMu-guarded).

- [ ] **Step 1: Write the failing test** (`internal/daemon/resize_guard_test.go`)

```go
package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func resizeMsg(t *testing.T, paneID string, cols, rows uint16) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
		PaneID: paneID, Cols: cols, Rows: rows,
	})
	if err != nil {
		t.Fatalf("build resize msg: %v", err)
	}
	return msg
}

func TestHandleResizePane_DuplicateSize_SkipsPTYResize(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &fakeSession{}
	pane := &Pane{ID: "p1", PTY: fake}
	d.session.panes["p1"] = pane

	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	if len(fake.resizes) != 1 {
		t.Fatalf("PTY.Resize called %d times, want 1 (duplicate must be skipped)", len(fake.resizes))
	}
	d.handleResizePane(resizeMsg(t, "p1", 120, 40))
	if len(fake.resizes) != 2 {
		t.Fatalf("PTY.Resize called %d times, want 2 (changed size must apply)", len(fake.resizes))
	}
	if pane.Cols != 120 || pane.Rows != 40 {
		t.Errorf("pane size = %dx%d, want 120x40", pane.Cols, pane.Rows)
	}
}

func TestHandleResizePane_FreshPTY_AcceptsSameSize(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &fakeSession{}
	pane := &Pane{ID: "p1", PTY: fake}
	d.session.panes["p1"] = pane

	d.handleResizePane(resizeMsg(t, "p1", 100, 40))

	// Simulate restart: new PTY installed the way spawnPane does it.
	fake2 := &fakeSession{}
	pane.PluginMu.Lock()
	pane.PTY = fake2
	pane.appliedCols, pane.appliedRows = 0, 0
	pane.PluginMu.Unlock()

	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	if len(fake2.resizes) != 1 {
		t.Fatalf("fresh PTY got %d resizes, want 1 (guard must reset on PTY install)", len(fake2.resizes))
	}
}

func TestHandleResizePane_NilPTY_NoPanic(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	pane := &Pane{ID: "p1"}
	d.session.panes["p1"] = pane
	d.handleResizePane(resizeMsg(t, "p1", 100, 40)) // must not panic
	if pane.Cols != 0 {
		t.Errorf("pane.Cols = %d, want 0 (no PTY, nothing applied)", pane.Cols)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `./scripts/dev.sh test 2>&1 | grep -E "resize_guard|appliedCols|FAIL" | head`
Expected: compile FAIL — `pane.appliedCols undefined`.

- [ ] **Step 3: Implement**

3a. `internal/daemon/session.go` — add after `Rows` (line 51):

```go
	// appliedCols/appliedRows: size last applied to the CURRENT PTY by
	// handleResizePane. The TUI re-sends every pane's size on each
	// workspace broadcast; this guard turns the duplicates into no-ops.
	// Zeroed when a new PTY is installed (spawnPane) so a fresh PTY always
	// accepts its first resize. Guarded by PluginMu.
	appliedCols int
	appliedRows int
```

3b. `internal/daemon/daemon.go` — replace `handleResizePane` (1332-1347):

```go
func (d *Daemon) handleResizePane(msg *ipc.Message) {
	var payload ipc.ResizePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		return
	}

	pane := d.session.Pane(payload.PaneID)
	if pane == nil {
		return
	}
	// Same-size guard: skip when this exact size was already applied to
	// the current PTY. Fields are PluginMu-guarded; the Resize syscall
	// runs outside the lock.
	pane.PluginMu.Lock()
	pty := pane.PTY
	same := pane.appliedCols == int(payload.Cols) && pane.appliedRows == int(payload.Rows)
	if pty != nil && !same {
		pane.appliedCols = int(payload.Cols)
		pane.appliedRows = int(payload.Rows)
	}
	pane.PluginMu.Unlock()
	if pty == nil || same {
		return
	}
	if err := pty.Resize(payload.Rows, payload.Cols); err != nil {
		log.Printf("resize pane %s to %dx%d: %v", payload.PaneID, payload.Cols, payload.Rows, err)
	}
	pane.Cols = int(payload.Cols)
	pane.Rows = int(payload.Rows)
}
```

3c. `internal/daemon/daemon.go` spawnPane PTY install (2254-2256):

```go
	pane.PluginMu.Lock()
	pane.PTY = ptySession
	// Fresh PTY: reset the same-size guard so its first resize_pane is
	// always applied (see handleResizePane).
	pane.appliedCols, pane.appliedRows = 0, 0
	pane.PluginMu.Unlock()
```

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test 2>&1 | tail -20`
Expected: full suite PASS including the three new tests (`resize_kick_test.go` and `wedge_regression_test.go` must stay green).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/session.go internal/daemon/daemon.go internal/daemon/resize_guard_test.go
git commit -m "perf(daemon): skip PTY resize when size unchanged"
```

---

### Task 4: Clean screen on width change for AI panes

**Files:**
- Modify: `internal/tui/pane.go:307-320` (ResizeVT) + new helpers below it
- Test: `internal/tui/pane_resize_clear_test.go` (new)

**Interfaces:**
- Consumes: `lastContentLine(pane)` (selection.go:140), `p.screenBlank()` (pane.go, used by showRestoreIndicator), `p.vt.IsAltScreen()` (x/vt SafeEmulator).
- Produces: `clearsOnWidthResize(paneType string) bool`, `(*PaneModel).pushScreenToScrollback()`.

- [ ] **Step 1: Write the failing tests** (`internal/tui/pane_resize_clear_test.go`)

```go
package tui

import (
	"strings"
	"testing"
)

// aiPane builds a claude-code pane at 40x10 with content written at that width.
func aiPane(t *testing.T) *PaneModel {
	t.Helper()
	p := NewPaneModel("pane-clear-test", 4096)
	p.Type = "claude-code"
	p.ResizeVT(40, 10)
	p.AppendOutput([]byte("first line\r\nsecond line\r\n" + strings.Repeat("w", 60) + "\r\nprompt> "))
	return p
}

func screenText(p *PaneModel) string {
	var b strings.Builder
	for y := 0; y < p.vt.Height(); y++ {
		for x := 0; x < p.vt.Width(); x++ {
			if c := p.vt.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
	}
	return b.String()
}

func TestResizeVT_WidthChange_AIPane_CleansScreen(t *testing.T) {
	p := aiPane(t)
	if p.screenBlank() {
		t.Fatal("setup: screen must have content before resize")
	}
	sbBefore := p.vt.ScrollbackLen()

	p.ResizeVT(80, 10)

	if !p.screenBlank() {
		t.Errorf("screen not blank after width change:\n%q", screenText(p))
	}
	if p.vt.ScrollbackLen() <= sbBefore {
		t.Errorf("scrollback %d -> %d: old screen content must be pushed, not dropped",
			sbBefore, p.vt.ScrollbackLen())
	}
	if pos := p.vt.CursorPosition(); pos.X != 0 || pos.Y != 0 {
		t.Errorf("cursor at %d,%d, want 0,0 (homed)", pos.X, pos.Y)
	}
}

func TestResizeVT_WidthChange_ScrollbackKeepsContent(t *testing.T) {
	p := aiPane(t)
	p.ResizeVT(80, 10)
	// "first line" must now live in scrollback (checked via cell scan).
	found := false
	for y := 0; y < p.vt.ScrollbackLen() && !found; y++ {
		var b strings.Builder
		for x := 0; x < 40; x++ {
			if c := p.vt.ScrollbackCellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		if strings.Contains(b.String(), "first line") {
			found = true
		}
	}
	if !found {
		t.Error("pre-resize content missing from scrollback — data loss")
	}
}

func TestResizeVT_HeightOnlyChange_KeepsScreen(t *testing.T) {
	p := aiPane(t)
	p.ResizeVT(40, 20)
	if p.screenBlank() {
		t.Error("height-only change must not clear the screen")
	}
}

func TestResizeVT_TerminalPane_KeepsScreen(t *testing.T) {
	p := aiPane(t)
	p.Type = "terminal"
	p.ResizeVT(80, 10)
	if p.screenBlank() {
		t.Error("terminal panes must keep their screen on width change")
	}
}

func TestResizeVT_AltScreen_KeepsScreen(t *testing.T) {
	p := NewPaneModel("pane-alt-test", 4096)
	p.Type = "claude-code"
	p.ResizeVT(40, 10)
	p.AppendOutput([]byte("\x1b[?1049h\x1b[Halt screen content"))
	sbBefore := p.vt.ScrollbackLen()
	p.ResizeVT(80, 10)
	if p.vt.ScrollbackLen() != sbBefore {
		t.Error("altscreen pane must not push to scrollback on width change")
	}
}

func TestClearsOnWidthResize_Types(t *testing.T) {
	cases := map[string]bool{
		"claude-code": true,
		"opencode":    true,
		"terminal":    false,
		"":            false,
		"ssh":         false,
		"k9s":         false,
	}
	for typ, want := range cases {
		if got := clearsOnWidthResize(typ); got != want {
			t.Errorf("clearsOnWidthResize(%q) = %v, want %v", typ, got, want)
		}
	}
}
```

Note: verify `p.screenBlank()` exists and scans only the visible screen (`grep -n "func (p \*PaneModel) screenBlank" internal/tui/pane.go`); if its semantics differ, assert via `screenText(p)` being all spaces instead.

- [ ] **Step 2: Run to verify failure**

Run: `./scripts/dev.sh test 2>&1 | grep -E "clearsOnWidthResize|ResizeVT_Width|FAIL" | head`
Expected: compile FAIL — `undefined: clearsOnWidthResize`.

- [ ] **Step 3: Implement in `internal/tui/pane.go`**

3a. Extend `ResizeVT`:

```go
func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols <= 0 || rows <= 0 || (cols == p.vt.Width() && rows == p.vt.Height()) {
		return
	}
	// AI panes repaint their whole viewport when the child sees the resize;
	// without a clean screen that repaint lands BELOW the stale frame wrapped
	// at the old width — mixed-width text and duplicated transcript chunks.
	// Push the old frame into scrollback (honest history) so the repaint
	// draws on a blank screen. Terminal/ssh panes don't repaint on resize
	// (clearing would only hide context) and altscreen apps repaint in place.
	if cols != p.vt.Width() && clearsOnWidthResize(p.Type) && !p.vt.IsAltScreen() {
		p.pushScreenToScrollback()
	}
	// Resize the emulator in place instead of rebuilding it from the raw PTY
	// ring buffer. Historical bytes from TUI apps (Claude Code, vim, htop,
	// fzf) contain CUP / scroll-region sequences laid out for the previous
	// width; replaying them into a freshly-sized emulator stamps narrow-
	// column ghost rows into the new screen. The x/vt library's Resize
	// preserves the current screen state, and the PTY child will redraw via
	// SIGWINCH (triggered separately by MsgResizePane) into the new size.
	p.vt.Resize(cols, rows)
	p.contentGen++
}

// clearsOnWidthResize reports whether a pane type gets the
// push-screen-to-scrollback treatment on width-changing resizes. Only the
// agent plugins repaint their viewport on resize; shells/ssh do not, and
// full-screen TUIs (k9s, lazygit) are excluded by the IsAltScreen gate at
// the call site.
func clearsOnWidthResize(paneType string) bool {
	return paneType == "claude-code" || paneType == "opencode"
}

// pushScreenToScrollback scrolls the visible screen's content rows into
// the emulator's scrollback and leaves a blank, homed screen. The
// synthetic bytes go to the emulator only — the child never sees them, and
// its own cursor bookkeeping is unaffected (its post-resize repaint opens
// with absolute/relative positioning that clamps harmlessly on a blank
// screen).
func (p *PaneModel) pushScreenToScrollback() {
	if p.screenBlank() {
		return
	}
	sbLen := p.vt.ScrollbackLen()
	contentRows := lastContentLine(p) - sbLen + 1
	if contentRows <= 0 {
		return // content is all in scrollback already
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[%d;1H", p.vt.Height())        // park cursor on the bottom row
	b.WriteString(strings.Repeat("\n", contentRows))    // scroll content rows out
	b.WriteString("\x1b[2J\x1b[H")                      // clear remnants, home cursor
	_, _ = p.vt.Write([]byte(b.String()))
	p.contentGen++
}
```

(`fmt` and `strings` are already imported in pane.go — verify, add if missing.)

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test 2>&1 | tail -20`
Expected: full suite PASS including all `TestResizeVT_*` and `TestClearsOnWidthResize_Types`. Watch `pane_widechar_test.go`, `vt_claude_test.go`, `pane_cache_test.go` — they exercise ResizeVT paths (their panes have `Type == ""` so behavior is unchanged; a failure there means the gate leaks).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/pane.go internal/tui/pane_resize_clear_test.go
git commit -m "fix(tui): clean AI pane screen on width change"
```

---

### Task 5: Full verification + docs

**Files:**
- Modify: `.claude/CLAUDE.md` (M12/notification-center bullet: sidebar is an overlay now; note the AI-pane clean-on-width-resize behavior in the pane-cursor/rendering area)
- Modify: `docs/features.md` (only if it describes the sidebar squeezing panes — `grep -n "sidebar" docs/*.md`)

- [ ] **Step 1: Race + vet + full suite**

Run: `./scripts/dev.sh test-race && ./scripts/dev.sh vet`
Expected: PASS, no vet findings. The new PluginMu-guarded fields must be race-clean.

- [ ] **Step 2: Build all variants**

Run: `./scripts/dev.sh build`
Expected: 6 binaries build cleanly.

- [ ] **Step 3: Manual smoke test in dev mode** (per `.claude/rules/dev-environment.md` — never production)

1. `./scripts/quil-dev.ps1`, verify `[dev]` in the status bar.
2. Create a claude-code pane (or any AI pane) in a 2-split tab; generate output.
3. Alt+N toggle: sidebar draws over the right edge; panes do NOT reflow/repaint; toggle off — content identical.
4. Ctrl+E zoom the AI pane: screen shows only the fresh repaint (old narrow frame reachable by scrolling up); zoom out — same.
5. Click inside the open sidebar region: no pane selection/scroll happens beneath it.
6. Dev daemon log: repeated workspace broadcasts produce no `resize pane` errors; same-size storms absent.

- [ ] **Step 4: Update docs** (CLAUDE.md bullet edits per Files above)

- [ ] **Step 5: Final commit**

```bash
git add .claude/CLAUDE.md docs/features.md
git commit -m "docs: sidebar overlay + resize artifact notes"
```

If `internal/tui/model.go` was deferred in Task 2 because of unrelated WIP hunks, stop here and present the user the exact hunk split before any commit that includes it.

---

## Self-Review Notes

- Spec coverage: Fix 1 → Tasks 1-2; Fix 2 → Task 3; Fix 3 → Task 4; testing section → per-task tests + Task 5. Focus-mode-visible sidebar covered (Task 2 test + `sidebarOverlayWidth` has no FocusMode check). Notes-editor-covered-by-overlay covered (View composits sidebar AFTER notes join).
- Type consistency: `overlayRight(base, overlay string, totalW, overlayW int) string` used identically in Tasks 1-2; `appliedCols/appliedRows` int fields consistent between Task 3 steps; `clearsOnWidthResize`/`pushScreenToScrollback` defined and consumed within Task 4.
- Known judgment calls the implementer may hit: exact test-model constructor in Task 2 Step 1 (copy from existing model tests), `screenBlank` semantics in Task 4 (fallback assertion given), `fmt`/`strings` imports in pane.go.
