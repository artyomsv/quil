package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// unseenUpdatesSent returns the Unseen values of every pane-update message the
// fake sender saw for paneID, in send order. Nil entries (a pane update that
// carried no Unseen field) are skipped.
func unseenUpdatesSent(t *testing.T, fake *fakeSender, paneID string) []bool {
	t.Helper()
	var out []bool
	for _, msg := range fake.sent {
		if msg.Type != ipc.MsgUpdatePane {
			continue
		}
		var payload ipc.UpdatePanePayload
		if err := msg.DecodePayload(&payload); err != nil {
			t.Fatalf("DecodePayload: %v", err)
		}
		if payload.PaneID != paneID || payload.Unseen == nil {
			continue
		}
		out = append(out, *payload.Unseen)
	}
	return out
}

// The client half of the wire contract, spelled out as a literal for the same
// reason the pin's twin does it: a shared constant would make both sides agree
// by construction and stop testing anything.
func TestParseWorkspaceState_ReadsTheUnseenWireKey(t *testing.T) {
	t.Parallel()
	got := parseWorkspaceState(map[string]any{
		"panes": []any{
			map[string]any{"id": "p1", "tab_id": "t1", "unseen": true},
			map[string]any{"id": "p2", "tab_id": "t1"},
		},
	})
	if len(got.Panes) != 2 {
		t.Fatalf("parsed %d panes, want 2", len(got.Panes))
	}
	if !got.Panes[0].Unseen {
		t.Error(`the "unseen" key did not reach PaneInfo — the daemon writes it under exactly that name`)
	}
	if got.Panes[1].Unseen {
		t.Error("a pane with no unseen key parsed as marked")
	}
}

// The daemon's copy seeds a pane the client is seeing for the FIRST time —
// that is what brings the mark back after a TUI restart — and is then left
// alone. The client owns the live value: it clears the mark the moment the
// user focuses the pane and sets it on completions it derives itself, and an
// unconditional copy would revert both on the next broadcast, which the git
// ticker alone delivers every 5 s.
func TestSyncPaneMeta_SeedsUnseenOnceThenLeavesItToTheClient(t *testing.T) {
	t.Parallel()
	pane := NewPaneModel("p1", 1024)
	syncPaneMeta(pane, &PaneInfo{ID: "p1", Unseen: true}, false, 0, false)
	if !pane.unseen {
		t.Fatal("the first sync must seed the mark from the daemon")
	}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", Unseen: false}, false, 0, false)
	if !pane.unseen {
		t.Error("a later broadcast must not overwrite the client's live mark")
	}

	fresh := NewPaneModel("p2", 1024)
	syncPaneMeta(fresh, &PaneInfo{ID: "p2"}, false, 0, false)
	if fresh.unseen {
		t.Error("a pane the daemon holds no mark for must seed unmarked")
	}
	fresh.unseen = true
	syncPaneMeta(fresh, &PaneInfo{ID: "p2"}, false, 0, false)
	if !fresh.unseen {
		t.Error("a later broadcast must not clear a mark the client set")
	}
}

// Every set reaches the daemon, or the next TUI start forgets it.
func TestApplyWorkTransition_CompletionReportsTheMarkToTheDaemon(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	fake := m.client.(*fakeSender)

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.Stop", nil)

	pane := m.curTabs()[1].Root.Leaves()[0]
	if !pane.unseen {
		t.Fatal("setup: a completion on a background tab must mark the pane")
	}
	if got := unseenUpdatesSent(t, fake, "p2"); len(got) != 1 || !got[0] {
		t.Errorf("unseen updates sent for p2 = %v, want exactly one [true]", got)
	}
}

// Every clear reaches the daemon too — including the one a typed prompt
// implies — or a restart resurrects a mark the user already dealt with.
func TestApplyWorkTransition_HumanStartReportsTheClearToTheDaemon(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	fake := m.client.(*fakeSender)
	pane := m.curTabs()[1].Root.Leaves()[0]
	pane.unseen = true

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)

	if pane.unseen {
		t.Fatal("setup: a typed prompt clears the mark")
	}
	if got := unseenUpdatesSent(t, fake, "p2"); len(got) != 1 || got[0] {
		t.Errorf("unseen updates sent for p2 = %v, want exactly one [false]", got)
	}
}

