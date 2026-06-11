# Tab + Pane "Unseen" Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 5-second green tab flash on agent work completion with a persistent per-pane "unseen" mark: green pane border + derived green tab label, cleared when the user focuses the marked pane.

**Architecture:** Pure TUI-side change in `internal/tui`. `PaneModel` gains `unseen bool`, set by `applyWorkTransition` on `workStop` for any pane that is not the focused pane of the active tab, and cleared by `ackFocusedPane()` — a single call at the top of `Model.Update` (by the time any message arrives, the previous frame with that pane focused has been rendered, so the user has seen it). `TabModel.flashUntil` and the flash-expiry tick are deleted; the tab label green becomes a pure derivation (`tabUnseen`: background tab with ≥1 unseen pane). The pane border picks green when unseen and unfocused. No daemon, IPC, or persistence changes.

**Tech Stack:** Go 1.25, Bubble Tea v2, Lipgloss v2. Build/test via Docker: `./scripts/dev.sh test` (no local Go).

**Spec:** `docs/superpowers/specs/2026-06-11-tab-unseen-indicator-design.md` (rev 2)

---

## Compile-coupling note

All files are in the single `tui` package, so each task's test rewrite and production edits cannot compile independently — the "failing test" step fails as a compile error (e.g. `unseen` undefined), which is the TDD red state for an in-package field swap. Each task still ends green and committed.

---

### Task 1: Per-pane unseen state, transitions, derived tab label

**Files:**
- Modify: `internal/tui/pane.go` (~line 60, PaneModel struct — `unseen` field)
- Modify: `internal/tui/tab.go:3,15` (drop `flashUntil` + `time` import)
- Modify: `internal/tui/workstate.go` (drop `tabFlashDuration`, rewrite `applyWorkTransition`, `tabFlashing`→derived `tabUnseen`)
- Modify: `internal/tui/styles.go:17-23` (`flashTabStyle`→`unseenTabStyle`)
- Modify: `internal/tui/model.go:149-151,402-403,774-777,864-868,2367-2376` (delete `flashTickMsg` plumbing, simplify caller, render via `tabUnseen`)
- Test: `internal/tui/workstate_test.go`

- [ ] **Step 1: Rewrite the flash tests as unseen tests**

In `internal/tui/workstate_test.go`:

1a. Remove `"time"` from the import block (nothing uses it after this rewrite):

```go
import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)
```

1b. Add two fixture helpers right after `modelForWorkTest` (the existing helper puts pane `p1` as the focused pane of the active tab):

```go
// modelWithBackgroundTab extends modelForWorkTest with a second, background
// tab (index 1) holding pane "p2". activeTab stays 0, so transitions on "p2"
// exercise the background-tab marking rules.
func modelWithBackgroundTab() Model {
	m := modelForWorkTest()
	tab2 := NewTabModel("tab-2", "background")
	tab2.Root = NewLeaf(NewPaneModel("p2", 1024))
	tab2.ActivePane = "p2"
	m.tabs = append(m.tabs, tab2)
	m.activeTab = 0
	return m
}

// modelWithSplitActiveTab extends modelForWorkTest with a second pane "p1b"
// split into the active tab. "p1" stays the focused pane (tab.ActivePane), so
// transitions on "p1b" exercise the unfocused-sibling marking rules.
func modelWithSplitActiveTab() Model {
	m := modelForWorkTest()
	m.tabs[0].Root = &LayoutNode{
		Split: SplitHorizontal,
		Ratio: 0.5,
		Left:  m.tabs[0].Root,
		Right: NewLeaf(NewPaneModel("p1b", 1024)),
	}
	m.tabs[0].invalidateLeaves()
	return m
}
```

(`invalidateLeaves` is `tab.go:42`; the cache is nil on a fresh model so the call is belt-and-suspenders, but the `Leaves()` contract requires it after any `Root` mutation.)

1c. Replace `TestApplyWorkTransition_StopClearsAndFlashes` with:

