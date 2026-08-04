package tui

import (
	"strconv"
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
		// Park-for-input edges: the agent is waiting on the user — distinct
		// from a completed turn, though both stop the spinner + mark unseen.
		{"hook.claude.Notification", workPark},
		{"hook.claude.PermissionRequest", workPark},
		{"hook.opencode.permission.ask", workPark},
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
		projects:      oneProject(tab),
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
	m.appendTab(tab2)
	m.setActiveTabIdx(0)
	return m
}

// modelWithSplitActiveTab extends modelForWorkTest with a second pane "p1b"
// split into the active tab. "p1" stays the focused pane (tab.ActivePane), so
// transitions on "p1b" exercise the unfocused-sibling marking rules.
func modelWithSplitActiveTab() Model {
	m := modelForWorkTest()
	m.curTabs()[0].Root = &LayoutNode{
		Split: SplitHorizontal,
		Ratio: 0.5,
		Left:  m.curTabs()[0].Root,
		Right: NewLeaf(NewPaneModel("p1b", 1024)),
	}
	m.curTabs()[0].invalidateLeaves()
	return m
}

func TestApplyWorkTransition_StartSetsWorking(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	if !m.curTabs()[0].Root.Leaves()[0].working {
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
	if m.curTabs()[1].Root.Leaves()[0].working {
		t.Error("pane.working should be false after stop")
	}
	if !m.curTabs()[1].Root.Leaves()[0].unseen {
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
	if m.curTabs()[0].Root.Leaves()[0].working {
		t.Error("pane.working should be false after stop")
	}
	if m.curTabs()[0].Root.Leaves()[0].unseen {
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
	if !m.curTabs()[0].Root.Right.Pane.unseen {
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
			if m.curTabs()[1].Root.Leaves()[0].working {
				t.Errorf("%s: pane.working should be false after a park-for-input edge", evt)
			}
			if !m.curTabs()[1].Root.Leaves()[0].unseen {
				t.Errorf("%s: pane should be marked unseen when the agent parks", evt)
			}
		})
	}
}

func TestApplyWorkTransition_ParkSetsBlockedFields(t *testing.T) {
	t.Parallel()
	// A park edge must stamp blockedSince and carry the tool name when the
	// hook supplies one (PermissionRequest), and leave blockedReason empty
	// rather than inventing one when it doesn't (Notification).
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p1", "hook.claude.PermissionRequest", map[string]string{"tool": "Bash"})
	pane := m.curTabs()[0].Root.Leaves()[0]
	if pane.blockedSince.IsZero() {
		t.Fatal("a park edge must set blockedSince")
	}
	if pane.blockedReason != "Bash" {
		t.Errorf("blockedReason = %q, want %q", pane.blockedReason, "Bash")
	}

	m2 := modelForWorkTest()
	m2.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	m2.applyWorkTransition("p1", "hook.claude.Notification", nil)
	pane2 := m2.curTabs()[0].Root.Leaves()[0]
	if pane2.blockedSince.IsZero() {
		t.Fatal("a park edge must set blockedSince even without a tool")
	}
	if pane2.blockedReason != "" {
		t.Errorf("blockedReason = %q, want empty — Notification carries no tool", pane2.blockedReason)
	}
}

func TestApplyWorkTransition_StartAndAbortClearBlockedFields(t *testing.T) {
	t.Parallel()
	// A fresh turn and a process exit must both clear a lingering blocked
	// mark from a prior park, same as they clear the unseen mark.
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p1", "hook.claude.PermissionRequest", map[string]string{"tool": "Bash"})
	pane := m.curTabs()[0].Root.Leaves()[0]
	if pane.blockedSince.IsZero() {
		t.Fatal("precondition: pane should be blocked after the park")
	}

	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	if !pane.blockedSince.IsZero() || pane.blockedReason != "" {
		t.Error("a new turn (UserPromptSubmit) must clear the blocked fields")
	}

	m2 := modelForWorkTest()
	m2.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	m2.applyWorkTransition("p1", "hook.claude.PermissionRequest", map[string]string{"tool": "Bash"})
	pane2 := m2.curTabs()[0].Root.Leaves()[0]
	m2.applyWorkTransition("p1", "process_exit", nil)
	if !pane2.blockedSince.IsZero() || pane2.blockedReason != "" {
		t.Error("process_exit must clear the blocked fields")
	}
}

// TestTurnCompletionClearsTheBlockedMark guards the non-obvious clearing
// edge: approving a permission prompt fires no hook of its own, so the
// pane's next event is the turn's Stop. If a plain workStop did not clear
// the blocked fields, the sidebar would keep showing the ⚠ marker on a pane
// that has already finished its turn, until the user submits another
// prompt — a completed turn is by definition not blocked.
func TestTurnCompletionClearsTheBlockedMark(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "pane-1"}
	tab := NewTabModel("tab-1", "AI")
	tab.Root = NewLeaf(pane)
	m := Model{projects: []*ProjectModel{{ID: "proj-a", tabs: []*TabModel{tab}}}}

	m.applyWorkTransition("pane-1", "hook.claude.PermissionRequest", map[string]string{"tool": "Bash"})
	if pane.blockedSince.IsZero() {
		t.Fatal("a permission request must mark the pane blocked")
	}

	// Approving fires no hook; the next event is the turn completing.
	m.applyWorkTransition("pane-1", "hook.claude.Stop", nil)

	if !pane.blockedSince.IsZero() {
		t.Fatal("a completed turn must clear the blocked mark — otherwise ⚠ " +
			"sticks on a pane that is done")
	}
}