// A transition that leaves the mark as it was sends nothing: the report is
// keyed to the mark CHANGING, not to the event, or the replay at attach would
// send one message per replayed edge.
func TestApplyWorkTransition_UnchangedMarkSendsNothing(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	fake := m.client.(*fakeSender)

	m.applyWorkTransition("p2", "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition("p2", "hook.claude.PreToolUse", nil)
	m.applyWorkTransition("p2", "hook.claude.SubagentStart", map[string]string{"agent_type": "qa"})

	if got := unseenUpdatesSent(t, fake, "p2"); len(got) != 0 {
		t.Errorf("unseen updates sent for p2 = %v, want none — the mark never changed", got)
	}
}

// Focus is the acknowledgement, and the daemon must hear it: the pane the
// user is looking at must not come back green after a restart.
func TestAckFocusedPane_ReportsTheClearToTheDaemon(t *testing.T) {
	t.Parallel()
	m := modelWithSplitActiveTab()
	fake := m.client.(*fakeSender)
	focused := m.curTabs()[0].Root.Left.Pane
	sibling := m.curTabs()[0].Root.Right.Pane
	focused.unseen = true
	sibling.unseen = true

	m.ackFocusedPane()

	if got := unseenUpdatesSent(t, fake, "p1"); len(got) != 1 || got[0] {
		t.Errorf("unseen updates sent for the focused pane = %v, want exactly one [false]", got)
	}
	if got := unseenUpdatesSent(t, fake, "p1b"); len(got) != 0 {
		t.Errorf("unseen updates sent for the unfocused sibling = %v, want none", got)
	}
	// ackFocusedPane runs at the top of EVERY Update; once cleared it must
	// stay quiet, not report the same clear per message.
	m.ackFocusedPane()
	if got := unseenUpdatesSent(t, fake, "p1"); len(got) != 1 {
		t.Errorf("a second ack sent again: %v — the clear is reported once", got)
	}
}

// A report made while the link was down is dropped (the router answers nil for
// a dest it has no conn for; a dead conn errors) and a daemon that restarted
// restored a snapshot up to one debounce older than the last report. Nothing
// re-derives a REPORT: the replay re-derives the local value, and a value that
// is already right sends nothing. So reattach restates every seeded mark for
// that daemon — the live value is the truth by design, so saying it again
// cannot be wrong — or a mark the user cleared during the outage comes back
// green on the next TUI start.
func TestFinishReconnect_RestatesUnseenMarks(t *testing.T) {
	t.Parallel()
	m := modelWithBackgroundTab()
	m.links = map[string]*reconnectState{"": {active: true, attempt: 1}}
	focused := m.curTabs()[0].Root.Leaves()[0]
	background := m.curTabs()[1].Root.Leaves()[0]
	focused.unseen, focused.unseenSeeded = false, true
	background.unseen, background.unseenSeeded = true, true

	fresh := &fakeSender{}
	m.finishReconnect("", fresh)

	if got := unseenUpdatesSent(t, fresh, focused.ID); len(got) != 1 || got[0] {
		t.Errorf("reattach restated %v for the cleared pane, want [false]", got)
	}
	if got := unseenUpdatesSent(t, fresh, background.ID); len(got) != 1 || !got[0] {
		t.Errorf("reattach restated %v for the marked pane, want [true]", got)
	}
}

// A seeded mark must not toast. The on-blur sweep is state-based — every
// unwatched pane with the mark gets a Done toast — and before the daemon copy
// existed a restart cleared every mark, so nothing toasted at start. With the
// seed, every green tab the daemon remembers would toast on the first message
// of every TUI start (auto-update restarts the TUI routinely), with the per-pane
// cooldown unable to help because lastToastAt is zero on a fresh PaneModel. The
// previous process already toasted for that completion; a mark derived HERE
// still does.
func TestRaiseDeferredToasts_SkipsASeededMark(t *testing.T) {
	m, f, pane := toastModel(t)
	syncPaneMeta(pane, &PaneInfo{ID: pane.ID, Unseen: true}, false, 0, false)
	if !pane.unseen {
		t.Fatal("setup: the seed must mark the pane")
	}

	m.raiseDeferredToasts()
	if len(f.sent) != 0 {
		t.Fatalf("sent %d toasts for a seeded mark, want 0 — the previous process already did", len(f.sent))
	}

	// A completion derived by THIS process is a fresh event and toasts as before.
	m.applyWorkTransition(pane.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(pane.ID, "hook.claude.Stop", nil)
	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts after a real completion, want 1", len(f.sent))
	}
}

// Clear attention drops the mark locally, and the daemon must hear that too.
func TestCtxMenu_ExecuteClearAttention_ReportsTheUnseenClear(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := newSplitDragTestModel(t)
	m.client = fake
	target := m.curTabs()[0].Root.Right.Pane
	target.unseen = true
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, cmd := got.executeCtxMenuItem(ctxMenuItem{id: ctxActClearAttention, label: "Clear attention", enabled: true})
	got = updated.(Model)
	runCmd(cmd)

	if got.curTabs()[0].Root.Right.Pane.unseen {
		t.Error("Clear attention left the unseen mark")
	}
	if sent := unseenUpdatesSent(t, fake, target.ID); len(sent) == 0 || sent[len(sent)-1] {
		t.Errorf("Clear attention did not report the unseen clear; sent = %v", sent)
	}
}
