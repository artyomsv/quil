# Work-In-Progress Indicators Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show an animated "working" spinner on tab labels and pane top-borders while a Claude Code / OpenCode pane is mid-turn, and flash the tab label green for 5 s when that pane finishes — so the user can tell at a glance which background tab/pane is busy or just completed.

**Architecture:** The daemon already broadcasts every Claude/OpenCode hook event to the TUI as a `paneEventMsg` (`Type = "hook.<src>.<event>"`). The entire working/idle state machine therefore lives **TUI-side**, derived from that existing event stream — no new IPC field and (for Claude) no daemon change. A single shared 100 ms tick animates a braille spinner reused from the existing `resuming` indicator. The only backend change is adding OpenCode's missing "work started" edge (`chat.message`) to the embedded JS hook plugin; Claude already emits both edges (`UserPromptSubmit` → start, `Stop` → finish).

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2, embedded JS opencode plugin. Build/test via Docker (`./scripts/dev.sh`).

---

## Decisions & assumptions (v1)

These were settled during analysis; later tasks depend on them:

1. **Two states only: working / not-working.** Permission-waiting (`PermissionRequest` / `permission.ask`) counts as **still working** (the turn is "pending"). A separate "blocked" state is a future enhancement, deliberately out of scope.
2. **Tab cue = steady green *background* highlight for 5 s on *inactive* tabs.** The active tab never flashes (you're already looking at it). A literal box-border around a tab cell is rejected — it would shift tab widths and break click hit-testing. (Confirmed with user: "can we just flash tab label color instead?" → yes.)
3. **Spinner = the existing braille set** `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` (single-width BMP, conhost-safe). One shared frame index drives both the tab spinner and every pane spinner so they animate in sync.
4. **Pane spinner is drawn on the *left* of the top border, before the CWD path** (where the user asked for it). The pre-existing `resuming`/`preparing` spinner stays on the right and is untouched.
5. **`process_exit` clears `working` WITHOUT a green flash** — a crash/exit is not a completed turn. Only genuine completion events (`Stop`, `session.idle`, …) flash green.
6. **OpenCode "work started" = the `chat.message` typed hook** (fires when a user message is submitted — the true analog of Claude's `UserPromptSubmit`). Task 6 includes a runtime verification step because the exact hook name could not be confirmed from offline docs.

**Event → transition mapping (single source of truth, implemented in Task 1):**

| `paneEventMsg.Type` | Transition |
|---|---|
| `hook.claude.UserPromptSubmit` | **start** |
| `hook.opencode.chat.message` | **start** |
| `hook.claude.Stop` | **stop** (→ flash) |
| `hook.claude.SessionEnd` | **stop** (→ flash) |
| `hook.opencode.session.idle` | **stop** (→ flash) |
| `hook.opencode.session.error` | **stop** (→ flash) |
| `process_exit` | **abort** (clear, no flash) |
| anything else | none |

---

## File structure

| File | Responsibility | Change |
|---|---|---|
| `internal/tui/workstate.go` | **New.** Pure event classifier + Model helpers (`workEventKind`, `applyWorkTransition`, `anyPaneWorking`, `tabHasWorkingPane`, `tabFlashing`, `findPaneAndTab`). | Create |
| `internal/tui/workstate_test.go` | **New.** White-box tests for the classifier + transitions + helpers. | Create |
| `internal/tui/pane.go` | Add `working` + `workFrame` fields to `PaneModel`; left-side spinner in `buildTopBorder`. | Modify |
| `internal/tui/tab.go` | Add `flashUntil time.Time` to `TabModel`. | Modify |
| `internal/tui/styles.go` | Add `flashTabStyle` (green). | Modify |
| `internal/tui/model.go` | New msg types + fields; `paneEventMsg` wiring; `workSpinnerTickMsg`/`flashTickMsg` handlers; spinner in `tabLabel`; `tabStyle` helper used by `renderTabBar` + `hitTestTab`. | Modify |
| `internal/tui/border_test.go` | **New.** `buildTopBorder` left-spinner test. | Create |
| `internal/opencodehook/scripts/quil-session-tracker.js` | Add `chat.message` start-edge producer. | Modify |
| `.claude/CLAUDE.md` | Document the indicator under the M5/Notification architecture notes. | Modify |

No daemon, IPC, or persistence changes. Working state is intentionally **not persisted** — on restart a pane starts idle and the next hook event corrects it.

---

## Task 1: Work-state model — classifier, fields, transition

**Files:**
- Create: `internal/tui/workstate.go`
- Create: `internal/tui/workstate_test.go`
- Modify: `internal/tui/pane.go` (PaneModel struct, ~lines 24-47)
- Modify: `internal/tui/tab.go` (TabModel struct, ~lines 4-13)
- Modify: `internal/tui/model.go` (Model struct fields + new msg types near line 140)

- [ ] **Step 1: Add state fields to the structs**

In `internal/tui/pane.go`, add two fields to `PaneModel` (after `liveOutputSeen` on line 46):

```go
	working        bool                // true while a claude/opencode turn is in progress (hook-driven)
	workFrame      int                 // shared spinner frame index, mirrored here for top-border render
```

In `internal/tui/tab.go`, add one field to `TabModel` (after `focusMode` on line 12):

```go
	flashUntil time.Time // until when this tab's label flashes green (work just finished)
```

Add the `time` import to `internal/tui/tab.go` (the file currently has no imports):

```go
package tui

import "time"
```

In `internal/tui/model.go`, add two fields to the `Model` struct (place them next to other UI-animation fields; search for the struct definition of `Model`):

```go
	workSpinnerFrame int  // current frame of the shared work spinner
	workTickRunning  bool // guards against starting multiple work-spinner tick loops
```

And add two message types near the existing `spinnerTickMsg` (model.go:140):

```go
// workSpinnerTickMsg advances the shared work-in-progress spinner animation.
type workSpinnerTickMsg struct{}

// flashTickMsg fires once when a tab's green "just finished" flash expires,
// forcing a re-render so the tab returns to its normal style.
type flashTickMsg struct{}
```

- [ ] **Step 2: Write the failing test for the classifier and transitions**

Create `internal/tui/workstate_test.go`:

```go
package tui

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func TestWorkEventKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		eventType string
		want      workTransition
	}{
		{"hook.claude.UserPromptSubmit", workStart},
		{"hook.opencode.chat.message", workStart},
		{"hook.claude.Stop", workStop},
		{"hook.claude.SessionEnd", workStop},
		{"hook.opencode.session.idle", workStop},
		{"hook.opencode.session.error", workStop},
		{"process_exit", workAbort},
		{"hook.claude.Notification", workNone},
		{"output_idle", workNone},
		{"", workNone},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			t.Parallel()
			if got := workEventKind(tt.eventType); got != tt.want {
				t.Errorf("workEventKind(%q) = %v, want %v", tt.eventType, got, tt.want)
			}
		})
	}
}

// modelForWorkTest builds a Model with one tab holding one pane (id "p1").
func modelForWorkTest() Model {
	cfg := config.Default()
	tab := NewTabModel("tab-1", "test")
	pane := NewPaneModel("p1", 1024)
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "p1"
	return Model{
		client:        &fakeSender{},
		tabs:          []*TabModel{tab},
		activeTab:     0,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
	}
}

func TestApplyWorkTransition_StartSetsWorking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	if !m.tabs[0].Root.Leaves()[0].working {
		t.Fatal("expected pane.working = true after start event")
	}
	if !m.anyPaneWorking() {
		t.Error("anyPaneWorking should be true")
	}
	if !m.tabHasWorkingPane(0) {
		t.Error("tabHasWorkingPane(0) should be true")
	}
}

func TestApplyWorkTransition_StopClearsAndFlashes(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	cmd := m.applyWorkTransition("p1", "hook.claude.Stop")
	if m.tabs[0].Root.Leaves()[0].working {
		t.Error("pane.working should be false after stop")
	}
	if !m.tabFlashing(0) {
		t.Error("tab should be flashing after a genuine stop")
	}
	if cmd == nil {
		t.Error("stop transition should return a flash-expiry tick cmd")
	}
}

func TestApplyWorkTransition_AbortClearsWithoutFlash(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	cmd := m.applyWorkTransition("p1", "process_exit")
	if m.tabs[0].Root.Leaves()[0].working {
		t.Error("pane.working should be false after process_exit")
	}
	if m.tabFlashing(0) {
		t.Error("process_exit must NOT flash the tab green")
	}
	if cmd != nil {
		t.Error("abort transition should not return a flash cmd")
	}
}

func TestApplyWorkTransition_StopWithoutPriorStart_NoFlash(t *testing.T) {
	t.Parallel()
	// A Stop with no in-progress turn (pane was already idle) must not flash.
	m := modelForWorkTest()
	cmd := m.applyWorkTransition("p1", "hook.claude.Stop")
	if m.tabFlashing(0) {
		t.Error("stop on an already-idle pane must not flash")
	}
	if cmd != nil {
		t.Error("no-op stop should not return a flash cmd")
	}
}

func TestApplyWorkTransition_UnknownPane_NoPanic(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	if cmd := m.applyWorkTransition("does-not-exist", "hook.claude.Stop"); cmd != nil {
		t.Error("unknown pane should be a no-op")
	}
}

func TestTabFlashing_Expired(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.tabs[0].flashUntil = time.Now().Add(-time.Second) // already past
	if m.tabFlashing(0) {
		t.Error("expired flashUntil should report not flashing")
	}
	m.tabs[0].flashUntil = time.Time{} // zero value
	if m.tabFlashing(0) {
		t.Error("zero flashUntil should report not flashing")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run 'TestWorkEventKind|TestApplyWorkTransition|TestTabFlashing'`
Expected: FAIL — `undefined: workTransition`, `undefined: workEventKind`, etc.

- [ ] **Step 4: Implement `internal/tui/workstate.go`**

```go
package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// tabFlashDuration is how long an inactive tab label stays green after the
// pane it contains finishes a turn.
const tabFlashDuration = 5 * time.Second

// workTransition classifies a pane event's effect on a pane's working state.
type workTransition int

const (
	workNone  workTransition = iota // no effect
	workStart                       // a turn began
	workStop                        // a turn completed normally → green flash
	workAbort                       // process exited → clear working, no flash
)

// workEventKind maps a PaneEvent Type (the daemon encodes hook events as
// "hook.<src>.<event>") to a working-state transition. See the plan's
// "Event → transition mapping" table — this is the single source of truth.
func workEventKind(eventType string) workTransition {
	switch eventType {
	case "hook.claude.UserPromptSubmit", "hook.opencode.chat.message":
		return workStart
	case "hook.claude.Stop", "hook.claude.SessionEnd",
		"hook.opencode.session.idle", "hook.opencode.session.error":
		return workStop
	case "process_exit":
		return workAbort
	}
	return workNone
}

// findPaneAndTab locates a pane by ID and the index of its containing tab.
// Returns (nil, -1) if not found.
func (m *Model) findPaneAndTab(paneID string) (*PaneModel, int) {
	for i, tab := range m.tabs {
		if tab.Root == nil {
			continue
		}
		if leaf := tab.Root.FindLeaf(paneID); leaf != nil {
			return leaf.Pane, i
		}
	}
	return nil, -1
}

// applyWorkTransition updates the working state of the pane identified by
// paneID based on the event type. On a normal completion it stamps the
// containing tab's flashUntil and returns a one-shot tick cmd that re-renders
// when the flash expires. All other cases return nil.
func (m *Model) applyWorkTransition(paneID, eventType string) tea.Cmd {
	kind := workEventKind(eventType)
	if kind == workNone {
		return nil
	}
	pane, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return nil
	}
	switch kind {
	case workStart:
		pane.working = true
	case workStop:
		wasWorking := pane.working
		pane.working = false
		if wasWorking {
			m.tabs[tabIdx].flashUntil = time.Now().Add(tabFlashDuration)
			return tea.Tick(tabFlashDuration, func(time.Time) tea.Msg { return flashTickMsg{} })
		}
	case workAbort:
		pane.working = false
	}
	return nil
}

// anyPaneWorking reports whether any pane in any tab is mid-turn.
func (m Model) anyPaneWorking() bool {
	for _, tab := range m.tabs {
		if tab.Root == nil {
			continue
		}
		for _, p := range tab.Root.Leaves() {
			if p != nil && p.working {
				return true
			}
		}
	}
	return false
}

// tabHasWorkingPane reports whether the tab at idx has at least one mid-turn pane.
func (m Model) tabHasWorkingPane(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) || m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Root.Leaves() {
		if p != nil && p.working {
			return true
		}
	}
	return false
}

// tabFlashing reports whether the tab at idx is within its green flash window.
func (m Model) tabFlashing(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) {
		return false
	}
	t := m.tabs[idx].flashUntil
	return !t.IsZero() && time.Now().Before(t)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run 'TestWorkEventKind|TestApplyWorkTransition|TestTabFlashing' -v`
Expected: PASS (all subtests).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/workstate.go internal/tui/workstate_test.go internal/tui/pane.go internal/tui/tab.go internal/tui/model.go
git commit -m "feat(tui): add hook-driven pane working-state model"
```

---

## Task 2: Wire the event stream + spinner animation tick

**Files:**
- Modify: `internal/tui/model.go` (`paneEventMsg` case ~line 751; add `workSpinnerTickMsg`/`flashTickMsg` cases near `spinnerTickMsg` ~line 655)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/workstate_test.go`:

```go
func TestUpdate_PaneEvent_StartBeginsTicking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	start := paneEventMsg(ipc.PaneEventPayload{
		ID:     "e1",
		PaneID: "p1",
		Type:   "hook.claude.UserPromptSubmit",
		Title:  "Working on: x",
	})
	next, _ := m.Update(start)
	nm := next.(Model)
	if !nm.anyPaneWorking() {
		t.Fatal("pane should be working after UserPromptSubmit")
	}
	if !nm.workTickRunning {
		t.Error("work spinner tick loop should have started")
	}
}

func TestUpdate_WorkSpinnerTick_AdvancesAndStops(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	// Pane working → tick should advance the frame and keep running.
	m.tabs[0].Root.Leaves()[0].working = true
	m.workTickRunning = true
	next, cmd := m.Update(workSpinnerTickMsg{})
	nm := next.(Model)
	if nm.workSpinnerFrame != 1 {
		t.Errorf("frame = %d, want 1", nm.workSpinnerFrame)
	}
	if nm.tabs[0].Root.Leaves()[0].workFrame != 1 {
		t.Errorf("pane.workFrame = %d, want 1 (mirrored)", nm.tabs[0].Root.Leaves()[0].workFrame)
	}
	if cmd == nil {
		t.Error("tick should reschedule while a pane is working")
	}

	// No pane working → tick stops.
	m2 := modelForWorkTest()
	m2.workTickRunning = true
	next2, cmd2 := m2.Update(workSpinnerTickMsg{})
	if next2.(Model).workTickRunning {
		t.Error("tick loop should stop when no pane is working")
	}
	if cmd2 != nil {
		t.Error("stopped tick must not reschedule")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run 'TestUpdate_PaneEvent_StartBeginsTicking|TestUpdate_WorkSpinnerTick'`
Expected: FAIL — `workSpinnerTickMsg` not handled (frame stays 0; `workTickRunning` never set).

- [ ] **Step 3: Add a `workSpinnerTick` helper + the two message handlers**

In `internal/tui/workstate.go`, add the tick interval constant and helper:

```go
// workSpinnerInterval is the animation cadence for the work-in-progress
// spinner (shared by tab and pane indicators).
const workSpinnerInterval = 100 * time.Millisecond

// workSpinnerTick schedules the next shared work-spinner frame.
func (m Model) workSpinnerTick() tea.Cmd {
	return tea.Tick(workSpinnerInterval, func(time.Time) tea.Msg { return workSpinnerTickMsg{} })
}
```

In `internal/tui/model.go`, add two cases right after the existing `spinnerTickMsg` case (after line 676):

```go
	case workSpinnerTickMsg:
		// Self-stopping loop: only keep ticking while a pane is mid-turn.
		if !m.anyPaneWorking() {
			m.workTickRunning = false
			return m, nil
		}
		m.workSpinnerFrame++
		// Mirror the shared frame onto every working pane so the top-border
		// spinner (rendered inside PaneModel.View) stays in sync with the tab.
		for _, tab := range m.tabs {
			if tab.Root == nil {
				continue
			}
			for _, p := range tab.Root.Leaves() {
				if p != nil && p.working {
					p.workFrame = m.workSpinnerFrame
				}
			}
		}
		return m, m.workSpinnerTick()

	case flashTickMsg:
		// A tab's green flash expired — returning triggers a re-render and
		// tabFlashing() recomputes from flashUntil (now in the past).
		return m, nil
```

- [ ] **Step 4: Wire `paneEventMsg` to drive transitions and start the tick**

In `internal/tui/model.go`, replace the `paneEventMsg` case body (lines 751-764) with:

```go
	case paneEventMsg:
		// Skip output_idle events for the pane the user is currently looking
		// at — it's redundant noise. Other event types (process_exit, bell,
		// command_complete) stay even on the active pane: they're transient
		// state changes that benefit from a sidebar audit trail.
		if !(msg.Type == "output_idle" && m.isActivePane(msg.PaneID)) {
			m.notifications.AddEvent(ipc.PaneEventPayload(msg))
		}
		cmds := []tea.Cmd{m.listenForMessages()}
		// Update working state + green-flash from the same hook stream.
		if flashCmd := m.applyWorkTransition(msg.PaneID, msg.Type); flashCmd != nil {
			cmds = append(cmds, flashCmd)
		}
		if m.anyPaneWorking() && !m.workTickRunning {
			m.workTickRunning = true
			cmds = append(cmds, m.workSpinnerTick())
		}
		// Refresh sidebar tick if visible (no auto-show — user controls with Alt+N)
		if m.notifications.visible {
			cmds = append(cmds, m.sidebarTick())
		}
		return m, tea.Batch(cmds...)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run 'TestUpdate_PaneEvent_StartBeginsTicking|TestUpdate_WorkSpinnerTick|TestApplyWorkTransition|TestWorkEventKind|TestTabFlashing' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/workstate.go internal/tui/workstate_test.go
git commit -m "feat(tui): drive work-spinner animation from hook event stream"
```

---

## Task 3: Spinner glyph on the tab label

**Files:**
- Modify: `internal/tui/model.go` (`tabLabel`, lines 2185-2197)

- [ ] **Step 1: Write the failing test**

First add `"strings"` to the import block of `internal/tui/workstate_test.go` (it currently imports `testing`, `time`, `config`, `ipc`). Then append this test:

```go
func TestTabLabel_ShowsSpinnerWhenWorking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.tabs[0].Name = "Build"
	m.workSpinnerFrame = 0 // spinnerFrames[0] == "⠋"

	// Not working: no spinner glyph.
	if got := m.tabLabel(0); strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("idle tab label %q should not contain a spinner", got)
	}

	// Working: leading spinner frame present.
	m.tabs[0].Root.Leaves()[0].working = true
	got := m.tabLabel(0)
	if !strings.Contains(got, "⠋") {
		t.Errorf("working tab label %q should contain frame ⠋", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestTabLabel_ShowsSpinnerWhenWorking`
Expected: FAIL — working label has no spinner.

- [ ] **Step 3: Add the spinner to `tabLabel`**

In `internal/tui/model.go`, modify `tabLabel` (lines 2185-2197) so it prepends the shared spinner frame when the tab has a working pane:

```go
func (m Model) tabLabel(idx int) string {
	if m.renaming && idx == m.activeTab {
		return "* " + m.renameInput + "▎"
	}
	name := fmt.Sprintf("%d:%s", idx+1, m.tabs[idx].Name)
	if m.tabHasEagerPane(idx) {
		name = eagerTabMarker + name
	}
	if m.tabHasWorkingPane(idx) {
		name = spinnerFrames[m.workSpinnerFrame%len(spinnerFrames)] + name
	}
	if idx == m.activeTab {
		return "* " + name
	}
	return name
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestTabLabel_ShowsSpinnerWhenWorking -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/workstate_test.go
git commit -m "feat(tui): show work spinner on tab labels"
```

---

## Task 4: Green flash on inactive tabs (+ DRY the tab style)

**Files:**
- Modify: `internal/tui/styles.go` (add `flashTabStyle`)
- Modify: `internal/tui/model.go` (`renderTabBar` ~2214-2225, `hitTestTab` ~2324-2335 → both call a new `tabStyle` helper)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/workstate_test.go`:

```go
func TestTabStyle_FlashOverridesInactive(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	// Add a second tab so we can flash a non-active one.
	tab2 := NewTabModel("tab-2", "second")
	tab2.Root = NewLeaf(NewPaneModel("p2", 1024))
	m.tabs = append(m.tabs, tab2)
	m.activeTab = 0

	// Inactive tab flashing → green flash style.
	m.tabs[1].flashUntil = time.Now().Add(time.Hour)
	if m.tabStyle(1) != flashTabStyle {
		t.Error("flashing inactive tab should use flashTabStyle")
	}

	// Active tab never flashes, even if flashUntil is set.
	m.tabs[0].flashUntil = time.Now().Add(time.Hour)
	if m.tabStyle(0) == flashTabStyle {
		t.Error("active tab must never use flashTabStyle")
	}
	if m.tabStyle(0) != activeTabStyle {
		t.Error("active tab without custom color should use activeTabStyle")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestTabStyle_FlashOverridesInactive`
Expected: FAIL — `undefined: m.tabStyle`, `undefined: flashTabStyle`.

- [ ] **Step 3: Add `flashTabStyle`**

In `internal/tui/styles.go`, add inside the `var (...)` block (after `inactiveTabStyle`):

```go
	// flashTabStyle highlights an inactive tab for a few seconds after a
	// pane in it finishes a turn. Green background, bright text.
	flashTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("28")).
		Padding(0, 1)
```

- [ ] **Step 4: Add the `tabStyle` helper and route both renderers through it**

In `internal/tui/model.go`, add this method just above `renderTabBar` (before line 2199):

```go
// tabStyle returns the lipgloss style for the tab at idx. Precedence:
// green flash (inactive + within flash window) > custom tab color > active/
// inactive default. Shared by renderTabBar and hitTestTab so rendered widths
// and click hit-testing never diverge.
func (m Model) tabStyle(idx int) lipgloss.Style {
	tab := m.tabs[idx]
	active := idx == m.activeTab
	if !active && m.tabFlashing(idx) {
		return flashTabStyle
	}
	if tab.Color != "" {
		c := lipgloss.Color(tab.Color)
		if active {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(c).Padding(0, 1)
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(c).Padding(0, 1)
	}
	if active {
		return activeTabStyle
	}
	return inactiveTabStyle
}
```

In `renderTabBar`, replace the style-selection block (lines 2214-2225) with:

```go
		style := m.tabStyle(i)
```

In `hitTestTab`, replace the style-selection block (lines 2324-2335) with:

```go
		style := m.tabStyle(i)
```

- [ ] **Step 5: Run the test + the tab hit-test regression suite**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run 'TestTabStyle_FlashOverridesInactive|TabBar|HitTest|Tab' -v`
Expected: PASS (the new test plus any existing tab/hit-test tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/styles.go internal/tui/model.go internal/tui/workstate_test.go
git commit -m "feat(tui): flash inactive tab green for 5s when work finishes"
```

---

## Task 5: Pane top-border left spinner

**Files:**
- Modify: `internal/tui/pane.go` (`buildTopBorder` signature + left-label section, lines 248-305; caller at line 248)
- Create: `internal/tui/border_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/border_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBuildTopBorder_WorkingShowsLeftSpinner(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")

	// working=false → no spinner glyph anywhere.
	idle := buildTopBorder(40, "/home/user/proj", "claude", c,
		false, false, false, false, 0, /*working*/ false, /*workFrame*/ 0)
	if strings.ContainsAny(idle, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("idle border %q should not contain a spinner", idle)
	}

	// working=true, frame 0 → leading spinner ⠋ present, CWD still shown.
	busy := buildTopBorder(40, "/home/user/proj", "claude", c,
		false, false, false, false, 0, /*working*/ true, /*workFrame*/ 0)
	if !strings.Contains(busy, "⠋") {
		t.Errorf("working border %q should contain frame ⠋", busy)
	}
	if !strings.Contains(busy, "proj") {
		t.Errorf("working border %q should still show the CWD", busy)
	}
}

func TestBuildTopBorder_WorkingNoCWD(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")
	busy := buildTopBorder(40, "", "claude", c,
		false, false, false, false, 0, true, 0)
	if !strings.Contains(busy, "⠋") {
		t.Errorf("working border with empty CWD %q should still show the spinner", busy)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestBuildTopBorder`
Expected: FAIL — `too many arguments in call to buildTopBorder`.

- [ ] **Step 3: Extend `buildTopBorder` with a left-side working spinner**

In `internal/tui/pane.go`, change the signature (line 253) to add two trailing params:

```go
func buildTopBorder(width int, cwd, name string, color color.Color, ghost, resuming, preparing, focus bool, spinnerFrame int, working bool, workFrame int) string {
```

Replace the "Left label" section (lines 287-305) with the spinner-aware version. The spinner is a fixed leading segment that is **never** subject to the CWD left-truncation:

```go
	// Optional working spinner — a fixed leading segment drawn before the CWD.
	// Reserved width is excluded from the CWD truncation budget so the spinner
	// itself is never cut off (the CWD truncates from its left with "…tail").
	spin := ""
	spinLen := 0
	if working {
		spin = " " + spinnerFrames[workFrame%len(spinnerFrames)]
		spinLen = 2 // leading space + single-width braille glyph
	}

	// Left label: CWD, truncated with ellipsis if needed.
	leftLabel := ""
	leftLen := 0
	if cwd != "" {
		available := innerW - rightLen - 1 - spinLen // reserve 1 dash + spinner
		cwdLabel := " " + cwd + " "
		cwdLabelLen := len([]rune(cwdLabel))

		if available < 0 {
			available = 0
		}
		if cwdLabelLen <= available {
			leftLabel = cwdLabel
			leftLen = cwdLabelLen
		} else if available >= 6 {
			// Truncate CWD from the left: " …tail "
			maxCwd := available - 4 // 4 = len(" …") + len(" ")
			cwdRunes := []rune(cwd)
			leftLabel = " …" + string(cwdRunes[len(cwdRunes)-maxCwd:]) + " "
			leftLen = len([]rune(leftLabel))
		}
	} else if working {
		// No CWD but working: still show the spinner with a trailing space.
		leftLabel = " "
		leftLen = 1
	}

	// Prepend the spinner segment (never truncated).
	leftLabel = spin + leftLabel
	leftLen += spinLen
```

Update the single caller (line 248) to pass the new pane fields:

```go
	topLine := buildTopBorder(p.Width, p.CWD, rightLabel, borderColor, p.ghost, p.resuming, p.preparing, p.focusMode, p.spinnerFrame, p.working, p.workFrame)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/ -run TestBuildTopBorder -v`
Expected: PASS.

- [ ] **Step 5: Run the full TUI package to catch any wide-char/border regressions**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./internal/tui/`
Expected: ok (whole package green).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/pane.go internal/tui/border_test.go
git commit -m "feat(tui): show work spinner on pane top-left border"
```

---

## Task 6: OpenCode "work started" producer hook (`chat.message`)

Claude already emits its start edge (`UserPromptSubmit`); OpenCode only emits the stop edge (`session.idle`). Without this task an opencode pane never lights up. The daemon needs **no** change — `emitHookEvent` maps any hook event to `hook.opencode.<event>` automatically, and the Task 1 classifier already treats `hook.opencode.chat.message` as a start.

**Files:**
- Modify: `internal/opencodehook/scripts/quil-session-tracker.js` (add a `chat.message` typed hook to the returned object, ~line 336 alongside `permission.ask`)

- [ ] **Step 1: Add the `chat.message` handler**

In `internal/opencodehook/scripts/quil-session-tracker.js`, inside the returned object (after the `event:` handler and before or beside `"permission.ask"`, around line 333-336), add:

```js
    // chat.message — fires when a user message is submitted to the model.
    // This is opencode's analog of Claude's UserPromptSubmit and marks the
    // START of a turn. The TUI flips the pane to "working" on this event and
    // back to idle on session.idle/session.error. Emitting it also produces a
    // "Working…" notification card, symmetric with Claude's behaviour.
    "chat.message": async (_input, _output) => {
      try {
        await spool("chat.message", "Working…", "info", {});
      } catch (e) {
        await logLine("chat.message handler error: " + (e && e.message ? e.message : String(e)));
      }
    },
```

- [ ] **Step 2: Verify the JS is syntactically valid**

Run (Node is available in the golang alpine image? No — use a node image):
`PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -w //src node:22-alpine node --check internal/opencodehook/scripts/quil-session-tracker.js`
Expected: no output, exit 0 (syntax OK).

- [ ] **Step 3: Confirm the embed still builds**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go build ./internal/opencodehook/`
Expected: builds clean (the `//go:embed scripts/*` picks up the edited file).

- [ ] **Step 4: Commit**

```bash
git add internal/opencodehook/scripts/quil-session-tracker.js
git commit -m "feat(opencode): emit chat.message start edge for work indicator"
```

- [ ] **Step 5: RUNTIME VERIFICATION (dev mode — manual, required)**

> ⚠️ The exact opencode hook name `chat.message` is the documented "user message submitted" hook but could not be confirmed against your installed opencode version from offline docs. This step confirms it fires. **Dev mode only — never touch `~/.quil`.**

1. Build dev binaries: `./scripts/dev.sh build`
2. Launch dev TUI: `./scripts/quil-dev.ps1` (Windows) — confirm `[dev]` shows in the status bar.
3. Create an opencode pane (Ctrl+N → opencode) and submit any prompt.
4. While it's generating: the tab label and the pane top-left should show the rotating braille spinner; when it finishes, the spinner clears and the (inactive) tab flashes green for 5 s.
5. Cross-check the spool: the dev events file `./.quil/events/<paneID>.jsonl` should contain a line with `"hook_event":"chat.message"` shortly after you submit.
   - **If no `chat.message` line appears**, the hook name differs in your opencode version. Fallback: subscribe to `tool.execute.before` instead (confirmed to exist) by adding a `"tool.execute.before": async (input, output) => { await spool("chat.message", "Working…", "info", {}); }` handler — keep the spool `hookEvent` string as `"chat.message"` so the daemon/TUI mapping is unchanged. Re-run this verification.

---

## Task 7: Documentation + full verification

**Files:**
- Modify: `.claude/CLAUDE.md` (Notification center / M5 area)

- [ ] **Step 1: Document the feature in CLAUDE.md**

In `.claude/CLAUDE.md`, append to the Notification-center bullet (the one starting "Notification center: `internal/daemon/event.go`") a sentence describing the indicator, and add a new bullet under the architecture list:

```markdown
- Work-in-progress indicators: `internal/tui/workstate.go` derives a per-pane `working` bool entirely TUI-side from the existing `paneEventMsg` stream (`Type == "hook.<src>.<event>"`). Start edges: `hook.claude.UserPromptSubmit`, `hook.opencode.chat.message`. Stop edges (→ 5 s green tab flash): `hook.claude.Stop`/`SessionEnd`, `hook.opencode.session.idle`/`session.error`. `process_exit` clears working without a flash. A single shared 100 ms `workSpinnerTickMsg` animates the braille `spinnerFrames` on both the tab label (`tabLabel` prefix when `tabHasWorkingPane`) and each working pane's top-left border (`buildTopBorder` left segment); the loop self-stops via `workTickRunning` when no pane is working. Inactive tabs flash via `flashTabStyle` (`tabStyle` precedence: flash > custom color > active/inactive; shared by `renderTabBar` + `hitTestTab`). OpenCode's start edge is produced by the `chat.message` handler in `internal/opencodehook/scripts/quil-session-tracker.js`; Claude needs no producer change. State is not persisted — panes start idle on restart and the next hook event corrects them.
```

- [ ] **Step 2: Run the full test suite**

Run: `PROJECT_DIR="$(pwd -W 2>/dev/null || pwd)"; docker run --rm -v "${PROJECT_DIR}:/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine go test ./...`
Expected: ok across all packages.

- [ ] **Step 3: Run vet**

Run: `./scripts/dev.sh vet`
Expected: no findings.

- [ ] **Step 4: Build all variants**

Run: `./scripts/dev.sh build`
Expected: 6 binaries built clean.

- [ ] **Step 5: Commit**

```bash
git add .claude/CLAUDE.md
git commit -m "docs: document work-in-progress indicators"
```

---

## Self-review notes

- **Spec coverage:** Tab spinner (Task 3) ✓; multi-pane tab → indicator if ≥1 pane working (`tabHasWorkingPane`, Task 1/3) ✓; per-pane top-left spinner (Task 5) ✓; rotation glyph (braille `spinnerFrames`) ✓; green flash on inactive tab for 5 s, not a border (Task 4) ✓; start/stop detection for Claude (Task 1, edges already arrive) and OpenCode (Task 6 adds start edge) ✓.
- **Out of scope (stated):** "blocked/needs-you" third state; persisting working state; pane-border green flash; a config toggle to disable the indicator. Each is an easy follow-up.
- **Type consistency:** `workTransition`/`workEventKind`/`applyWorkTransition`/`anyPaneWorking`/`tabHasWorkingPane`/`tabFlashing`/`findPaneAndTab`/`workSpinnerTick`/`tabStyle` are defined once (Tasks 1, 2, 4) and reused with identical signatures. `buildTopBorder`'s new `(working bool, workFrame int)` trailing params match the caller and both tests. `PaneModel.working`/`workFrame`, `TabModel.flashUntil`, `Model.workSpinnerFrame`/`workTickRunning`, and msgs `workSpinnerTickMsg`/`flashTickMsg` are declared in Task 1 and referenced consistently thereafter.
- **Risk:** the only unverified external fact is opencode's `chat.message` hook name — Task 6 Step 5 verifies it at runtime with a concrete confirmed fallback (`tool.execute.before`).