func TestApplyWorkTransition_ResumeAfterParkClearsUnseenAndReArms(t *testing.T) {
	t.Parallel()
	// Full prompt cycle on a background pane: start → park (spinner off +
	// unseen) → user answers (PostToolUse) → spinner back on, mark cleared.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.PermissionRequest", nil) // park
	pane := m.curTabs()[1].Root.Leaves()[0]
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
	m.curTabs()[1].Root.Leaves()[0].unseen = true
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	if m.curTabs()[1].Root.Leaves()[0].unseen {
		t.Error("a new turn (UserPromptSubmit) should clear a stale unseen mark")
	}
}

func TestApplyWorkTransition_AbortClearsWorkingWithoutMarking(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "process_exit", nil)
	if m.curTabs()[1].Root.Leaves()[0].working {
		t.Error("pane.working should be false after process_exit")
	}
	if m.curTabs()[1].Root.Leaves()[0].unseen {
		t.Error("process_exit must NOT mark the pane unseen (a crash is not a completed turn)")
	}

	// An existing mark from an earlier completion survives an abort.
	m2 := modelWithBackgroundTab()
	m2.curTabs()[1].Root.Leaves()[0].unseen = true
	m2.applyWorkTransition("p2", "process_exit", nil)
	if !m2.curTabs()[1].Root.Leaves()[0].unseen {
		t.Error("abort must not clear an existing unseen mark")
	}
}