```go
func TestApplyWorkTransition_StopOnBackgroundTab_SetsUnseen(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit")
	m.applyWorkTransition("p2", "hook.claude.Stop")
	if m.tabs[1].Root.Leaves()[0].working {
		t.Error("pane.working should be false after stop")
	}
	if !m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("background-tab pane should be marked unseen after a genuine stop")
	}
	if !m.tabUnseen(1) {
		t.Error("tab label derivation should report the background tab unseen")
	}
}

func TestApplyWorkTransition_StopOnFocusedPane_NoMark(t *testing.T) {
	t.Parallel()
	// Completion in the pane being looked at is seen by definition — no mark.
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit")
	m.applyWorkTransition("p1", "hook.claude.Stop")
	if m.tabs[0].Root.Leaves()[0].working {
		t.Error("pane.working should be false after stop")
	}
	if m.tabs[0].Root.Leaves()[0].unseen {
		t.Error("the focused pane of the active tab must never be marked unseen")
	}
}

func TestApplyWorkTransition_StopOnUnfocusedSibling_MarksPaneOnly(t *testing.T) {
	t.Parallel()
	// An unfocused split sibling on the ACTIVE tab gets the border cue (the
	// user may be typing in the focused pane), but the active tab's label
	// never goes green — you're already on the tab.
	m := modelWithSplitActiveTab()
	m.applyWorkTransition("p1b", "hook.claude.UserPromptSubmit")
	m.applyWorkTransition("p1b", "hook.claude.Stop")
	if !m.tabs[0].Root.Right.Pane.unseen {
		t.Error("unfocused sibling pane should be marked unseen")
	}
	if m.tabUnseen(0) {
		t.Error("the active tab's label must not report unseen")
	}
}
```

1d. Replace `TestApplyWorkTransition_ParkForInputClearsAndFlashes` with:

```go
func TestApplyWorkTransition_ParkForInput_MarksBackgroundPane(t *testing.T) {
	t.Parallel()
	// When the agent parks for user input (permission prompt / option select)
	// the spinner must stop and the pane must be marked unseen — the mark
	// persists until the user focuses the pane.
	for _, evt := range []string{
		"hook.claude.Notification",
		"hook.claude.PermissionRequest",
		"hook.opencode.permission.ask",
	} {
		t.Run(evt, func(t *testing.T) {
			t.Parallel()
			m := modelWithBackgroundTab()
			m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit")
			m.applyWorkTransition("p2", evt)
			if m.tabs[1].Root.Leaves()[0].working {
				t.Errorf("%s: pane.working should be false after a park-for-input edge", evt)
			}
			if !m.tabs[1].Root.Leaves()[0].unseen {
				t.Errorf("%s: pane should be marked unseen when the agent parks", evt)
			}
		})
	}
}
```

1e. Replace `TestApplyWorkTransition_ResumeAfterParkClearsFlashAndReArms` with:

```go
func TestApplyWorkTransition_ResumeAfterParkClearsUnseenAndReArms(t *testing.T) {
	t.Parallel()
	// Full prompt cycle on a background pane: start → park (spinner off +
	// unseen) → user answers (PostToolUse) → spinner back on, mark cleared.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit")
	m.applyWorkTransition("p2", "hook.claude.PermissionRequest") // park
	pane := m.tabs[1].Root.Leaves()[0]
	if pane.working {
		t.Fatal("precondition: pane should be parked (not working) before resume")
	}
	if !pane.unseen {
		t.Fatal("precondition: pane should be unseen after the park")
	}

	m.applyWorkTransition("p2", "hook.claude.PostToolUse") // resume
	if !pane.working {
		t.Error("pane.working should be true again after the answer (PostToolUse)")
	}
	if pane.unseen {
		t.Error("resume must clear the unseen mark — work is no longer parked")
	}
}
```

1f. Replace `TestApplyWorkTransition_StartClearsStaleFlash` with:

