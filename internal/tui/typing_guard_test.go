package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// typingGuardModel builds a Model holding ONE project (the interim project
// oneProject seeds) with one tab per entry in tabIDs, each holding a single
// pane named "p"+tabID's own suffix ("t1" -> "p1"). Tab 0 starts active. The
// clock is a fake fixed at t0 so tests can advance it deterministically
// around remoteSwitchGuardWindow.
func typingGuardModel(t *testing.T, t0 time.Time, tabIDs ...string) (*Model, *fakeSender) {
	t.Helper()
	tabs := make([]*TabModel, 0, len(tabIDs))
	for _, id := range tabIDs {
		paneID := "p" + id[1:] // "t1" -> "p1", "t2" -> "p2", ...
		pane := NewPaneModel(paneID, 1024)
		tab := NewTabModel(id, id)
		tab.Root = NewLeaf(pane)
		tab.ActivePane = paneID
		tabs = append(tabs, tab)
	}
	fake := &fakeSender{}
	m := &Model{
		cfg:            config.Default(),
		client:         fake,
		termFocused:    true,
		notifications:  NewNotificationCenter(30, 50),
		inputCh:        make(chan paneInput, inputForwardBuffer),
		tabDragFromIdx: -1,
		now:            func() time.Time { return t0 },
	}
	m.projects = oneProject(tabs...)
	m.setActiveTabIdx(0)
	m.initKeymap()
	return m, fake
}

// typingGuardBroadcast returns a WorkspaceStateMsg naming exactly the tabs
// typingGuardModel built, with activeTab as the daemon's reported active tab
// — the shape a real broadcast carries (mirrors broadcast_echo_test.go's
// echoModel, minus the layout round trip this task's tests don't need).
func typingGuardBroadcast(activeTab string, tabIDs ...string) WorkspaceStateMsg {
	state := WorkspaceStateMsg{ActiveTab: activeTab}
	for _, id := range tabIDs {
		paneID := "p" + id[1:]
		state.Tabs = append(state.Tabs, TabInfo{ID: id, Name: id, Panes: []string{paneID}})
		state.Panes = append(state.Panes, PaneInfo{ID: paneID, TabID: id})
	}
	return state
}

// drainOneInput pops the single entry a test expects to already be queued,
// mirroring input_order_test.go's drainQueued (unavailable here for a count
// of 1 with a custom failure message).
func drainOneInput(t *testing.T, m *Model) paneInput {
	t.Helper()
	select {
	case in := <-m.inputCh:
		return in
	default:
		t.Fatal("nothing enqueued")
		return paneInput{}
	}
}

// TestTypingGuard_KeyWithinWindowGoesToOldPane pins the core of spec §8.1: a
// remote switch (nobody on this client requested it) redirects key-originated
// input to the pane that was active before it, for as long as the guard
// window holds.
func TestTypingGuard_KeyWithinWindowGoesToOldPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	// Another client switches the active tab to t2 — this client never asked
	// for it (m.requestedTab is empty), so this is a REMOTE switch.
	m.applyWorkspaceState(typingGuardBroadcast("t2", "t1", "t2"), "")

	if m.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1 (the pane active before the remote switch)", m.guardPaneID)
	}
	if m.activeTabModel().ID != "t2" {
		t.Fatalf("active tab = %q, want t2 (the daemon's own switch still lands)", m.activeTabModel().ID)
	}

	// Within the window: a keystroke goes to p1, not the now-active p2.
	m.now = func() time.Time { return t0.Add(100 * time.Millisecond) }
	if cmd := m.forwardInputBytes([]byte("x")); cmd != nil {
		t.Fatalf("forwardInputBytes returned a non-nil cmd; keystrokes must enqueue synchronously")
	}
	in := drainOneInput(t, m)
	if in.paneID != "p1" {
		t.Errorf("queued paneID = %q, want p1", in.paneID)
	}
}