func TestApplyWorkTransition_StopWithoutPriorStart_NoMark(t *testing.T) {
	t.Parallel()
	// A Stop with no in-progress turn (pane was already idle) must not mark.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	if m.curTabs()[1].Root.Leaves()[0].unseen {
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
			m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore"})
			m.applyWorkTransition("p2", stopEdge, nil)
			pane := m.curTabs()[1].Root.Leaves()[0]
			if !pane.working {
				t.Errorf("%s with an outstanding subagent must keep the spinner", stopEdge)
			}
			if pane.unseen {
				t.Errorf("%s with an outstanding subagent must defer the unseen mark", stopEdge)
			}

			// The last subagent finishing IS the completion edge now.
			m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "Explore"})
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
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore"})
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "Explore"})
	pane := m.curTabs()[1].Root.Leaves()[0]
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
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore", "coalesced": "3"})
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "Explore", "coalesced": "2"})
	if !pane.working {
		t.Fatal("2 of 3 subagents drained — one is still outstanding, spinner must stay")
	}
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "Explore"}) // last one
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
	pane := m.curTabs()[1].Root.Leaves()[0]
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "Explore"}) // orphan
	if pane.working {
		t.Fatal("orphan SubagentStop on an idle pane must not start the spinner")
	}
	if pane.unseen {
		t.Fatal("orphan SubagentStop on an idle pane must not mark the pane")
	}

	// Counter must still balance: one start + one stop = drained.
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore"})
	if !pane.working {
		t.Fatal("SubagentStart after an orphan stop must start the spinner")
	}
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "Explore"})
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
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore"})
	m.applyWorkTransition("p2", "hook.claude.SessionEnd", nil)
	pane := m.curTabs()[1].Root.Leaves()[0]
	if pane.working {
		t.Error("SessionEnd must stop the spinner even with an outstanding subagent count")
	}
	if !pane.unseen {
		t.Error("SessionEnd is a genuine completion — background pane should be marked")
	}
}

func TestApplyWorkTransition_SubagentStartWithoutAgentType_IsRefused(t *testing.T) {
	t.Parallel()
	// The ledger's whole guarantee is that the empty key is NEVER live —
	// that is what makes the unpaired end-of-turn stop (which always carries
	// an empty agent_type) unable to cancel anything. Measured: Claude Code
	// never emits a start without naming its agent. Enforce it rather than
	// assume it: if the producer ever renames or drops the field, both edges
	// would collapse onto the empty key and the phantom would silently drain
	// real work again, with no test failing to say so.
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": ""})
	if pane.working {
		t.Error("a SubagentStart naming no agent must not enter the ledger")
	}
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", nil)
	if pane.working {
		t.Error("a SubagentStart with no data at all must not enter the ledger")
	}
	if len(pane.subagents) != 0 {
		t.Errorf("ledger must stay empty; got %v", pane.subagents)
	}
}

func TestApplyWorkTransition_OverCountedStopDrainsWithoutWedging(t *testing.T) {
	t.Parallel()
	// data["coalesced"] is producer-controlled and the ingester only rewrites
	// it on a real burst, so a stop can claim a larger count than the ledger
	// holds. The entry must be removed rather than left negative: len() > 0
	// is the spinner's input, so a negative-valued entry would wedge it ON
	// until SessionEnd — the mirror of the bug this branch fixes.
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "A"})
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "A", "coalesced": "3"})

	if pane.working {
		t.Error("over-counted stop left the spinner lit — the ledger entry was not removed")
	}
	if len(pane.subagents) != 0 {
		t.Errorf("over-counted stop left a residual entry: %v", pane.subagents)
	}
}

func TestApplyWorkTransition_LedgerIsBounded(t *testing.T) {
	t.Parallel()
	// agent_type is producer-controlled, so the ledger's key cardinality is
	// too. Cap it: the pane's own child can otherwise grow the map without
	// limit in a TUI process that runs for weeks. Refusing new keys at the
	// ceiling cannot turn the spinner off — `working` derives from len() > 0,
	// which is already true once the ledger is full.
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	for i := 0; i < maxTrackedSubagents*3; i++ {
		m.applyWorkTransition("p2", "hook.claude.SubagentStart",
			map[string]string{"agent_type": "agent-" + strconv.Itoa(i)})
	}
	if len(pane.subagents) > maxTrackedSubagents {
		t.Errorf("ledger grew to %d entries, want <= %d", len(pane.subagents), maxTrackedSubagents)
	}
	if !pane.working {
		t.Error("a full ledger must still report work in progress")
	}

	// A terminal edge still clears it completely.
	m.applyWorkTransition("p2", "hook.claude.SessionEnd", nil)
	if len(pane.subagents) != 0 || pane.working {
		t.Errorf("SessionEnd must clear a capped ledger; got %d entries, working=%v",
			len(pane.subagents), pane.working)
	}
}

