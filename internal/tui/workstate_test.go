package tui

import (
	"strings"
	"testing"

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
		{"hook.claude.PostToolUse", workStart}, // resume after a prompt is answered
		{"hook.claude.Stop", workStop},
		{"hook.claude.SessionEnd", workStop},
		{"hook.opencode.session.idle", workStop},
		{"hook.opencode.session.error", workStop},
		{"process_exit", workAbort},
		// Park-for-input edges: the agent is waiting on the user, so the turn
		// is effectively done until they respond → stop spinner + unseen mark.
		{"hook.claude.Notification", workStop},
		{"hook.claude.PermissionRequest", workStop},
		{"hook.opencode.permission.ask", workStop},
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

func TestApplyWorkTransition_StopWithoutPriorStart_NoMark(t *testing.T) {
	t.Parallel()
	// A Stop with no in-progress turn (pane was already idle) must not mark.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.Stop")
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("stop on an already-idle pane must not mark the pane unseen")
	}
}

func TestApplyWorkTransition_UnknownPane_NoPanic(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("does-not-exist", "hook.claude.Stop") // must not panic
}

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

func TestSyncPaneMeta_MuteClearsWorking(t *testing.T) {
	t.Parallel()
	// Muting a pane mid-turn must clear `working`: the daemon drops a muted
	// pane's hook events, so the completion edge that would clear it never
	// arrives — otherwise the spinner (and the 100ms tick) would run forever.
	pane := NewPaneModel("p1", 1024)
	pane.working = true
	syncPaneMeta(pane, &PaneInfo{Muted: true})
	if pane.working {
		t.Error("muting a pane must clear its working indicator")
	}

	// An unmuted metadata sync must not disturb working.
	pane2 := NewPaneModel("p2", 1024)
	pane2.working = true
	syncPaneMeta(pane2, &PaneInfo{Muted: false})
	if !pane2.working {
		t.Error("a non-mute metadata sync must not clear working")
	}
}

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

func TestWorkSpinnerTick_FrameWraparoundMirrors(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.tabs[0].Root.Leaves()[0].working = true
	m.workTickRunning = true
	// Push the frame to the last index so the next tick crosses the modulo
	// boundary — the raw frame keeps incrementing and the pane mirror must
	// track it without any out-of-range glyph indexing.
	m.workSpinnerFrame = len(spinnerFrames) - 1
	next, _ := m.Update(workSpinnerTickMsg{})
	nm := next.(Model)
	if nm.workSpinnerFrame != len(spinnerFrames) {
		t.Fatalf("frame = %d, want %d", nm.workSpinnerFrame, len(spinnerFrames))
	}
	if nm.tabs[0].Root.Leaves()[0].workFrame != len(spinnerFrames) {
		t.Errorf("pane.workFrame = %d, want %d (mirrors raw frame)",
			nm.tabs[0].Root.Leaves()[0].workFrame, len(spinnerFrames))
	}
	// Rendering at the wrapped frame must not panic (modulo guards the index).
	_ = nm.tabLabel(0)
}