// TestTypingGuard_AfterWindowGoesToNewPane is the other side of the window:
// once remoteSwitchGuardWindow has elapsed, a keystroke goes to whatever pane
// is active now, not the guarded one. This is also the "window of 0" mutation
// check: mutating remoteSwitchGuardWindow to 0 makes THIS test's "within the
// window" sibling fail instead, by expiring the guard immediately.
func TestTypingGuard_AfterWindowGoesToNewPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	m.applyWorkspaceState(typingGuardBroadcast("t2", "t1", "t2"), "")
	if m.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1", m.guardPaneID)
	}

	// Past the window: the keystroke goes to p2, the pane the remote switch
	// actually made active.
	m.now = func() time.Time { return t0.Add(300 * time.Millisecond) }
	if cmd := m.forwardInputBytes([]byte("x")); cmd != nil {
		t.Fatalf("forwardInputBytes returned a non-nil cmd")
	}
	in := drainOneInput(t, m)
	if in.paneID != "p2" {
		t.Errorf("queued paneID = %q, want p2 (guard window elapsed)", in.paneID)
	}
}

// TestTypingGuard_LocalSwitchSetsNoGuard: this client's OWN switchTab, echoed
// back by the daemon unchanged, must never arm the guard — every attached
// client sees its own switch land as a broadcast, and treating that as
// "someone else switched" would guard every ordinary tab change.
func TestTypingGuard_LocalSwitchSetsNoGuard(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	m.switchTab(1) // requests t2

	m.applyWorkspaceState(typingGuardBroadcast("t2", "t1", "t2"), "")

	if m.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — a local switch's own echo must not arm the guard", m.guardPaneID)
	}
	if m.remoteFocusUnacked {
		t.Errorf("remoteFocusUnacked = true, want false")
	}
}

// TestTypingGuard_LocalThenDifferentRemoteIsGuarded pins the requestedTab
// TOKEN compare (spec §8.1): a local switch (to t2) followed quickly by a
// DIFFERENT client's switch (to t3, not t2) must still be guarded — a time
// window alone could not tell that apart from the local switch's own
// (slightly late) echo. Removing the token compare — always treating this as
// the local switch landing — is the mutation this test exists to catch.
func TestTypingGuard_LocalThenDifferentRemoteIsGuarded(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2", "t3")

	m.switchTab(1) // requests t2; this client's active tab is now t2 (pane p2)

	// A DIFFERENT client's switch lands first, to t3 — not what this client
	// asked for.
	m.applyWorkspaceState(typingGuardBroadcast("t3", "t1", "t2", "t3"), "")

	if m.guardPaneID != "p2" {
		t.Fatalf("guardPaneID = %q, want p2 (this client's own tab when the remote switch arrived)", m.guardPaneID)
	}
	if !m.remoteFocusUnacked {
		t.Error("remoteFocusUnacked = false, want true")
	}
	if m.activeTabModel().ID != "t3" {
		t.Fatalf("active tab = %q, want t3 — the daemon's switch still lands, only typing is redirected", m.activeTabModel().ID)
	}

	// The originally-requested "t2" then lands late (a delayed confirmation
	// arriving after the intervening remote switch). requestedTab still holds
	// "t2" — untouched by the remote switch above, which did not match it —
	// so THIS broadcast must be recognised as the local request finally
	// landing, and must NOT re-arm the guard against t3's own pane. This is
	// the token compare's MATCH branch: mutating it away (always treating a
	// move as remote) would re-arm here and change guardPaneID to p3.
	m.applyWorkspaceState(typingGuardBroadcast("t2", "t1", "t2", "t3"), "")
	if m.guardPaneID != "p2" {
		t.Errorf("guardPaneID = %q after the delayed local confirmation, want it unchanged at p2 — "+
			"the token match must be recognised as this client's own request, not a second remote switch", m.guardPaneID)
	}
}