func TestApplyWorkTransition_OverflowedAgentsKeepSpinnerLit(t *testing.T) {
	t.Parallel()
	// The cap discards a start it cannot name-track, and that agent is then
	// invisible to the ledger. Draining the tracked ones must NOT declare the
	// pane idle: the discarded agent may still be running, and turning the
	// spinner off while work is in flight is the exact bug this whole branch
	// exists to fix. Overflow is therefore sticky until a terminal edge —
	// wrong-on is the safe direction, wrong-off is the one users report.
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	total := maxTrackedSubagents + 5
	for i := 0; i < total; i++ {
		m.applyWorkTransition("p2", "hook.claude.SubagentStart",
			map[string]string{"agent_type": "agent-" + strconv.Itoa(i)})
	}
	// Drain every agent the ledger managed to track.
	for i := 0; i < total; i++ {
		m.applyWorkTransition("p2", "hook.claude.SubagentStop",
			map[string]string{"agent_type": "agent-" + strconv.Itoa(i)})
	}
	if len(pane.subagents) != 0 {
		t.Fatalf("precondition: every tracked agent should have drained; got %v", pane.subagents)
	}
	if !pane.working {
		t.Error("spinner went dark while agents refused by the cap may still be running")
	}

	// SessionEnd is terminal for the whole session — nothing can still be live.
	m.applyWorkTransition("p2", "hook.claude.SessionEnd", nil)
	if pane.working {
		t.Error("SessionEnd must clear the overflow flag, not leave the spinner wedged forever")
	}
}

func TestApplyWorkTransition_PhantomSubagentStop_DoesNotDrainNamedAgent(t *testing.T) {
	t.Parallel()
	// Claude Code emits ONE unpaired SubagentStop carrying an empty agent_type
	// at the end of every main turn — measured 1:1 against Stop across every
	// AI pane in a real workspace, and a SubagentStart with an empty
	// agent_type never occurs. It names no live agent, so it must cancel
	// nothing. A ledger that treats stops as fungible spends it on whichever
	// background agent happens to be outstanding, and the spinner goes dark
	// while that agent is still working.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "impl-task7"})
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	pane := m.curTabs()[1].Root.Leaves()[0]
	if !pane.working {
		t.Fatal("precondition: Stop with an outstanding subagent must keep the spinner")
	}

	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": ""})
	if !pane.working {
		t.Error("phantom SubagentStop (empty agent_type) drained a live named agent and killed the spinner")
	}
	if pane.unseen {
		t.Error("phantom SubagentStop must not produce a completion mark — nothing completed")
	}

	// The agent's own stop is still the completion edge.
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "impl-task7"})
	if pane.working {
		t.Error("the named agent's own SubagentStop must drain it and stop the spinner")
	}
	if !pane.unseen {
		t.Error("draining the last agent after the turn ended must mark the background pane")
	}
}

func TestApplyWorkTransition_SubagentStopForUnknownAgent_DoesNotDrainAnother(t *testing.T) {
	t.Parallel()
	// Identity, not arithmetic: a stop may only cancel a start it can be
	// matched to. A stop naming an agent this pane never saw start (replay
	// gap, lost start, a stop from a session we did not track) must be
	// ignored rather than spent on an unrelated live agent.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "impl-task7"})
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "rev-task7"})
	pane := m.curTabs()[1].Root.Leaves()[0]
	if !pane.working {
		t.Error("a stop naming a different agent drained impl-task7 — stops must be matched by identity")
	}
}

