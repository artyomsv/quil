package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// A subagent whose turn dies on an API error fires StopFailure and NEVER a
// SubagentStop (verified against Claude Code 2.1.260 by forcing a subagent
// onto a model that does not exist: SubagentStart, then StopFailure carrying
// the same agent_id + agent_type, then nothing). The producer marks such a
// failure with data["agent_type"], and that mark must drain the ledger entry
// exactly as the SubagentStop that will never come would have.
//
// This is the 2026-09-04 stuck spinner: two production panes each held one
// SubagentStart with no stop, both QA agents killed at 11:16 on 2026-09-02 when
// the usage limit hit. Nothing short of SessionEnd or a pane restart could
// clear them, because the only edge Claude sent was read as the MAIN turn
// ending.
func TestApplyWorkTransition_SubagentStopFailure_DrainsOnlyThatAgent(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "qa-t0"})
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "sec-t0"})

	m.applyWorkTransition("p2", "hook.claude.StopFailure", map[string]string{"agent_type": "qa-t0", "error": "usage_limit"})

	if _, live := pane.subagents["qa-t0"]; live {
		t.Error("a subagent's StopFailure must drain that agent from the ledger")
	}
	if _, live := pane.subagents["sec-t0"]; !live {
		t.Error("a subagent's StopFailure must not touch a different agent")
	}
	if !pane.turnActive {
		t.Error("a subagent's failure says nothing about the MAIN turn, which is still running")
	}
	if !pane.working {
		t.Error("spinner must stay lit: the main turn and another agent are still running")
	}
}

// The production shape: the main turn already ended (Stop), the ledger holds
// the agent, and the agent then dies. Its StopFailure is the completion edge,
// so the spinner goes off and the background pane gets its mark.
func TestApplyWorkTransition_SubagentStopFailure_IsTheCompletionEdgeAfterStop(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "review-qa-2"})
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)
	if !pane.working {
		t.Fatal("setup: the outstanding agent must keep the spinner lit past the main Stop")
	}

	m.applyWorkTransition("p2", "hook.claude.StopFailure", map[string]string{"agent_type": "review-qa-2"})

	if pane.working {
		t.Error("the failed agent was the last outstanding work; the spinner must go off")
	}
	if !pane.unseen {
		t.Error("the last outstanding agent dying is a completion the user has not seen")
	}
}

// A StopFailure that names NO agent is the main turn's own failure and keeps
// its existing meaning: the turn ends, the ledger is untouched.
func TestApplyWorkTransition_MainStopFailure_StillLeavesTheLedgerAlone(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "qa"})
	m.applyWorkTransition("p2", "hook.claude.StopFailure", map[string]string{"error": "rate_limit"})

	if pane.turnActive {
		t.Error("the main turn's StopFailure must end the turn")
	}
	if _, live := pane.subagents["qa"]; !live {
		t.Error("the main turn's StopFailure must not drain a running subagent")
	}
	if !pane.working {
		t.Error("the running subagent must keep the spinner lit")
	}
}

// hook.claude.Stop shares WorkEventStop with StopFailure, and so do the two
// opencode stops and the synthetic interrupt. Only StopFailure is ever a
// subagent's ending. A Stop that happens to carry agent_type — no producer
// sends one today, but a --agent session carries the field on every event —
// must still close the main turn, or turnActive never clears and the pane is
// lit for good through a sibling event type. The gate is the event TYPE, not
// the classified kind.
func TestApplyWorkTransition_PlainStopWithAgentType_StillEndsTheTurn(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "qa"})
	m.applyWorkTransition("p2", "hook.claude.Stop", map[string]string{"agent_type": "qa"})

	if pane.turnActive {
		t.Error("a plain Stop must end the main turn whatever data it carries")
	}
	if _, live := pane.subagents["qa"]; !live {
		t.Error("a plain Stop must not drain a subagent; only StopFailure stands in for SubagentStop")
	}
}

// A subagent failure naming an agent the ledger does not hold — a start lost
// to ring eviction, or a replay truncated past it — has nothing to drain. It
// must not end the main turn either: it says nothing about it. This pins a
// deliberate change; before the fix every StopFailure cleared turnActive.
func TestApplyWorkTransition_OrphanSubagentStopFailure_LeavesTheMainTurnAlone(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	pane := m.curTabs()[1].Root.Leaves()[0]

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.StopFailure", map[string]string{"agent_type": "ghost", "error": "usage_limit"})

	if !pane.turnActive {
		t.Error("an orphan subagent failure must not close the main turn")
	}
	if len(pane.subagents) != 0 {
		t.Error("nothing to drain, nothing drained")
	}
	if !pane.working {
		t.Error("the main turn is still running; the spinner must stay lit")
	}
}

// Through Update, not the decision function: this is the path a live
// paneEventMsg takes, and it is where the spinner tick loop is told to stop.
func TestUpdate_PaneEvent_SubagentStopFailure_StopsTheSpinner(t *testing.T) {
	t.Parallel()
	m := modelForWorkTest()
	feed := func(m Model, typ string, data map[string]string) Model {
		next, _ := m.Update(paneEventMsg(ipc.PaneEventPayload{
			ID:     "e-" + typ,
			PaneID: "p1",
			Type:   typ,
			Title:  typ,
			Data:   data,
		}))
		return next.(Model)
	}
	m = feed(m, "hook.claude.UserPromptSubmit", nil)
	m = feed(m, "hook.claude.SubagentStart", map[string]string{"agent_type": "qa-t0"})
	m = feed(m, "hook.claude.Stop", nil)
	if !m.anyPaneWorking() {
		t.Fatal("setup: the outstanding agent must keep the pane working past Stop")
	}

	m = feed(m, "hook.claude.StopFailure", map[string]string{"agent_type": "qa-t0", "error": "usage_limit"})

	if m.anyPaneWorking() {
		t.Error("after the only outstanding agent fails, no pane may report work")
	}
}
