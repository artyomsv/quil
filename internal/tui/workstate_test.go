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
		// SessionEnd is terminal: no subagent can outlive the session, so it
		// also clears any outstanding-subagent count (WorkEventStopFinal).
		{"hook.claude.SessionEnd", workStopFinal},
		{"hook.opencode.session.idle", workStop},
		{"hook.opencode.session.error", workStop},
		{"process_exit", workAbort},
		// Park-for-input edges: the agent is waiting on the user, so the turn
		// is effectively done until they respond → stop spinner + unseen mark.
		{"hook.claude.Notification", workStop},
		{"hook.claude.PermissionRequest", workStop},
		{"hook.opencode.permission.ask", workStop},
		// Subagent lifecycle: background subagents outlive the main turn's
		// Stop (Claude Code runs them detached by default), so they get their
		// own edges instead of riding Start/Stop.
		{"hook.claude.SubagentStart", workSubagentStart},
		{"hook.claude.SubagentStop", workSubagentStop},
		// Task-list bookkeeping is NOT an execution signal — a created task
		// may never run and completion is a manual/tool state flip.
		{"hook.claude.TaskCreated", workNone},
		{"hook.claude.TaskCompleted", workNone},
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
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
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
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
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
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p1", "hook.claude.Stop", nil)
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
	m.applyWorkTransition("p1b", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p1b", "hook.claude.Stop", nil)
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
			m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
			m.applyWorkTransition("p2", evt, nil)
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
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.PermissionRequest", nil) // park
	pane := m.tabs[1].Root.Leaves()[0]
	if pane.working {
		t.Fatal("precondition: pane should be parked (not working) before resume")
	}
	if !pane.unseen {
		t.Fatal("precondition: pane should be unseen after the park")
	}

	m.applyWorkTransition("p2", "hook.claude.PostToolUse", nil) // resume
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
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("a new turn (UserPromptSubmit) should clear a stale unseen mark")
	}
}

func TestApplyWorkTransition_AbortClearsWorkingWithoutMarking(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "process_exit", nil)
	if m.tabs[1].Root.Leaves()[0].working {
		t.Error("pane.working should be false after process_exit")
	}
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("process_exit must NOT mark the pane unseen (a crash is not a completed turn)")
	}

	// An existing mark from an earlier completion survives an abort.
	m2 := modelWithBackgroundTab()
	m2.tabs[1].Root.Leaves()[0].unseen = true
	m2.applyWorkTransition("p2", "process_exit", nil)
	if !m2.tabs[1].Root.Leaves()[0].unseen {
		t.Error("abort must not clear an existing unseen mark")
	}
}

func TestApplyWorkTransition_StopWithoutPriorStart_NoMark(t *testing.T) {
	t.Parallel()
	// A Stop with no in-progress turn (pane was already idle) must not mark.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	if m.tabs[1].Root.Leaves()[0].unseen {
		t.Error("stop on an already-idle pane must not mark the pane unseen")
	}
}

func TestApplyWorkTransition_StopWithOutstandingSubagents_KeepsSpinner(t *testing.T) {
	t.Parallel()
	// Claude Code runs subagents in the background by default: the main
	// turn's Stop (or a park-for-input edge) fires while they are still
	// working. The spinner must survive the edge and the unseen mark must be
	// deferred until the work has actually drained.
	for _, stopEdge := range []string{"hook.claude.Stop", "hook.claude.Notification"} {
		t.Run(stopEdge, func(t *testing.T) {
			t.Parallel()
			m := modelWithBackgroundTab()
			m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
			m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
			m.applyWorkTransition("p2", stopEdge, nil)
			pane := m.tabs[1].Root.Leaves()[0]
			if !pane.working {
				t.Errorf("%s with an outstanding subagent must keep the spinner", stopEdge)
			}
			if pane.unseen {
				t.Errorf("%s with an outstanding subagent must defer the unseen mark", stopEdge)
			}

			// The last subagent finishing IS the completion edge now.
			m.applyWorkTransition("p2", "hook.claude.SubagentStop", nil)
			if pane.working {
				t.Error("draining the last subagent after the turn ended must stop the spinner")
			}
			if !pane.unseen {
				t.Error("draining the last subagent after the turn ended must mark the background pane unseen")
			}
		})
	}
}