func TestApplyWorkTransition_ConcurrentNamedAgentsDrainIndependently(t *testing.T) {
	t.Parallel()
	// Two background agents in flight: each is drained only by its own stop,
	// and the spinner survives until the last one is gone.
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "impl-task7"})
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "rev-task7"})
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "rev-task7"})
	if !pane.working {
		t.Fatal("impl-task7 is still outstanding — spinner must stay lit")
	}
	// A repeat stop for an agent already drained must not consume the other.
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "rev-task7"})
	if !pane.working {
		t.Error("a duplicate stop for an already-drained agent consumed impl-task7's slot")
	}

	m.applyWorkTransition("p2", "hook.claude.SubagentStop", map[string]string{"agent_type": "impl-task7"})
	if pane.working {
		t.Error("both agents drained — spinner must stop")
	}
}

func TestApplyWorkTransition_ProductionPhantomSequence_KeepsSpinnerLit(t *testing.T) {
	t.Parallel()
	// Regression: the exact event order captured from pane-8ebb2d53's spool on
	// 2026-08-02, where the work indicator went dark while impl-task7 ran for
	// 27 minutes. Each main turn ends Stop → phantom SubagentStop(""), and one
	// of those phantoms landed 2 s after impl-task7 spawned.
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]
	named := func(t string) map[string]string { return map[string]string{"agent_type": t} }
	phantom := map[string]string{"agent_type": ""}

	m.applyWorkTransition("p2", "hook.claude.SubagentStop", named("rev-task7")) // drains the prior agent
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", named("impl-task7"))
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", phantom)
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStop", phantom)
	m.applyWorkTransition("p2", "hook.claude.Notification", nil)
	// Several further turns run to completion while impl-task7 keeps working.
	for i := 0; i < 3; i++ {
		m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
		m.applyWorkTransition("p2", "hook.claude.Stop", nil)
		m.applyWorkTransition("p2", "hook.claude.SubagentStop", phantom)
		m.applyWorkTransition("p2", "hook.claude.Notification", nil)
	}

	if !pane.working {
		t.Error("impl-task7 never stopped — the spinner must still be lit after the phantom stops")
	}
}

