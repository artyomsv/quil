package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// callUpdatePaneMarked drives handleUpdatePane with only MarkedForDeletion set,
// mirroring how the TUI's context menu sends the message.
func callUpdatePaneMarked(t *testing.T, d *Daemon, paneID string, marked bool) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID:            paneID,
		MarkedForDeletion: &marked,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdatePane(nil, msg)
}

// TestUpdatePane_SetsAndClearsMarkedForDeletion pins the toggle in both
// directions, for the reason its PinnedAttention twin gives: the payload field
// is a *bool precisely so an explicit false is distinguishable from an absent
// one, and a plain bool would make "Unmark for deletion" unsendable.
func TestUpdatePane_SetsAndClearsMarkedForDeletion(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}})

	callUpdatePaneMarked(t, d, "pane-1", true)
	if got := d.session.Pane("pane-1"); got == nil || !got.MarkedForDeletion {
		t.Fatal("MarkedForDeletion was not set")
	}
	callUpdatePaneMarked(t, d, "pane-1", false)
	if got := d.session.Pane("pane-1"); got == nil || got.MarkedForDeletion {
		t.Fatal("MarkedForDeletion was not cleared — an explicit false must reach the pane")
	}
}

// TestUpdatePane_PartialUpdateLeavesTheDeletionMarkAlone is what the *bool
// tri-state buys. handleUpdatePane is a PARTIAL update handler: the TUI sends
// it for a rename and, far more often, for every OSC 7 CWD change the shell
// emits. The deletion mark is set on a pane whose SHELL IS STILL RUNNING — that
// is the whole use case, a deployment finishing in the background — so the CWD
// updates keep arriving after the mark is set, and a plain bool would unmark
// the pane on the next `cd`.
func TestUpdatePane_PartialUpdateLeavesTheDeletionMarkAlone(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{
		{ID: "pane-1", TabID: "tab-1", Type: "terminal", MarkedForDeletion: true},
	})

	for _, tt := range []struct {
		name    string
		payload ipc.UpdatePanePayload
	}{
		{"rename", ipc.UpdatePanePayload{PaneID: "pane-1", Name: "renamed"}},
		{"cwd change", ipc.UpdatePanePayload{PaneID: "pane-1", CWD: "/tmp"}},
		{"mute toggle", ipc.UpdatePanePayload{PaneID: "pane-1", Muted: boolPtr(true)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ipc.NewMessage(ipc.MsgUpdatePane, tt.payload)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			d.handleUpdatePane(nil, msg)
			if got := d.session.Pane("pane-1"); got == nil || !got.MarkedForDeletion {
				t.Errorf("a %s cleared MarkedForDeletion", tt.name)
			}
		})
	}
}

// TestUpdatePane_MarkingForDeletionClearsTheAttentionPin pins the exclusion in
// the direction the user reaches for most: a pane marked "needs me" earlier is
// the same pane they later decide is finished.
//
// Enforced HERE rather than in the TUI deliberately. The two marks are opposite
// claims about the same pane — "come back to this" and "nothing left here" —
// and a client-side clear would be one client's opinion: a second TUI attached
// to this daemon, and the snapshot on disk, would both keep the stale pin.
func TestUpdatePane_MarkingForDeletionClearsTheAttentionPin(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{
		{ID: "pane-1", TabID: "tab-1", Type: "terminal", PinnedAttention: true},
	})

	callUpdatePaneMarked(t, d, "pane-1", true)

	got := d.session.Pane("pane-1")
	if got == nil {
		t.Fatal("pane vanished")
	}
	if !got.MarkedForDeletion {
		t.Fatal("MarkedForDeletion was not set")
	}
	if got.PinnedAttention {
		t.Error("the attention pin survived a mark for deletion — the two are mutually exclusive")
	}
}