```go
func TestApplyWorkTransition_StartClearsStaleUnseen(t *testing.T) {
	t.Parallel()
	// A fresh turn must clear a lingering mark from the previous turn — the
	// spinner supersedes the green "finished" cue.
	m := modelWithBackgroundTab()
	m.tabs[1].Root.Leaves()[0].unseen = true
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit")
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("a new turn (UserPromptSubmit) should clear a stale unseen mark")
	}
}
```

1g. Replace `TestApplyWorkTransition_AbortClearsWithoutFlash` with:

```go
func TestApplyWorkTransition_AbortClearsWorkingWithoutMarking(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit")
	m.applyWorkTransition("p2", "process_exit")
	if m.tabs[1].Root.Leaves()[0].working {
		t.Error("pane.working should be false after process_exit")
	}
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("process_exit must NOT mark the pane unseen (a crash is not a completed turn)")
	}

	// An existing mark from an earlier completion survives an abort.
	m2 := modelWithBackgroundTab()
	m2.tabs[1].Root.Leaves()[0].unseen = true
	m2.applyWorkTransition("p2", "process_exit")
	if !m2.tabs[1].Root.Leaves()[0].unseen {
		t.Error("abort must not clear an existing unseen mark")
	}
}
```

1h. Replace `TestApplyWorkTransition_StopWithoutPriorStart_NoFlash` with:

```go
func TestApplyWorkTransition_StopWithoutPriorStart_NoMark(t *testing.T) {
	t.Parallel()
	// A Stop with no in-progress turn (pane was already idle) must not mark.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.Stop")
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("stop on an already-idle pane must not mark the pane unseen")
	}
}
```

1i. Replace `TestApplyWorkTransition_UnknownPane_NoPanic` body (no return value to check anymore):

```go
func TestApplyWorkTransition_UnknownPane_NoPanic(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("does-not-exist", "hook.claude.Stop") // must not panic
}
```

1j. Replace `TestTabFlashing_Expired` with:

```go
func TestTabUnseen_DerivedAndBounds(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	if m.tabUnseen(-1) || m.tabUnseen(99) {
		t.Error("out-of-range tab index must report not unseen")
	}
	if m.tabUnseen(1) {
		t.Error("background tab with no unseen pane must report false")
	}
	m.tabs[1].Root.Leaves()[0].unseen = true
	if !m.tabUnseen(1) {
		t.Error("background tab with an unseen pane must report true")
	}
	// The same tab reports false the moment it is active — the label cue is
	// suppressed while the user is on the tab (the pane border takes over).
	m.activeTab = 1
	if m.tabUnseen(1) {
		t.Error("the active tab must never report unseen")
	}
}
```

1k. Replace `TestTabStyle_FlashOverridesInactive` with:

```go
func TestTabStyle_UnseenOverridesInactive(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()

	// lipgloss.Style is uncomparable (contains a slice), so assert on the
	// rendered 256-color background SGR: unseen=48;5;28, active=48;5;57.

	// Background tab with an unseen pane → green label.
	m.tabs[1].Root.Leaves()[0].unseen = true
	if !strings.Contains(m.tabStyle(1).Render("x"), "48;5;28") {
		t.Error("unseen background tab should render with green background (48;5;28)")
	}

	// Active tab never renders the green label, even with an unseen pane.
	m.tabs[0].Root.Leaves()[0].unseen = true
	if strings.Contains(m.tabStyle(0).Render("x"), "48;5;28") {
		t.Error("active tab must never use the green unseen background")
	}
	if !strings.Contains(m.tabStyle(0).Render("x"), "48;5;57") {
		t.Error("active tab without custom color should use activeTabStyle (48;5;57)")
	}
}
```

All other tests in the file (`TestWorkEventKind`, `TestApplyWorkTransition_StartSetsWorking`, spinner/mute/label tests) stay untouched.

- [ ] **Step 2: Run tests to verify they fail (compile error)**

Run: `./scripts/dev.sh test`
Expected: FAIL — `internal/tui` does not compile: `.unseen undefined`, `m.tabUnseen undefined`.

- [ ] **Step 3: Add the PaneModel field**