func TestApplyWorkTransition_ProcessExitClearsOutstandingSubagents(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore"})
	m.applyWorkTransition("p2", "process_exit", nil)
	pane := m.curTabs()[1].Root.Leaves()[0]
	if len(pane.subagents) != 0 {
		t.Fatalf("process_exit must clear the subagent ledger; got %v", pane.subagents)
	}
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
	pane := m.curTabs()[1].Root.Leaves()[0]
	pane.unseen = true
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "Explore"})
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
	m.curTabs()[1].Root.Leaves()[0].unseen = true
	if !m.tabUnseen(1) {
		t.Error("background tab with an unseen pane must report true")
	}
	// The same tab reports false the moment it is active — the label cue is
	// suppressed while the user is on the tab (the pane border takes over).
	m.setActiveTabIdx(1)
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
	m.curTabs()[1].Root.Leaves()[0].unseen = true
	if !strings.Contains(m.tabStyle(1).Render("x"), "48;5;28") {
		t.Error("unseen background tab should render with green background (48;5;28)")
	}

	// Active tab never renders the green label, even with an unseen pane.
	m.curTabs()[0].Root.Leaves()[0].unseen = true
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
	m.curTabs()[0].Root.Leaves()[0].Muted = true

	start := paneEventMsg(ipc.PaneEventPayload{
		ID: "e1", PaneID: "p1", Type: "hook.claude.UserPromptSubmit", Title: "Working on: x",
	})
	next, _ := m.Update(start)
	nm := next.(Model)
	if !nm.curTabs()[0].Root.Leaves()[0].working {
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
	if nm2.curTabs()[0].Root.Leaves()[0].working {
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
	m.curTabs()[0].Root.Leaves()[0].working = true
	m.workTickRunning = true
	next, cmd := m.Update(workSpinnerTickMsg{})
	nm := next.(Model)
	if nm.workSpinnerFrame != 1 {
		t.Errorf("frame = %d, want 1", nm.workSpinnerFrame)
	}
	if nm.curTabs()[0].Root.Leaves()[0].workFrame != 1 {
		t.Errorf("pane.workFrame = %d, want 1 (mirrored)", nm.curTabs()[0].Root.Leaves()[0].workFrame)
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
	m.curTabs()[0].Name = "Build"
	m.workSpinnerFrame = 0 // spinnerFrames[0] == "⠋"

	// Not working: no spinner glyph.
	if got := m.tabLabel(0); strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("idle tab label %q should not contain a spinner", got)
	}

	// Working: leading spinner frame present.
	m.curTabs()[0].Root.Leaves()[0].working = true
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
	focused := m.curTabs()[0].Root.Left.Pane  // "p1" — tab.ActivePane
	sibling := m.curTabs()[0].Root.Right.Pane // "p1b" — unfocused
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
	bg := m.curTabs()[1].Root.Leaves()[0] // "p2" is tab-2's ActivePane, but tab-2 is background
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
	m.curTabs()[0].Root.Leaves()[0].unseen = true
	next, _ := m.Update(workSpinnerTickMsg{})
	nextM := next.(Model)
	if nextM.curTabs()[0].Root.Leaves()[0].unseen {
		t.Error("Update entry must acknowledge the focused pane of the active tab")
	}
}

func TestWorkSpinnerTick_FrameWraparoundMirrors(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	m.curTabs()[0].Root.Leaves()[0].working = true
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
	if nm.curTabs()[0].Root.Leaves()[0].workFrame != len(spinnerFrames) {
		t.Errorf("pane.workFrame = %d, want %d (mirrors raw frame)",
			nm.curTabs()[0].Root.Leaves()[0].workFrame, len(spinnerFrames))
	}
	// Rendering at the wrapped frame must not panic (modulo guards the index).
	_ = nm.tabLabel(0)
}

// Task 6: cross-project resolution. Tasks 1-4 built the daemon-side project
// layer; Task 5 scoped most client reads to the active project. These sites
// are the minority that must span every project instead.

// A hook event for a pane in a BACKGROUND project must still update that
// pane's working state — that is the whole point of the sidebar: an agent
// working in a project the user isn't currently looking at still needs its
// spinner and unseen mark tracked.
func TestPaneEventFromBackgroundProjectUpdatesState(t *testing.T) {
	bgPane := &PaneModel{ID: "pane-bg"}
	bg := NewTabModel("tab-bg", "Agent")
	bg.Root = NewLeaf(bgPane)

	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-fg", tabs: []*TabModel{NewTabModel("tab-fg", "Shell")}},
			{ID: "proj-bg", tabs: []*TabModel{bg}},
		},
		activeProject: 0, // the event's pane is NOT in the active project
	}

	m.applyWorkTransition("pane-bg", "hook.claude.UserPromptSubmit", nil)

	if !bgPane.working {
		t.Fatal("a pane event for a background project must still update it — " +
			"this is the whole point of the sidebar")
	}
}

func TestFindPaneAndTabReportsOwningProject(t *testing.T) {
	tab := NewTabModel("tab-bg", "Agent")
	tab.Root = NewLeaf(&PaneModel{ID: "pane-bg"})
	m := Model{projects: []*ProjectModel{
		{ID: "proj-fg", tabs: []*TabModel{NewTabModel("tab-fg", "Shell")}},
		{ID: "proj-bg", tabs: []*TabModel{tab}},
	}}

	pane, proj, idx := m.findPaneAndTab("pane-bg")
	if pane == nil || proj == nil || proj.ID != "proj-bg" || idx != 0 {
		t.Fatalf("findPaneAndTab = (%v, %v, %d), want proj-bg tab 0", pane, proj, idx)
	}
}