// TestUpdatePane_PinningAttentionClearsTheDeletionMark is the other direction.
// It matters just as much: a pane the user marked disposable and then found
// more work in must stop advertising itself as safe to close.
func TestUpdatePane_PinningAttentionClearsTheDeletionMark(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{
		{ID: "pane-1", TabID: "tab-1", Type: "terminal", MarkedForDeletion: true},
	})

	callUpdatePanePinned(t, d, "pane-1", true)

	got := d.session.Pane("pane-1")
	if got == nil {
		t.Fatal("pane vanished")
	}
	if !got.PinnedAttention {
		t.Fatal("PinnedAttention was not set")
	}
	if got.MarkedForDeletion {
		t.Error("the deletion mark survived an attention pin — the two are mutually exclusive")
	}
}

// TestUpdatePane_ClearingOneMarkLeavesTheOtherAlone is the guard against
// implementing the exclusion as "any write to either field clears the other".
// Unmarking is not a claim about the opposite mark, and the two marks are
// mutually exclusive anyway — so a clear that also cleared its opposite could
// only ever destroy state the user set, never fix an invalid combination.
func TestUpdatePane_ClearingOneMarkLeavesTheOtherAlone(t *testing.T) {
	t.Run("clearing the deletion mark keeps the pin", func(t *testing.T) {
		d := New(config.Default())
		tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
		d.session.RestoreTab(tab, []*Pane{
			{ID: "pane-1", TabID: "tab-1", Type: "terminal", PinnedAttention: true},
		})

		callUpdatePaneMarked(t, d, "pane-1", false)

		if got := d.session.Pane("pane-1"); got == nil || !got.PinnedAttention {
			t.Error("clearing the deletion mark also cleared the attention pin")
		}
	})
	t.Run("clearing the pin keeps the deletion mark", func(t *testing.T) {
		d := New(config.Default())
		tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
		d.session.RestoreTab(tab, []*Pane{
			{ID: "pane-1", TabID: "tab-1", Type: "terminal", MarkedForDeletion: true},
		})

		callUpdatePanePinned(t, d, "pane-1", false)

		if got := d.session.Pane("pane-1"); got == nil || !got.MarkedForDeletion {
			t.Error("clearing the attention pin also cleared the deletion mark")
		}
	})
}

// TestSnapshot_MarkedForDeletionSurvivesTheRoundTrip is the headline
// requirement, and it is a stronger one than the pin's. The mark exists to be
// read on a LATER day: the user leaves a pane alive so a deployment can finish,
// marks it, and comes back to a workspace that has been restarted since. A mark
// lost at restart returns the user to reading the pane's scrollback to work out
// whether it is safe to close — which is exactly what the mark replaces.
//
// Driven through DISK rather than by inspecting the built state: a key the
// writer emits and the reader ignores is the same bug one step later, and only
// the round trip catches it.
func TestSnapshot_MarkedForDeletionSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	marked, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	plain, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	marked.MarkedForDeletion = true
	d.snapshot()

	d2 := newTestDaemonInDir(t, dir)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	got := d2.session.Pane(marked.ID)
	if got == nil {
		t.Fatal("marked pane did not survive restore")
	}
	if !got.MarkedForDeletion {
		t.Error("MarkedForDeletion did not survive the snapshot round trip")
	}
	// The control arm. The key is written only when true (the omit-if-false
	// idiom every flag in that block uses), so a reader that defaulted to true
	// — or a writer that emitted the key unconditionally — would mark every
	// pane in the workspace as disposable, and the assertion above alone would
	// not notice. That is the worst possible direction for this flag to fail
	// in: it invites the user to close panes that are still working.
	if other := d2.session.Pane(plain.ID); other == nil || other.MarkedForDeletion {
		t.Error("an unmarked pane came back marked for deletion")
	}
}

