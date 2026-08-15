package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestUpdate_EscapeInterruptsAWorkingPane covers the third missing turn-ending
// edge, and the only one upstream provides no event for at all.
//
// Measured against Claude Code 2.1.233 by driving a real pane over IPC:
// submitting a prompt spooled UserPromptSubmit, interrupting the streaming
// response with ESC spooled NOTHING — no Stop, no StopFailure, no Notification,
// still nothing 80 s later. So unlike the API-error case there is no hook to
// register: `turnActive` stayed true until SessionEnd. Reported from a live
// workspace where a pane sat marked in-progress for 43 minutes after the user
// stopped it.
//
// Driven through Update rather than by calling the transition directly: the
// whole question is whether an ESC keypress REACHES the work state, and a
// direct-call test would pass against wiring that does not exist.
func TestUpdate_EscapeInterruptsAWorkingPane(t *testing.T) {
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	pane := m.curTabs()[0].Root.Leaves()[0]
	if !pane.working {
		t.Fatal("setup: the pane must be working before ESC can end anything")
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)

	if pane.working {
		t.Error("ESC is Claude's interrupt — the pane must stop reporting work")
	}
	if pane.turnActive {
		t.Error("the interrupted turn must be closed, not merely hidden")
	}
}

// TestUpdate_EscapeLeavesBackgroundSubagentsAlone keeps the interrupt scoped to
// the MAIN turn, which is the only thing ESC ends.
//
// The direction matters more than it looks. Clearing the ledger here would be
// unrecoverable: a subagent's own tool calls are dropped by the agent_id gate,
// so nothing re-lights the spinner for a teammate that is still running, and
// its SubagentStop is the only edge that could. Leaving the ledger alone risks
// the opposite — a spinner outliving teammates the interrupt did kill — which
// is the direction this codebase already accepts for subagentsOverflow, and
// which SessionEnd and process_exit both clear.
func TestUpdate_EscapeLeavesBackgroundSubagentsAlone(t *testing.T) {
	m := modelForWorkTest()
	m.applyWorkTransition("p1", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p1", "hook.claude.SubagentStart", map[string]string{"agent_type": "spec-reviewer"})
	pane := m.curTabs()[0].Root.Leaves()[0]

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)

	if pane.turnActive {
		t.Error("ESC must close the main turn")
	}
	if !pane.working {
		t.Error("a teammate the interrupt did not stop must keep the spinner lit")
	}

	// The drain is still the completion edge.
	m.applyWorkTransition("p1", "hook.claude.SubagentStop", map[string]string{"agent_type": "spec-reviewer"})
	if pane.working {
		t.Error("draining the last subagent after an interrupt must stop the spinner")
	}
}

// TestUpdate_EscapeOnAnIdlePaneChangesNothing pins that the interrupt is a
// no-op when there is no turn to end. ESC has other uses inside Claude —
// dismissing a menu, leaving a mode — and a pane that was never working must
// not acquire a spurious completion mark from one.
func TestUpdate_EscapeOnAnIdlePaneChangesNothing(t *testing.T) {
	m := modelForWorkTest()
	pane := m.curTabs()[0].Root.Leaves()[0]

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)

	if pane.working || pane.turnActive {
		t.Error("ESC must not invent work on an idle pane")
	}
	if pane.unseen {
		t.Error("ESC on an idle pane must not leave a you-missed-something mark")
	}
}