// twoProjectModel builds a Model with two projects: "proj-fg" (active,
// holding pane "p-fg") and "proj-bg" (background, holding pane "p-bg").
func twoProjectModel() Model {
	fgTab := NewTabModel("tab-fg", "Shell")
	fgTab.Root = NewLeaf(NewPaneModel("p-fg", 1024))
	fgTab.ActivePane = "p-fg"
	fg := &ProjectModel{ID: "proj-fg", Name: "Foreground", tabs: []*TabModel{fgTab}}

	bgTab := NewTabModel("tab-bg", "Agent")
	bgTab.Root = NewLeaf(NewPaneModel("p-bg", 1024))
	bgTab.ActivePane = "p-bg"
	bg := &ProjectModel{ID: "proj-bg", Name: "Background", tabs: []*TabModel{bgTab}}

	return Model{projects: []*ProjectModel{fg, bg}, activeProject: 0}
}

func TestJumpToPane_SwitchesProjectAndTab(t *testing.T) {
	t.Parallel()
	m := twoProjectModel()
	if !m.jumpToPane("p-bg") {
		t.Fatal("jumpToPane should report success for an existing pane")
	}
	if m.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 (proj-bg)", m.activeProject)
	}
	if got := m.activeTabModel(); got == nil || got.ID != "tab-bg" {
		t.Fatalf("activeTabModel = %v, want tab-bg", got)
	}
	if m.activeTabModel().ActivePane != "p-bg" {
		t.Fatalf("ActivePane = %q, want p-bg", m.activeTabModel().ActivePane)
	}
}

// TestJumpToPane_TellsTheOwningDaemonAboutTheTab: jumpToPane writes
// proj.activeTab directly rather than going through switchTab, so nothing told
// the daemon. After a lazy restore that matters — the daemon spawns a tab's
// deferred panes on the switch, so a jump into a background tab landed the user
// on panes that were still Pending, with no process behind them.
func TestJumpToPane_TellsTheOwningDaemonAboutTheTab(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := twoProjectModel()
	m.client = fake
	m.projects[1].Dest = "gpu01"
	m.projects[1].tabs[0].Dest = "gpu01"

	if !m.jumpToPane("p-bg") {
		t.Fatal("jumpToPane should report success for an existing pane")
	}

	var switched *ipc.Message
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchTab {
			switched = msg
		}
	}
	if switched == nil {
		t.Fatal("jumpToPane sent no MsgSwitchTab — the target tab's panes stay Pending")
	}
	var payload ipc.SwitchTabPayload
	if err := switched.DecodePayload(&payload); err != nil {
		t.Fatalf("decode switch payload: %v", err)
	}
	if payload.TabID != "tab-bg" {
		t.Errorf("MsgSwitchTab TabID = %q, want tab-bg", payload.TabID)
	}
	// Stamped for the tab's OWN daemon: an unstamped send resolves to whatever
	// the router's current dest is, which is the wrong machine as often as not.
	if switched.Origin != "gpu01" {
		t.Errorf("MsgSwitchTab Origin = %q, want gpu01", switched.Origin)
	}
}

func TestJumpToPane_MissingPaneReturnsFalseWithoutMutating(t *testing.T) {
	t.Parallel()
	m := twoProjectModel()
	if m.jumpToPane("does-not-exist") {
		t.Fatal("jumpToPane should report failure for a pane that does not exist")
	}
	if m.activeProject != 0 {
		t.Fatalf("activeProject changed to %d on a failed jump", m.activeProject)
	}
}

// MCP set_active_pane must reach a pane in a background project instead of
// silently no-oping, which is what curTabs()-scoped resolution used to do.
func TestUpdate_SetActivePaneMsg_CrossProject(t *testing.T) {
	t.Parallel()
	m := twoProjectModel()
	next, _ := m.Update(setActivePaneMsg{PaneID: "p-bg"})
	got := next.(Model)
	if got.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 (proj-bg)", got.activeProject)
	}
	if got.activeTabModel() == nil || got.activeTabModel().ActivePane != "p-bg" {
		t.Fatal("set_active_pane did not focus the background project's pane")
	}
}