// TestSnapshot_MarkedForDeletionUsesTheWireKey pins the DAEMON half of the wire
// contract. The round trip above proves this package's writer and reader agree
// with each other — they are the same file — but the same map is also the
// BROADCAST the TUI parses, and a rename here would leave that round trip green
// while the mark silently stopped appearing in the client.
//
// The literal is spelled out rather than shared through a constant, which would
// make both sides agree by construction and test nothing. Its twin is
// TestParseWorkspaceState_ReadsTheDeletionMarkWireKey in internal/tui.
func TestSnapshot_MarkedForDeletionUsesTheWireKey(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.MarkedForDeletion = true

	// includeOverlays=true is the BROADCAST shape — the one the TUI parses.
	// The flag must not gate this key: the mark has to reach both disk and
	// wire, which is why it is written outside that block.
	for _, includeOverlays := range []bool{false, true} {
		activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
		state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, includeOverlays)
		panes, ok := state["panes"].([]map[string]any)
		if !ok {
			t.Fatalf("includeOverlays=%v: panes is %T, want []map[string]any", includeOverlays, state["panes"])
		}
		var found bool
		for _, pd := range panes {
			if pd["id"] != pane.ID {
				continue
			}
			found = true
			if pd["marked_for_deletion"] != true {
				t.Errorf("includeOverlays=%v: pane data = %v, want marked_for_deletion:true — "+
					"the TUI parser looks for exactly that key", includeOverlays, pd)
			}
		}
		if !found {
			t.Errorf("includeOverlays=%v: the marked pane is missing from the state", includeOverlays)
		}
	}
}

// TestWorkspaceState_MarkedForDeletionFlip_NoRace mirrors
// TestWorkspaceState_PinnedAttentionFlip_NoRace for the new field: the
// `marked_for_deletion` read must sit inside the same PluginMu span that
// guards Overlay and the pin, and handleUpdatePane writes it concurrently.
func TestWorkspaceState_MarkedForDeletionFlip_NoRace(t *testing.T) {
	d := New(config.Default())

	tab := &Tab{ID: "tab-delmark", Name: "del", Panes: []string{"pane-cafecafe"}}
	pane := &Pane{ID: "pane-cafecafe", TabID: "tab-delmark", CWD: "/tmp"}
	tabs := []*Tab{tab}
	panesByTab := map[string][]*Pane{tab.ID: {pane}}

	const iters = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iters; i++ {
			pane.PluginMu.Lock()
			pane.MarkedForDeletion = i%2 == 0
			pane.PluginMu.Unlock()
		}
	}()

	for i := 0; i < iters; i++ {
		_ = d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, nil, "", true)
	}
	<-done
}

// TestUpdatePane_BothMarksTrueInOnePayloadIsDeterministic pins the tie-break.
//
// No client sends this: the context menu toggles one mark at a time. It is
// pinned anyway because the handler's own comment PROMISES an outcome ("deletion
// is applied second and therefore wins"), and a promise about an input nobody
// currently sends is exactly the kind that rots — reordering the two blocks
// would silently invert it, and every other test in this file would stay green.
//
// What matters is less WHICH mark wins than that exactly one does: the pair is
// mutually exclusive, so a payload that left both set would persist an invalid
// state to disk and hand every client a pane wearing two contradictory marks.
func TestUpdatePane_BothMarksTrueInOnePayloadIsDeterministic(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}})

	yes := true
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID:            "pane-1",
		PinnedAttention:   &yes,
		MarkedForDeletion: &yes,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdatePane(nil, msg)

	got := d.session.Pane("pane-1")
	if got == nil {
		t.Fatal("pane vanished")
	}
	if got.PinnedAttention && got.MarkedForDeletion {
		t.Fatal("both marks are set — the pair is mutually exclusive, and this state " +
			"would be persisted to disk and broadcast to every client")
	}
	if !got.MarkedForDeletion {
		t.Errorf("deletion did not win the tie-break (pinned=%v, marked=%v) — the handler "+
			"documents that it is applied second and wins", got.PinnedAttention, got.MarkedForDeletion)
	}
}