`internal/tui/pane.go` — in the `PaneModel` struct, next to the existing `working` field, add:

```go
	unseen bool // work finished/parked while this pane was not focused; cleared on focus
```

- [ ] **Step 4: Drop the TabModel flash field**

`internal/tui/tab.go` — delete line 15 (`flashUntil time.Time …`) and the now-unused `import "time"` on line 3 (`flashUntil` was its only use). The struct keeps everything else, including the `leavesCache` block.

- [ ] **Step 5: Rewrite the transition + query in workstate.go**

`internal/tui/workstate.go`:

5a. Delete the `tabFlashDuration` const (lines 9-11). The `time` import stays — `workSpinnerInterval` uses it.

5b. Update the `workStop`/`workAbort` doc comments on the consts:

```go
const (
	workNone  workTransition = iota // no effect
	workStart                       // a turn began
	workStop                        // turn completed OR parked for user input → mark pane unseen
	workAbort                       // process exited → clear working, no mark
)
```

5c. Replace `applyWorkTransition` (no more `tea.Cmd` return):

```go
// applyWorkTransition updates the working state of the pane identified by
// paneID based on the event type. On a normal completion or park, any pane
// that is not the focused pane of the active tab gets a persistent unseen
// mark — green border + derived green tab label — cleared when the user
// focuses the pane (ackFocusedPane at Update entry). There is no timer.
func (m *Model) applyWorkTransition(paneID, eventType string) {
	kind := workEventKind(eventType)
	if kind == workNone {
		return
	}
	pane, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return
	}
	switch kind {
	case workStart:
		pane.working = true
		// Seed the pane spinner with the shared frame so the tab and pane
		// glyphs are in sync from the first render (before the next tick).
		pane.workFrame = m.workSpinnerFrame
		// A (re)start means the work is no longer "finished/parked" — the
		// spinner supersedes the green unseen mark. Covers both a fresh turn
		// after a previous completion and a resume after the user answers a
		// prompt (PostToolUse arrives while the mark is set).
		pane.unseen = false
	case workStop:
		wasWorking := pane.working
		pane.working = false
		// Mark unless the user is looking straight at the pane: completion
		// in the focused pane of the active tab is seen by definition. An
		// unfocused split sibling IS marked — its green border is the cue.
		focused := tabIdx == m.activeTab && m.tabs[tabIdx].ActivePane == paneID
		if wasWorking && !focused {
			pane.unseen = true
		}
	case workAbort:
		pane.working = false
	}
}
```

5d. Replace `tabFlashing` with the derived `tabUnseen`:

```go
// tabUnseen reports whether the background tab at idx contains at least one
// pane with an unacknowledged work-finished mark. Purely derived from pane
// state — the active tab always reports false (the user is on it; the pane
// border carries the cue there).
func (m Model) tabUnseen(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) || idx == m.activeTab || m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Leaves() {
		if p != nil && p.unseen {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Rename the style**

`internal/tui/styles.go` lines 17-23:

```go
	// unseenTabStyle highlights a background tab containing a pane that
	// finished a turn (or parked for user input) and hasn't been focused
	// since. Green background, bright text; clears when the pane is focused.
	unseenTabStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("231")).
				Background(lipgloss.Color("28")).
				Padding(0, 1)
```

(Only the name and comment change — colors stay. Let gofmt settle the alignment.)

- [ ] **Step 7: Update model.go (delete tick plumbing, simplify caller, render)**

Five edits in `internal/tui/model.go`:

7a. Delete the `flashTickMsg` type (lines 149-151):

```go
// flashTickMsg fires once when a tab's green "just finished" flash expires,
// forcing a re-render so the tab returns to its normal style.
type flashTickMsg struct{}
```

7b. Delete its `msgName` case (lines 402-403):

```go
	case flashTickMsg:
		return "tui.flashTickMsg"
```

7c. Delete its `Update` handler (lines 774-777):

```go
	case flashTickMsg:
		// A tab's green flash expired — returning triggers a re-render and
		// tabFlashing() recomputes from flashUntil (now in the past).
		return m, nil