func TestUpdate_SetActivePaneMsg_MissingPaneNoOps(t *testing.T) {
	t.Parallel()
	m := twoProjectModel()
	next, _ := m.Update(setActivePaneMsg{PaneID: "does-not-exist"})
	got := next.(Model)
	if got.activeProject != 0 {
		t.Fatalf("activeProject changed to %d for a pane that does not exist", got.activeProject)
	}
}

// Sidebar notification navigation must jump across projects, and must not
// grow pane-navigation history for a jump that never happened (the pane
// named by the event no longer exists).
func TestHandleNotificationKey_Navigate_CrossProject(t *testing.T) {
	t.Parallel()
	m := twoProjectModel()
	m.notifications = NewNotificationCenter(30, 50)
	m.notifications.AddEvent(ipc.PaneEventPayload{ID: "e1", PaneID: "p-bg"})

	next, _ := m.handleNotificationKey("enter")
	got := next.(Model)
	if got.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 (proj-bg)", got.activeProject)
	}
	if len(got.paneHistory) != 1 || got.paneHistory[0].ProjectID != "proj-fg" || got.paneHistory[0].PaneID != "p-fg" {
		t.Fatalf("paneHistory = %+v, want one entry recording proj-fg/p-fg", got.paneHistory)
	}
}

func TestHandleNotificationKey_Navigate_FailedJumpDoesNotGrowHistory(t *testing.T) {
	t.Parallel()
	m := twoProjectModel()
	m.notifications = NewNotificationCenter(30, 50)
	m.notifications.AddEvent(ipc.PaneEventPayload{ID: "e1", PaneID: "does-not-exist"})

	next, _ := m.handleNotificationKey("enter")
	got := next.(Model)
	if got.activeProject != 0 {
		t.Fatalf("activeProject changed to %d for a jump that should have failed", got.activeProject)
	}
	if len(got.paneHistory) != 0 {
		t.Fatalf("paneHistory grew to %d entries for a jump that never happened", len(got.paneHistory))
	}
}

// TestApplyWorkTransition_ReplayOrderDecidesTheFinalState pins the contract the
// daemon's attach replay has to satisfy. applyWorkTransition is an ordered
// state machine, so the sequence it is fed IS the answer — feeding a pane's
// history backwards reports the state implied by its oldest event.
//
// The sequence below is the one logged for pane-fa75ba78 on 2026-08-03: a turn
// resumed, replied, then parked waiting for the user. Played forwards the pane
// is blocked, which is where it really was. Played backwards it is working,
// which is what the user saw after restarting the TUI — a spinner with nothing
// behind it.
func TestApplyWorkTransition_ReplayOrderDecidesTheFinalState(t *testing.T) {
	t.Parallel()
	history := []string{
		"hook.claude.PostToolUse",   // resumed after AskUserQuestion
		"hook.claude.Stop",          // reply ready
		"hook.claude.Notification",  // parked: waiting for input
	}

	forwards := modelForWorkTest()
	for _, evt := range history {
		forwards.applyWorkTransition("p1", evt, nil)
	}
	pane := forwards.curTabs()[0].Root.Leaves()[0]
	if pane.working {
		t.Error("chronological replay left the pane working; its last event was a park")
	}
	if pane.blockedSince.IsZero() {
		t.Error("chronological replay did not mark the pane blocked")
	}

	// The failing direction, asserted so the daemon-side ordering fix has a
	// stated reason to exist rather than looking like a cosmetic reversal.
	backwards := modelForWorkTest()
	for i := len(history) - 1; i >= 0; i-- {
		backwards.applyWorkTransition("p1", history[i], nil)
	}
	if got := backwards.curTabs()[0].Root.Leaves()[0]; !got.working || !got.blockedSince.IsZero() {
		t.Errorf("reverse replay produced working=%v blocked=%v; the bug this "+
			"pins is that it reports working with no park — if this changed, the "+
			"daemon's replay order may no longer be load-bearing",
			got.working, !got.blockedSince.IsZero())
	}
}