func TestApplyWorkTransition_SubagentStopBeforeStop_TurnKeepsSpinner(t *testing.T) {
	t.Parallel()
	// A subagent finishing while the main turn is still running must NOT
	// stop the spinner — the turn itself is still mid-flight.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", nil)
	pane := m.tabs[1].Root.Leaves()[0]
	if !pane.working {
		t.Error("subagent drain during an active turn must keep the spinner")
	}
	if pane.unseen {
		t.Error("subagent drain during an active turn must not mark the pane")
	}

	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	if pane.working {
		t.Error("Stop with no outstanding subagents must stop the spinner")
	}
	if !pane.unseen {
		t.Error("Stop with no outstanding subagents must mark the background pane")
	}
}

func TestApplyWorkTransition_CoalescedSubagentBursts(t *testing.T) {
	t.Parallel()
	// The daemon's ingester debounces per (paneID, hook_event) with a 50 ms
	// window: N events in a burst arrive as ONE PaneEvent carrying
	// data["coalesced"] = "N". The counter must honor the burst count or a
	// parallel spawn of 3 subagents would be undercounted as 1.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"coalesced": "3"})
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	pane := m.tabs[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"coalesced": "2"})
	if !pane.working {
		t.Fatal("2 of 3 subagents drained — one is still outstanding, spinner must stay")
	}
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", nil) // last one
	if pane.working {
		t.Error("all 3 subagents drained — spinner must stop")
	}
	if !pane.unseen {
		t.Error("all subagents drained after turn end — background pane must be marked")
	}
}

func TestApplyWorkTransition_OrphanSubagentStop_NoUnderflow(t *testing.T) {
	t.Parallel()
	// A SubagentStop with no recorded start (event replay gap, hook loss)
	// must be a no-op — and must NOT push the counter negative, which would
	// make the next SubagentStart+SubagentStop pair fail to balance.
	m := modelWithBackgroundTab()
	pane := m.tabs[1].Root.Leaves()[0]
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", nil) // orphan
	if pane.working {
		t.Fatal("orphan SubagentStop on an idle pane must not start the spinner")
	}
	if pane.unseen {
		t.Fatal("orphan SubagentStop on an idle pane must not mark the pane")
	}

	// Counter must still balance: one start + one stop = drained.
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
	if !pane.working {
		t.Fatal("SubagentStart after an orphan stop must start the spinner")
	}
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", nil)
	if pane.working {
		t.Error("counter went negative on the orphan stop — start/stop pair no longer balances")
	}
}

func TestApplyWorkTransition_SessionEndClearsOutstandingSubagents(t *testing.T) {
	t.Parallel()
	// SessionEnd (/clear, /logout, process exit path) is terminal for every
	// subagent of that session — a stale counter must not wedge the spinner.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
	m.applyWorkTransition("p2", "hook.claude.SessionEnd", nil)
	pane := m.tabs[1].Root.Leaves()[0]
	if pane.working {
		t.Error("SessionEnd must stop the spinner even with an outstanding subagent count")
	}
	if !pane.unseen {
		t.Error("SessionEnd is a genuine completion — background pane should be marked")
	}
}

func TestApplyWorkTransition_ProcessExitClearsOutstandingSubagents(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
	m.applyWorkTransition("p2", "process_exit", nil)
	pane := m.tabs[1].Root.Leaves()[0]
	if pane.working {
		t.Fatal("process_exit must clear the spinner regardless of subagent count")
	}
	if pane.unseen {
		t.Fatal("process_exit must not mark the pane (a crash is not a completed turn)")
	}

	// The stale counter must not leak into the next session: a plain
	// start → stop cycle must end idle.
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	if pane.working {
		t.Error("a pre-exit subagent count leaked into the next turn and wedged the spinner")
	}
}

func TestApplyWorkTransition_SubagentStartFromIdle_SetsWorkingAndClearsUnseen(t *testing.T) {
	t.Parallel()
	// Between the main turn's Stop and the harness's synthetic resume, a new
	// subagent can spawn (or an event replay can start mid-cycle): the spawn
	// alone must light the spinner and supersede a stale unseen mark.
	m := modelWithBackgroundTab()
	pane := m.tabs[1].Root.Leaves()[0]
	pane.unseen = true
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
	if !pane.working {
		t.Error("SubagentStart on an idle pane must start the spinner")
	}
	if pane.unseen {
		t.Error("SubagentStart must clear a stale unseen mark — work is in progress again")
	}
}

func TestCoalescedCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data map[string]string
		want int
	}{
		{"nil map", nil, 1},
		{"missing key", map[string]string{"other": "x"}, 1},
		{"plain burst", map[string]string{"coalesced": "3"}, 3},
		{"burst of one", map[string]string{"coalesced": "1"}, 1},
		{"zero rejected", map[string]string{"coalesced": "0"}, 1},
		{"negative rejected", map[string]string{"coalesced": "-2"}, 1},
		{"malformed rejected", map[string]string{"coalesced": "abc"}, 1},
		{"empty value rejected", map[string]string{"coalesced": ""}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := coalescedCount(tt.data); got != tt.want {
				t.Errorf("coalescedCount(%v) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestApplyWorkTransition_UnknownPane_NoPanic(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("does-not-exist", "hook.claude.Stop", nil) // must not panic
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

func TestUpdate_PaneEvent_MutedPaneTracksWorkingWithoutCard(t *testing.T) {
	t.Parallel()
	// A muted pane's daemon still forwards work-state hook events live (see
	// daemon.emitEvent) so `working` stays accurate across mute/unmute — but
	// muting must still suppress the visible sidebar card.
	m := modelForWorkTest()
	m.tabs[0].Root.Leaves()[0].Muted = true

	start := paneEventMsg(ipc.PaneEventPayload{
		ID: "e1", PaneID: "p1", Type: "hook.claude.UserPromptSubmit", Title: "Working on: x",
	})
	next, _ := m.Update(start)
	nm := next.(Model)
	if !nm.tabs[0].Root.Leaves()[0].working {
		t.Fatal("muted pane should still track working=true from a live work-state event")
	}
	if nm.notifications.Count() != 0 {
		t.Errorf("muted pane must not produce a sidebar card: got %d events", nm.notifications.Count())
	}

	stop := paneEventMsg(ipc.PaneEventPayload{
		ID: "e2", PaneID: "p1", Type: "hook.claude.Stop", Title: "Done",
	})
	next2, _ := nm.Update(stop)
	nm2 := next2.(Model)
	if nm2.tabs[0].Root.Leaves()[0].working {
		t.Error("muted pane should still clear working=false from a live Stop event")
	}
	if nm2.notifications.Count() != 0 {
		t.Errorf("muted pane must not produce a sidebar card on stop: got %d events", nm2.notifications.Count())
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

func TestSyncPaneMeta_SetsWideCanvas(t *testing.T) {
	t.Parallel()
	// The flag is passed explicitly (resolved against the live registry by
	// the caller) so every reconciliation path re-evaluates it — a plugin
	// migration mid-session must be able to flip it in both directions.
	pane := NewPaneModel("p", 1024)
	syncPaneMeta(pane, &PaneInfo{Type: "claude-code"}, true, 0)
	if !pane.WideCanvas {
		t.Error("syncPaneMeta must set WideCanvas from the passed flag (true)")
	}
	syncPaneMeta(pane, &PaneInfo{Type: "claude-code"}, false, 0)
	if pane.WideCanvas {
		t.Error("syncPaneMeta must clear WideCanvas when the flag flips to false")
	}
}

func TestSyncPaneMeta_MuteDoesNotDisturbWorking(t *testing.T) {
	t.Parallel()
	// The daemon still delivers work-state hook events live for a muted pane
	// (see daemon.emitEvent), so a metadata sync must NOT clobber `working`
	// just because the pane is muted — otherwise a real completion event
	// racing a workspace-state broadcast would get its effect reverted, and
	// the spinner would never reappear after unmuting a still-working pane.
	pane := NewPaneModel("p1", 1024)
	pane.working = true
	syncPaneMeta(pane, &PaneInfo{Muted: true}, false, 0)
	if !pane.working {
		t.Error("a mute metadata sync must not clear working")
	}

	pane2 := NewPaneModel("p2", 1024)
	pane2.working = true
	syncPaneMeta(pane2, &PaneInfo{Muted: false}, false, 0)
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
	// Integration: Update's entry hook acknowledges the focused pane of the
	// active tab on every message — focusing is the acknowledgement (see
	// ackFocusedPane; a focused pane never renders the mark anyway).
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