// TestTypingGuard_RemoteSwitchDoesNotAckUnseenUntilInput pins the other half
// of spec §8.1: a pane focused only because of a remote switch keeps its
// unseen mark through messages that are not local input, and only a key or a
// mouse click acknowledges it. Removing the remoteFocusUnacked check in
// ackFocusedPane is the mutation this test exists to catch.
func TestTypingGuard_RemoteSwitchDoesNotAckUnseenUntilInput(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, fake := typingGuardModel(t, t0, "t1", "t2")

	m.applyWorkspaceState(typingGuardBroadcast("t2", "t1", "t2"), "")
	if !m.remoteFocusUnacked {
		t.Fatal("remoteFocusUnacked should be set by the remote switch")
	}

	// Set AFTER the broadcast: syncPaneMeta seeds a pane's unseen mark from
	// the (unset, so false) daemon copy exactly once per PaneModel, and that
	// first sync already happened inside applyWorkspaceState above — setting
	// it before would just be overwritten back to false.
	p2, _, _ := m.findPaneAndTab("p2")
	p2.unseen = true

	// A message that is not local input — the shared spinner tick — must not
	// ack the mark, however many of them arrive.
	updated, _ := m.Update(workSpinnerTickMsg{})
	got := updated.(Model)
	p2after, _, _ := got.findPaneAndTab("p2")
	if p2after == nil || !p2after.unseen {
		t.Fatal("unseen was acknowledged by a non-input message; it must survive until local input arrives")
	}
	if !got.remoteFocusUnacked {
		t.Fatal("remoteFocusUnacked was cleared by a non-input message")
	}

	// A real keystroke both clears the hold and acknowledges the pane, in the
	// same Update call.
	updated, _ = got.Update(tea.KeyPressMsg{Text: "x"})
	got = updated.(Model)
	p2final, _, _ := got.findPaneAndTab("p2")
	if p2final == nil || p2final.unseen {
		t.Error("unseen should be cleared once local input arrives")
	}
	if got.remoteFocusUnacked {
		t.Error("remoteFocusUnacked should be cleared by the keystroke")
	}
	_ = fake
}

// TestEventDismissed_RemovesCard pins the client-side half of spec §8.4's
// dismissal broadcast: a named id removes that one card, and an empty id
// clears the whole sidebar list — mirroring the daemon's own DismissEventPayload
// "" = all convention.
func TestEventDismissed_RemovesCard(t *testing.T) {
	t.Parallel()
	m := Model{
		cfg:           config.Default(),
		client:        &fakeSender{},
		notifications: NewNotificationCenter(30, 50),
	}
	m.notifications.AddEvent(ipc.PaneEventPayload{ID: "evt-1", Type: "bell", Title: "one"})
	m.notifications.AddEvent(ipc.PaneEventPayload{ID: "evt-2", Type: "bell", Title: "two"})

	updated, _ := m.Update(eventDismissedMsg{eventID: "evt-1"})
	got := updated.(Model)
	if n := got.notifications.Count(); n != 1 {
		t.Fatalf("count after dismissing evt-1 = %d, want 1", n)
	}
	if got.notifications.events[0].ID != "evt-2" {
		t.Errorf("remaining event id = %q, want evt-2", got.notifications.events[0].ID)
	}

	updated, _ = got.Update(eventDismissedMsg{eventID: ""})
	got = updated.(Model)
	if n := got.notifications.Count(); n != 0 {
		t.Errorf("count after dismissing all = %d, want 0", n)
	}
}

// TestPaneSeen_ClearsMarkWithoutEcho pins spec §8.4's unseen-clear broadcast:
// this client clears its own local mark and sends nothing back — echoing
// would have every attached client answer each other's clears forever.
func TestPaneSeen_ClearsMarkWithoutEcho(t *testing.T) {
	t.Parallel()
	pane := NewPaneModel("p1", 1024)
	pane.unseen = true
	tab := NewTabModel("t1", "One")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "p1"
	fake := &fakeSender{}
	m := Model{
		cfg:      config.Default(),
		client:   fake,
		projects: oneProject(tab),
	}

	updated, _ := m.Update(paneSeenMsg{paneID: "p1"})
	got := updated.(Model)

	target, _, _ := got.findPaneAndTab("p1")
	if target == nil {
		t.Fatal("pane p1 vanished")
	}
	if target.unseen {
		t.Error("unseen should be cleared")
	}
	if len(fake.sent) != 0 {
		t.Errorf("pane_seen must not echo anything back to the daemon; got %d sends", len(fake.sent))
	}
}