```

7d. Simplify the `paneEventMsg` caller (lines 864-868). Replace:

```go
		cmds := []tea.Cmd{m.listenForMessages()}
		// Update working state + green-flash from the same hook stream.
		if flashCmd := m.applyWorkTransition(msg.PaneID, msg.Type); flashCmd != nil {
			cmds = append(cmds, flashCmd)
		}
```

with:

```go
		cmds := []tea.Cmd{m.listenForMessages()}
		// Update working state + unseen marks from the same hook stream.
		m.applyWorkTransition(msg.PaneID, msg.Type)
```

7e. Update `tabStyle` (lines 2367-2376). Replace the doc comment and first guard:

```go
// tabStyle returns the lipgloss style for the tab at idx. Precedence: green
// unseen mark (background tab with an unfocused finished pane) > custom tab
// color > active/inactive default. Shared by renderTabBar and hitTestTab so
// rendered widths and click hit-testing never diverge.
func (m Model) tabStyle(idx int) lipgloss.Style {
	tab := m.tabs[idx]
	active := idx == m.activeTab
	if !active && m.tabUnseen(idx) {
		return unseenTabStyle
	}
```

(`tabUnseen` already excludes the active tab; the `!active` guard stays as belt-and-suspenders. The rest of the function — custom color, active/inactive fallback — is unchanged.)

- [ ] **Step 8: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS everywhere. If anything still references `flashUntil`, `tabFlashing`, `flashTabStyle`, `flashTickMsg`, or `tabFlashDuration`, the compiler will name it — those five identifiers must have zero remaining references.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/pane.go internal/tui/tab.go internal/tui/workstate.go internal/tui/styles.go internal/tui/model.go internal/tui/workstate_test.go
git commit -m "feat(tui): persistent per-pane unseen mark replaces 5s tab flash

A pane whose agent finishes a turn or parks for input while unfocused
is now marked unseen; the background tab's label derives green from
its panes and stays green until acknowledged, instead of flashing for
5 seconds. The focused pane of the active tab is never marked.
Removes flashTickMsg and the flash-expiry timer."
```

---

### Task 2: ackFocusedPane — clear the mark on focus

**Files:**
- Modify: `internal/tui/workstate.go` (new helper)
- Modify: `internal/tui/model.go:419-426` (call at Update entry)
- Test: `internal/tui/workstate_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/workstate_test.go`:

```go
func TestAckFocusedPane_ClearsOnlyFocusedPane(t *testing.T) {
	t.Parallel()
	m := modelWithSplitActiveTab()
	focused := m.tabs[0].Root.Left.Pane  // "p1" — tab.ActivePane
	sibling := m.tabs[0].Root.Right.Pane // "p1b" — unfocused
	focused.unseen = true
	sibling.unseen = true
	m.ackFocusedPane()
	if focused.unseen {
		t.Error("the focused pane of the active tab must be acknowledged")
	}
	if !sibling.unseen {
		t.Error("an unfocused sibling must keep its mark until focused")
	}
}

func TestAckFocusedPane_BackgroundTabUntouched(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	bg := m.tabs[1].Root.Leaves()[0] // "p2" is tab-2's ActivePane, but tab-2 is background
	bg.unseen = true
	m.ackFocusedPane()
	if !bg.unseen {
		t.Error("panes on background tabs must keep their mark")
	}
}

func TestAckFocusedPane_NoTabs_NoPanic(t *testing.T) {
	t.Parallel()
	m := Model{}
	m.ackFocusedPane() // must not panic on an empty model
}

func TestUpdate_AcksFocusedPaneAtEntry(t *testing.T) {
	t.Parallel()
	// Integration: ANY message arriving means the previous frame (with the
	// focused pane visible) has been rendered — Update's entry hook clears it.
	m := modelForWorkTest()
	m.tabs[0].Root.Leaves()[0].unseen = true
	next, _ := m.Update(workSpinnerTickMsg{})
	if next.(Model).tabs[0].Root.Leaves()[0].unseen {
		t.Error("Update entry must acknowledge the focused pane of the active tab")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — compile error `m.ackFocusedPane undefined`.

- [ ] **Step 3: Implement the helper and wire it into Update**

3a. Add to `internal/tui/workstate.go`, after `applyWorkTransition`:

```go
// ackFocusedPane clears the unseen mark on the focused pane of the active
// tab. Called at the top of Update: by the time any message arrives, the
// previous frame — with that pane focused and visible — has already been
// rendered, so the user has seen it. This single choke point replaces
// auditing every ActivePane/activeTab assignment (13 call sites); a newly
// focused pane is acknowledged one message later (the 1 s size poll bounds
// the wait), and a focused pane never renders the green border anyway.
// Unfocused panes keep their mark until focused.
func (m *Model) ackFocusedPane() {
	if m.activeTab < 0 || m.activeTab >= len(m.tabs) {
		return
	}
	tab := m.tabs[m.activeTab]
	if tab == nil || tab.Root == nil || tab.ActivePane == "" {
		return
	}
	for _, p := range tab.Leaves() {
		if p != nil && p.ID == tab.ActivePane {
			p.unseen = false
			return
		}
	}
}
```

3b. In `internal/tui/model.go`, at the top of `Update` (line ~426), insert the call between the perf defer and the message switch:

```go
	defer func() {
		markUpdateEnd()
		m.perfStats.recordMsg(msgTypeName(msg), time.Since(start))
	}()
	// The previous frame rendered the active tab with its focused pane
	// visible — acknowledge its unseen mark before processing the message.
	m.ackFocusedPane()
	switch msg := msg.(type) {
```

(`Update` has a value receiver; `m` is addressable so the pointer-receiver call compiles, and the mutation lands on the shared `*PaneModel`, not the copy.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS, including the four new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/workstate.go internal/tui/model.go internal/tui/workstate_test.go
git commit -m "feat(tui): acknowledge unseen mark when its pane is focused

Single choke point at Update entry: the focused pane of the active tab
was visible in the previous frame, so any arriving message proves the
user has seen it. Avoids auditing all 13 ActivePane assignment sites."
```

---

### Task 3: Green pane border for unseen panes

**Files:**
- Modify: `internal/tui/pane.go:91-128` (`paneRenderKey` + `renderKey`), `:349-358` (border color chain)
- Test: `internal/tui/workstate_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/workstate_test.go`:

```go
func TestPaneView_UnseenBorderGreen(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("px", 1024)
	p.Width, p.Height = 24, 6

	// Baseline: no green border.
	if strings.Contains(p.View(), "38;5;28") {
		t.Fatal("baseline pane must not render the green border")
	}

	// Unseen + unfocused → green border. This also exercises renderKey
	// invalidation: without `unseen` in the key the cached baseline would
	// be returned unchanged.
	p.unseen = true
	if !strings.Contains(p.View(), "38;5;28") {
		t.Error("unseen unfocused pane should render a green border (38;5;28)")
	}

	// Focused wins over unseen — the user is looking at it.
	p.Active = true
	view := p.View()
	if strings.Contains(view, "38;5;28") {
		t.Error("focused pane must not render the green border")
	}
	if !strings.Contains(view, "38;5;57") {
		t.Error("focused pane should render the active border (38;5;57)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `unseen unfocused pane should render a green border`.

- [ ] **Step 3: Implement border color + cache key**

3a. `internal/tui/pane.go` — `paneRenderKey` struct (line ~91): add `unseen bool` next to `working`:

```go
	working                        bool
	unseen                         bool
```

3b. `renderKey()` (line ~113): add the assignment next to `working`:

```go
		working:       p.working,
		unseen:        p.unseen,
```

3c. `View()` border chain (lines 349-358) — insert the unseen check between the base color and `Active`, so a focused pane always shows the active border and ghost/MCP keep their precedence:

```go
	borderColor := lipgloss.Color("238")
	if p.unseen {
		borderColor = lipgloss.Color("28") // green — finished/parked, awaiting focus
	}
	if p.Active {
		borderColor = lipgloss.Color("57")
	}
	if p.ghost || p.resuming || p.preparing {
		borderColor = lipgloss.Color("95") // muted purple — distinct but not jarring
	}
	if p.mcpHighlight {
		borderColor = lipgloss.Color("208") // orange — MCP interaction
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/pane.go internal/tui/workstate_test.go
git commit -m "feat(tui): green border on unseen panes

With multiple agent panes in one tab, the border pinpoints exactly
which pane finished or is waiting for input. Focused, ghost, and MCP
highlight borders keep precedence."
```

---

### Task 4: Docs + full verification

**Files:**
- Modify: `.claude/CLAUDE.md` (work-in-progress indicators bullet)

- [ ] **Step 1: Update the CLAUDE.md work-indicator bullet**

In `.claude/CLAUDE.md`, the bullet starting `- Work-in-progress indicators:` documents the 5 s flash. Apply these phrase replacements (the bullet is one long line; everything else in it stays):

| Old | New |
|---|---|
| `Stop edges (→ 5 s green tab flash):` | `Stop edges (→ persistent green unseen mark on the pane):` |
| `(the agent is blocked on the user → stop spinner + flash for attention)` | `(the agent is blocked on the user → stop spinner + unseen mark for attention)` |
| `\`workStart\` clears any pending flash` | `\`workStart\` clears the pane's unseen mark` |
| `\`process_exit\` clears \`working\` WITHOUT a flash (a crash is not a completed turn)` | `\`process_exit\` clears \`working\` WITHOUT marking unseen (a crash is not a completed turn)` |
| `Inactive tabs flash via \`flashTabStyle\` — \`tabStyle(idx)\` precedence is flash > custom color > active/inactive` | `\`unseen\` lives on \`PaneModel\` (set on workStop unless the pane is the focused pane of the active tab; cleared by \`ackFocusedPane\` at the single \`Update\` entry choke point — focusing the pane is the acknowledgement, no timer). Marked panes render a green border (precedence below active/ghost/MCP-highlight); background tabs derive a green label via \`tabUnseen\` + \`unseenTabStyle\` — \`tabStyle(idx)\` precedence is unseen > custom color > active/inactive` |
| `The active tab never flashes (you're already looking at it).` | `The active tab label never shows green (you're already looking at it); an unfocused split sibling still shows its green border.` |
| `Permission/option prompts park the spinner (stop + green flash)` | `Permission/option prompts park the spinner (stop + unseen mark)` |

- [ ] **Step 2: Full test suite + vet**

Run: `./scripts/dev.sh test && ./scripts/dev.sh vet`
Expected: all packages PASS, no vet findings.

- [ ] **Step 3: Stale-identifier sweep**

Run: `grep -rn "flashUntil\|tabFlashing\|flashTabStyle\|flashTickMsg\|tabFlashDuration" internal/ cmd/ .claude/CLAUDE.md`
Expected: no matches. (Historical docs/plans/specs may still mention "flash" — only Go sources and CLAUDE.md must be clean.)

- [ ] **Step 4: Commit**

```bash
git add .claude/CLAUDE.md
git commit -m "docs: update work-indicator notes for persistent unseen marks"
```

- [ ] **Step 5: Manual dev-mode check (optional but recommended)**

Build `./scripts/dev.sh build`, run `./quil-dev.exe` (confirm `[dev]` in status bar).

1. Two tabs, claude pane on tab 2. Start a turn, switch to tab 1 before it finishes. On completion: tab 2's label goes green and STAYS green (no 5 s expiry). Alt+2 → label normal, pane focused, border normal.
2. One tab, two split claude panes. Start a turn in the left pane, focus the right pane. On completion: left pane's border goes green, tab label stays normal (active tab). Click the left pane → border reverts.
3. Repeat 1 with a permission prompt (park) instead of a completion: same green-until-focused behavior.
