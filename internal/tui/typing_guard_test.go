package tui

import (
	"strings"
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
// around remoteSwitchGuardWindow. Sized and resized like newSplitDragTestModel
// so mouse-driven tests (clicks) have real geometry to hit-test against.
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
		width:          100,
		height:         40,
	}
	m.projects = oneProject(tabs...)
	m.setActiveTabIdx(0)
	m.initKeymap()
	m.resizeTabs()
	return m, fake
}

// typingGuardBroadcast returns a WorkspaceStateMsg naming exactly the tabs
// listed, with activeTab as the daemon's reported active tab — the shape a
// real broadcast carries (mirrors broadcast_echo_test.go's echoModel, minus
// the layout round trip this task's tests don't need). tabIDs need not match
// what typingGuardModel built: a shorter list simulates tabs this client no
// longer holds (destroyed, moved), and an id absent from BOTH the model and
// every prior broadcast simulates a brand new tab.
func typingGuardBroadcast(activeTab string, tabIDs ...string) WorkspaceStateMsg {
	state := WorkspaceStateMsg{ActiveTab: activeTab}
	for _, id := range tabIDs {
		paneID := "p" + id[1:]
		state.Tabs = append(state.Tabs, TabInfo{ID: id, Name: id, Panes: []string{paneID}})
		state.Panes = append(state.Panes, PaneInfo{ID: paneID, TabID: id})
	}
	return state
}

// altKey builds the Alt+<digit> keypress that drives tab.switch_N — the real
// local-switch entry point (Update -> handleKey -> switchTab), matching how
// tabbar_scroll_test.go drives the same action.
func altKey(digit rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: digit, Mod: tea.ModAlt}
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
// window holds. Driven entirely through Update: the broadcast arrives as a
// WorkspaceStateMsg and the keystroke as a KeyPressMsg, exactly as production
// delivers both.
func TestTypingGuard_KeyWithinWindowGoesToOldPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	// Another client switches the active tab to t2 — this client never asked
	// for it (m.requestedTab is empty), so this is a REMOTE switch.
	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)

	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1 (the pane active before the remote switch)", got.guardPaneID)
	}
	if got.activeTabModel().ID != "t2" {
		t.Fatalf("active tab = %q, want t2 (the daemon's own switch still lands)", got.activeTabModel().ID)
	}

	// Within the window: a keystroke goes to p1, not the now-active p2.
	got.now = func() time.Time { return t0.Add(100 * time.Millisecond) }
	updated, _ = got.Update(tea.KeyPressMsg{Text: "x"})
	got2 := updated.(Model)

	in := drainOneInput(t, &got2)
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

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1", got.guardPaneID)
	}

	// Past the window: the keystroke goes to p2, the pane the remote switch
	// actually made active.
	got.now = func() time.Time { return t0.Add(300 * time.Millisecond) }
	updated, _ = got.Update(tea.KeyPressMsg{Text: "x"})
	got2 := updated.(Model)

	in := drainOneInput(t, &got2)
	if in.paneID != "p2" {
		t.Errorf("queued paneID = %q, want p2 (guard window elapsed)", in.paneID)
	}
}

// TestTypingGuard_LocalSwitchSetsNoGuard: this client's OWN switchTab (driven
// via the real Alt+2 keypress), echoed back by the daemon unchanged, must
// never arm the guard — every attached client sees its own switch land as a
// broadcast, and treating that as "someone else switched" would guard every
// ordinary tab change.
func TestTypingGuard_LocalSwitchSetsNoGuard(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(altKey('2')) // requests t2 (tab.switch_2)
	got := updated.(Model)

	updated, _ = got.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got2 := updated.(Model)

	if got2.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — a local switch's own echo must not arm the guard", got2.guardPaneID)
	}
	if got2.remoteFocusUnacked {
		t.Errorf("remoteFocusUnacked = true, want false")
	}
}

// TestTypingGuard_LocalThenDifferentRemoteIsGuarded pins the requestedTab
// TOKEN compare (spec §8.1): a local switch (to t2) followed quickly by a
// DIFFERENT client's switch (to t3, not t2) must still be guarded — a time
// window alone could not tell that apart from the local switch's own
// (slightly late) echo.
func TestTypingGuard_LocalThenDifferentRemoteIsGuarded(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2", "t3")

	updated, _ := m.Update(altKey('2')) // requests t2; active tab is now t2 (pane p2)
	got := updated.(Model)

	// A DIFFERENT client's switch lands first, to t3 — not what this client
	// asked for.
	updated, _ = got.Update(typingGuardBroadcast("t3", "t1", "t2", "t3"))
	got2 := updated.(Model)

	if got2.guardPaneID != "p2" {
		t.Fatalf("guardPaneID = %q, want p2 (this client's own tab when the remote switch arrived)", got2.guardPaneID)
	}
	if !got2.remoteFocusUnacked {
		t.Error("remoteFocusUnacked = false, want true")
	}
	if got2.activeTabModel().ID != "t3" {
		t.Fatalf("active tab = %q, want t3 — the daemon's switch still lands, only typing is redirected", got2.activeTabModel().ID)
	}

	// The originally-requested "t2" then lands late (a delayed confirmation
	// arriving after the intervening remote switch). requestedTab still holds
	// "t2" — untouched by the remote switch above, which did not match it —
	// so THIS broadcast must be recognised as the local request finally
	// landing, and must NOT re-arm the guard against t3's own pane.
	updated, _ = got2.Update(typingGuardBroadcast("t2", "t1", "t2", "t3"))
	got3 := updated.(Model)
	if got3.guardPaneID != "p2" {
		t.Errorf("guardPaneID = %q after the delayed local confirmation, want it unchanged at p2 — "+
			"the token match must be recognised as this client's own request, not a second remote switch", got3.guardPaneID)
	}
}

// TestTypingGuard_StaleTokenDoesNotSuppressALaterRemoteSwitch pins the fix for
// review round 1's Important 1: requestedTab must be retired on an ORDINARY
// echo (fromTab == targetTab, no "move"), not only inside the moved branch —
// switchTab updates this client's own active-tab index synchronously, so the
// confirming broadcast for a local switch is never itself a "move". Leaving
// the token in place after that echo lets it wrongly satisfy a LATER,
// unrelated broadcast that happens to name the same tab id.
func TestTypingGuard_StaleTokenDoesNotSuppressALaterRemoteSwitch(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(altKey('2')) // requests t2
	got := updated.(Model)

	// The ordinary echo: fromTab == targetTab == t2 already (switchTab moved
	// it synchronously), so this is not a "move" — the token must still be
	// retired here.
	updated, _ = got.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got2 := updated.(Model)
	if got2.guardPaneID != "" {
		t.Fatalf("guardPaneID = %q after the echo, want empty", got2.guardPaneID)
	}

	// A different client switches to t1 — a genuine remote switch.
	updated, _ = got2.Update(typingGuardBroadcast("t1", "t1", "t2"))
	got3 := updated.(Model)
	if got3.guardPaneID != "p2" {
		t.Fatalf("guardPaneID = %q after switching to t1, want p2", got3.guardPaneID)
	}

	// A different client switches BACK to t2 — a SECOND, unrelated remote
	// change. If the stale "t2" token from the very first local switch had
	// survived the echo above, it would wrongly match here and suppress this
	// genuine remote switch.
	updated, _ = got3.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got4 := updated.(Model)
	if got4.guardPaneID != "p1" {
		t.Errorf("guardPaneID = %q after the second remote switch back to t2, want p1 "+
			"(armed by a genuine remote switch, not suppressed by a stale token)", got4.guardPaneID)
	}
}

// TestTypingGuard_LocalTabDestroyIsNotARemoteSwitch pins the fix for review
// round 1's Important 2: closing, moving, or dissolving the ACTIVE tab is
// this client's OWN action taking the tab away from under itself, not a
// switch "by another client" — the false positive read as a bogus flash and
// an unseen-ack hold on the most common tab actions there are.
func TestTypingGuard_LocalTabDestroyIsNotARemoteSwitch(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	// This client destroyed t1 (Ctrl+W): the very next broadcast no longer
	// mentions it at all, and its neighbour t2 becomes active.
	updated, _ := m.Update(typingGuardBroadcast("t2", "t2"))
	got := updated.(Model)

	if got.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — destroying the active tab must not arm the typing guard", got.guardPaneID)
	}
	if got.remoteFocusUnacked {
		t.Error("remoteFocusUnacked = true, want false")
	}
}

// TestTypingGuard_RemoteSwitchDoesNotAckUnseenUntilInput pins the other half
// of spec §8.1: a pane focused only because of a remote switch keeps its
// unseen mark through messages that are not local input, and only a key or a
// mouse click acknowledges it — and while unacked, nothing is reported back
// to the daemon (no pane_seen echo, no MsgUpdatePane{Unseen}).
func TestTypingGuard_RemoteSwitchDoesNotAckUnseenUntilInput(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, fake := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if !got.remoteFocusUnacked {
		t.Fatal("remoteFocusUnacked should be set by the remote switch")
	}

	// Set AFTER the broadcast: syncPaneMeta seeds a pane's unseen mark from
	// the (unset, so false) daemon copy exactly once per PaneModel, and that
	// first sync already happened inside applyWorkspaceState above — setting
	// it before would just be overwritten back to false.
	p2, _, _ := got.findPaneAndTab("p2")
	p2.unseen = true

	// A message that is not local input — the shared spinner tick — must not
	// ack the mark, however many of them arrive, and must not report anything.
	updated, _ = got.Update(workSpinnerTickMsg{})
	got2 := updated.(Model)
	p2after, _, _ := got2.findPaneAndTab("p2")
	if p2after == nil || !p2after.unseen {
		t.Fatal("unseen was acknowledged by a non-input message; it must survive until local input arrives")
	}
	if !got2.remoteFocusUnacked {
		t.Fatal("remoteFocusUnacked was cleared by a non-input message")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("%d message(s) sent to the daemon while unacked, want 0 (no pane_seen echo, no unseen report)", len(fake.sent))
	}

	// A real keystroke both clears the hold and acknowledges the pane, in the
	// same Update call — which reports the clear back (MsgUpdatePane{Unseen:false}).
	updated, _ = got2.Update(tea.KeyPressMsg{Text: "x"})
	got3 := updated.(Model)
	p2final, _, _ := got3.findPaneAndTab("p2")
	if p2final == nil || p2final.unseen {
		t.Error("unseen should be cleared once local input arrives")
	}
	if got3.remoteFocusUnacked {
		t.Error("remoteFocusUnacked should be cleared by the keystroke")
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sends after the acknowledging keystroke = %d, want 1 (the unseen report)", len(fake.sent))
	}
	var payload ipc.UpdatePanePayload
	if err := fake.sent[0].DecodePayload(&payload); err != nil {
		t.Fatalf("decode sent payload: %v", err)
	}
	if payload.PaneID != "p2" || payload.Unseen == nil || *payload.Unseen {
		t.Errorf("sent payload = %+v, want {PaneID: p2, Unseen: false}", payload)
	}
}

// TestTypingGuard_WheelNeverGuarded pins the key-vs-mouse ruling: wheel input
// goes straight to enqueueInput (sendInputToPane), bypassing the typing guard
// entirely, because a remote tab switch says nothing about where the mouse
// pointer is now. Calls sendInputToPane directly, matching this file's own
// precedent for the wheel path (TestSendInputToPane_SharesTheKeystrokeQueue
// in input_order_test.go) rather than driving a full mouse-rect Update.
func TestTypingGuard_WheelNeverGuarded(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1", got.guardPaneID)
	}
	got.now = func() time.Time { return t0.Add(100 * time.Millisecond) } // well within the window

	got.sendInputToPane("p2", []byte("\x1b[<64;1;1M")) // one wheel-up notch

	in := drainOneInput(t, &got)
	if in.paneID != "p2" {
		t.Errorf("wheel notch queued paneID = %q, want p2 (unredirected) — the typing guard must never apply to mouse input", in.paneID)
	}
}

// TestTypingGuard_PasteWithinWindowGoesToOldPane drives the paste path
// through Update (tea.PasteMsg), the second key-originated producer besides
// typed keys (spec §8.1: "paste counts as a key here").
func TestTypingGuard_PasteWithinWindowGoesToOldPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1", got.guardPaneID)
	}
	got.now = func() time.Time { return t0.Add(100 * time.Millisecond) }

	updated, _ = got.Update(tea.PasteMsg{Content: "pasted"})
	got2 := updated.(Model)

	in := drainOneInput(t, &got2)
	if in.paneID != "p1" {
		t.Errorf("pasted into %q, want p1 (the pane active before the remote switch)", in.paneID)
	}
}

// TestTypingGuard_PasteEncodesForTheRedirectedPane pins review round 1's
// Important 3: the paste must be ENCODED against the pane it actually reaches
// (bracketed-paste mode), not the pane that was active when the paste was
// requested. Encoding against the wrong pane's mode and then redirecting the
// bytes sends an unbracketed multi-line paste into a shell character by
// character (executing every line but the last), or a bracketed one into an
// app that never enabled the mode.
func TestTypingGuard_PasteEncodesForTheRedirectedPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	p1, _, _ := m.findPaneAndTab("p1")
	p1.bracketedPasteSeen = true
	p1.bracketedPaste = true // the REDIRECTED pane wants bracketed paste
	// p2 (the pane superficially "active" when the paste is requested) is
	// left at its zero value — bracketed paste NOT enabled.

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1", got.guardPaneID)
	}
	got.now = func() time.Time { return t0.Add(100 * time.Millisecond) }

	updated, _ = got.Update(tea.PasteMsg{Content: "hello\nworld"})
	got2 := updated.(Model)

	in := drainOneInput(t, &got2)
	if in.paneID != "p1" {
		t.Fatalf("pasted into %q, want p1", in.paneID)
	}
	if !strings.HasPrefix(string(in.data), pasteStart) {
		t.Errorf("paste data = %q, want bracketed (p1's own mode) — "+
			"encoding against p2's mode instead sent it unbracketed", string(in.data))
	}
}

// TestTypingGuard_KeySideEffectsActOnTheRedirectedPane pins the rest of
// Important 3: ResetScroll, answerBlockedByInput and interruptWorkingPane
// must act on whichever pane the keystroke actually reaches, not on
// tab.ActivePaneModel() — crediting the wrong pane with input it never
// received (a parked prompt on the redirected pane stays parked; the WRONG
// pane's scrollback resets instead).
func TestTypingGuard_KeySideEffectsActOnTheRedirectedPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	p1, _, _ := m.findPaneAndTab("p1")
	p1.blockedSince = t0.Add(-time.Hour)
	p1.blockedReason = "Bash"
	p1.scrollBack = 42

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1", got.guardPaneID)
	}
	got.now = func() time.Time { return t0.Add(100 * time.Millisecond) }

	updated, _ = got.Update(tea.KeyPressMsg{Text: "x"})
	got2 := updated.(Model)

	p1after, _, _ := got2.findPaneAndTab("p1")
	if !p1after.blockedSince.IsZero() {
		t.Error("p1.blockedSince should be cleared — the keystroke was redirected to it")
	}
	if p1after.blockedReason != "" {
		t.Errorf("p1.blockedReason = %q, want empty", p1after.blockedReason)
	}
	if p1after.scrollBack != 0 {
		t.Errorf("p1.scrollBack = %d, want 0 (ResetScroll should act on the redirected pane)", p1after.scrollBack)
	}
}

// TestTypingGuard_ClickClearsFlag pins the mouse half of "local input answers
// the ack hold" (spec §8.1): a mouse click, not only a keystroke, clears
// remoteFocusUnacked. Driven through Update with a real tea.MouseClickMsg.
func TestTypingGuard_ClickClearsFlag(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if !got.remoteFocusUnacked {
		t.Fatal("remoteFocusUnacked should be set by the remote switch")
	}

	updated, _ = got.Update(tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	got2 := updated.(Model)

	if got2.remoteFocusUnacked {
		t.Error("remoteFocusUnacked should be cleared by a mouse click")
	}
}

// TestTypingGuard_OverlayVisibleGuardsTheOverlayPane pins review round 1's
// Important 4/minor 4: when the FROM tab has a visible overlay (lazygit, say)
// the user was typing into, the guard must protect the OVERLAY pane, not the
// tree pane sitting behind it — ActivePaneModel's own rule for who owns
// keyboard input while an overlay is up.
func TestTypingGuard_OverlayVisibleGuardsTheOverlayPane(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	tab1 := m.curTabs()[0] // t1, the FROM tab
	overlay := NewPaneModel("overlay-1", 1024)
	tab1.overlayPane = overlay
	tab1.overlayVisible = true

	state := WorkspaceStateMsg{
		ActiveTab: "t2",
		Tabs: []TabInfo{
			{ID: "t1", Name: "t1", Panes: []string{"p1", "overlay-1"}},
			{ID: "t2", Name: "t2", Panes: []string{"p2"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "t1"},
			{ID: "overlay-1", TabID: "t1", Overlay: true},
			{ID: "p2", TabID: "t2"},
		},
	}
	updated, _ := m.Update(state)
	got := updated.(Model)

	if got.guardPaneID != "overlay-1" {
		t.Errorf("guardPaneID = %q, want overlay-1 — the user was typing into the overlay, not the tree pane behind it", got.guardPaneID)
	}
}

// TestTypingGuard_CreateTabTokenSpentOnAnExistingTab pins review round 1's
// minor 1: pendingTabCreateToken must match ONLY a genuinely new tab id (one
// absent before this broadcast). A switch to an EXISTING tab beating the
// create's own landing to the wire must be treated as remote, and the
// one-shot token must be spent regardless — checked directly against
// requestedTab, since a leftover token could otherwise wrongly satisfy some
// LATER broadcast.
func TestTypingGuard_CreateTabTokenSpentOnAnExistingTab(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")
	proj := m.cur()
	key := requestedTabKey("", proj.ID)
	m.recordRequestedTab("", proj.ID, pendingTabCreateToken, "")

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)

	if got.guardPaneID != "p1" {
		t.Fatalf("guardPaneID = %q, want p1 — a switch to an EXISTING tab must not be mistaken for the pending create", got.guardPaneID)
	}
	if _, stillSet := got.requestedTab[key]; stillSet {
		t.Error("pendingTabCreateToken survived a broadcast it did not match — " +
			"it must be spent either way, or it could wrongly satisfy a LATER broadcast")
	}
}

// TestTypingGuard_CreateTabTokenMatchesTheNewTab is the token's intended
// match: a genuinely new tab id landing while a create is pending is
// recognised as that create's own landing, and does not arm the guard.
func TestTypingGuard_CreateTabTokenMatchesTheNewTab(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1")
	proj := m.cur()
	m.recordRequestedTab("", proj.ID, pendingTabCreateToken, "")

	// A brand new tab (never seen before) becomes active — this client's own
	// create_tab landing.
	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)

	if got.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — a genuinely new tab must be recognised as the pending create's own landing", got.guardPaneID)
	}
}

// TestTypingGuard_CreateTabTokenSurvivesANoOpBroadcast pins review round 2's
// new Important finding: applyTabMoveGuard now runs on EVERY broadcast for
// the active project (fix round 1's Important 1), including ordinary ones
// that change nothing — the git ticker, an OSC 7 CWD update, another
// client's unrelated action. Before this fix, the pending-create branch
// deleted its token unconditionally and only THEN checked existedBefore, so
// a no-op broadcast (existedBefore trivially true — it's the tab we were
// already on) spent it — and the create's own tab, landing moments later,
// read as a stranger's remote switch: a false flash and 250 ms of redirected
// typing right after Ctrl+T.
func TestTypingGuard_CreateTabTokenSurvivesANoOpBroadcast(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1")
	proj := m.cur()
	m.recordRequestedTab("", proj.ID, pendingTabCreateToken, "")

	// An ordinary broadcast, same active tab, lands before the create's own.
	updated, _ := m.Update(typingGuardBroadcast("t1", "t1"))
	got := updated.(Model)
	if got.guardPaneID != "" {
		t.Fatalf("guardPaneID = %q after a no-op broadcast, want empty", got.guardPaneID)
	}
	key := requestedTabKey("", proj.ID)
	if _, ok := got.requestedTab[key]; !ok {
		t.Fatal("pendingTabCreateToken was spent by a broadcast that changed nothing")
	}

	// The create's own tab lands next — the token must still be there to
	// recognise it.
	updated, _ = got.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got2 := updated.(Model)
	if got2.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — the surviving token must still recognise its own create's landing", got2.guardPaneID)
	}
}

// TestTypingGuard_StaleFromTabBroadcastIsRejectedWhilePending pins review
// round 2's pre-existing issue: a broadcast already in flight when switchTab
// runs still names the tab this client just left. Adopting it — as the
// pre-fix code did, since it had no way to tell "the daemon really switched
// back" from "this is leftover from before my own switch" — visibly jumped
// the tab back to the old one for the width of one round trip before the
// requester's own echo corrected it, and armed a false guard/flash on top.
func TestTypingGuard_StaleFromTabBroadcastIsRejectedWhilePending(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(altKey('2')) // requests t2, leaving t1
	got := updated.(Model)
	if got.activeTabModel().ID != "t2" {
		t.Fatalf("setup: active tab = %q, want t2", got.activeTabModel().ID)
	}

	// A broadcast already in flight before the switch still names t1 — the
	// PRE-switch state, not a real switch back. Well within the stale bound.
	got.now = func() time.Time { return t0.Add(500 * time.Millisecond) }
	updated, _ = got.Update(typingGuardBroadcast("t1", "t1", "t2"))
	got2 := updated.(Model)

	if got2.activeTabModel().ID != "t2" {
		t.Errorf("active tab = %q after the stale t1 broadcast, want t2 (unchanged)", got2.activeTabModel().ID)
	}
	if got2.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — a stale echo of the pre-switch state must not arm the guard", got2.guardPaneID)
	}
	if got2.flashText != "" {
		t.Errorf("flashText = %q, want empty — no false flash for a stale broadcast", got2.flashText)
	}

	// The real confirmation lands next and must still be recognised — the
	// stale broadcast above must not have consumed the token.
	updated, _ = got2.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got3 := updated.(Model)
	if got3.activeTabModel().ID != "t2" {
		t.Fatalf("active tab = %q after the echo, want t2", got3.activeTabModel().ID)
	}
	if got3.guardPaneID != "" {
		t.Errorf("guardPaneID = %q after the echo, want empty", got3.guardPaneID)
	}

	// NOW a genuine remote switch to t1 (the token is spent, so nothing can
	// mistake this for another stale echo) must be honoured and guarded.
	updated, _ = got3.Update(typingGuardBroadcast("t1", "t1", "t2"))
	got4 := updated.(Model)
	if got4.activeTabModel().ID != "t1" {
		t.Fatalf("active tab = %q after the genuine remote switch, want t1", got4.activeTabModel().ID)
	}
	if got4.guardPaneID != "p2" {
		t.Errorf("guardPaneID = %q, want p2 (this client's own tab when the remote switch arrived)", got4.guardPaneID)
	}
}

// TestTypingGuard_JumpRecordsARequestedTab: jumpToPane (MCP set_active_pane,
// sidebar clicks, Alt+Backspace, the palette, attention jumps) moves this
// client's active tab exactly as switchTab does, so it must record the same
// token. Without one, a broadcast still in flight from before the jump names
// the old tab and reads as another client switching back: the tab jumps
// back, the guard arms and the flash shows.
func TestTypingGuard_JumpRecordsARequestedTab(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(setActivePaneMsg{PaneID: "p2"})
	got := updated.(Model)
	if got.activeTabModel().ID != "t2" {
		t.Fatalf("setup: active tab = %q, want t2", got.activeTabModel().ID)
	}

	got.now = func() time.Time { return t0.Add(100 * time.Millisecond) }
	updated, _ = got.Update(typingGuardBroadcast("t1", "t1", "t2"))
	got2 := updated.(Model)
	if got2.activeTabModel().ID != "t2" {
		t.Errorf("active tab = %q after a stale t1 broadcast, want t2 (no jump back)", got2.activeTabModel().ID)
	}
	if got2.guardPaneID != "" {
		t.Errorf("guardPaneID = %q, want empty — the jump was this client's own", got2.guardPaneID)
	}
	if got2.flashText != "" {
		t.Errorf("flashText = %q, want empty", got2.flashText)
	}

	// The jump's own echo lands and retires the token.
	updated, _ = got2.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got3 := updated.(Model)
	if got3.guardPaneID != "" || got3.flashText != "" {
		t.Errorf("the jump's echo armed the guard (%q) or flashed (%q)", got3.guardPaneID, got3.flashText)
	}
}

// TestTypingGuard_StaleFromTabBroadcastIsAdoptedPastTheBound is the other
// side of requestedSwitchStaleWindow: once it elapses, the local switch is
// assumed lost (never reached the daemon, or was overtaken), and a broadcast
// naming the tab this client asked to leave is adopted normally — guard
// included — rather than held forever.
func TestTypingGuard_StaleFromTabBroadcastIsAdoptedPastTheBound(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(altKey('2')) // requests t2, leaving t1
	got := updated.(Model)

	got.now = func() time.Time { return t0.Add(3 * time.Second) } // past requestedSwitchStaleWindow
	updated, _ = got.Update(typingGuardBroadcast("t1", "t1", "t2"))
	got2 := updated.(Model)

	if got2.activeTabModel().ID != "t1" {
		t.Errorf("active tab = %q, want t1 — past the bound the broadcast must be adopted, not held", got2.activeTabModel().ID)
	}
	if got2.guardPaneID != "p2" {
		t.Errorf("guardPaneID = %q, want p2", got2.guardPaneID)
	}
}

// TestArmReattachReset_ClearsTypingGuardStateForThatDest pins review round
// 1's minor 3: a reattach replaces its destination's whole state, so a
// pending requestedTab token can never land the broadcast it was waiting for,
// and a guardPaneID naming one of that destination's panes is redirecting
// input toward a pane about to be rebuilt out from under it.
func TestArmReattachReset_ClearsTypingGuardStateForThatDest(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := typingGuardModel(t, t0, "t1", "t2")

	updated, _ := m.Update(typingGuardBroadcast("t2", "t1", "t2"))
	got := updated.(Model)
	if got.guardPaneID != "p1" {
		t.Fatalf("setup: guardPaneID = %q, want p1", got.guardPaneID)
	}
	got.recordRequestedTab("", "some-other-project", "some-tab", "")

	got.armReattachReset("")

	if got.guardPaneID != "" {
		t.Errorf("guardPaneID = %q after reattach reset, want empty", got.guardPaneID)
	}
	if got.remoteFocusUnacked {
		t.Error("remoteFocusUnacked should be cleared by a reattach of its own destination")
	}
	if _, ok := got.requestedTab[requestedTabKey("", "some-other-project")]; ok {
		t.Error("requestedTab entries for the reattaching dest should be cleared")
	}
}

// TestListenForMessages_DecodesEventDismissedWithOrigin pins the wire decode
// for spec §8.4's dismissal broadcast, including that Origin (the sending
// daemon, stamped by the router) survives into eventDismissedMsg.dest.
func TestListenForMessages_DecodesEventDismissedWithOrigin(t *testing.T) {
	t.Parallel()
	wire, err := ipc.NewMessage(ipc.MsgEventDismissed, ipc.EventDismissedPayload{EventID: "evt-9"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	wire.Origin = "gpu01"
	c := &scriptedConn{msgs: make(chan *ipc.Message, 1)}
	c.msgs <- wire

	m := Model{client: c}
	got := m.listenForMessages()()
	msg, ok := got.(eventDismissedMsg)
	if !ok {
		t.Fatalf("msg is %T, want eventDismissedMsg", got)
	}
	if msg.dest != "gpu01" {
		t.Errorf("dest = %q, want gpu01", msg.dest)
	}
	if msg.eventID != "evt-9" {
		t.Errorf("eventID = %q, want evt-9", msg.eventID)
	}
}

// TestListenForMessages_DecodesPaneSeenWithOrigin is the pane_seen twin.
func TestListenForMessages_DecodesPaneSeenWithOrigin(t *testing.T) {
	t.Parallel()
	wire, err := ipc.NewMessage(ipc.MsgPaneSeen, ipc.PaneSeenPayload{PaneID: "pane-7"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	wire.Origin = "gpu01"
	c := &scriptedConn{msgs: make(chan *ipc.Message, 1)}
	c.msgs <- wire

	m := Model{client: c}
	got := m.listenForMessages()()
	msg, ok := got.(paneSeenMsg)
	if !ok {
		t.Fatalf("msg is %T, want paneSeenMsg", got)
	}
	if msg.dest != "gpu01" {
		t.Errorf("dest = %q, want gpu01", msg.dest)
	}
	if msg.paneID != "pane-7" {
		t.Errorf("paneID = %q, want pane-7", msg.paneID)
	}
}

// TestEventDismissed_RemovesCard pins the client-side half of spec §8.4's
// dismissal broadcast: a named id removes that one card, and an empty id
// clears the whole sidebar list — mirroring the daemon's own DismissEventPayload
// "" = all convention. Both events resolve to the same ("") dest here (no
// projects are set up, so destOfPane falls back to activeDest, "" either
// way) — TestEventDismissed_DismissAllScopedToDest covers the multi-dest case.
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

// TestEventDismissed_DismissAllScopedToDest pins review round 1's Important
// 4: a dismiss-all ("" event id) from ONE destination must remove only that
// destination's cards, not every attached daemon's. destOfPane resolves each
// stored event's pane through the client's projects, so this test sets up
// one project per destination, each holding the pane the matching event
// names.
func TestEventDismissed_DismissAllScopedToDest(t *testing.T) {
	t.Parallel()
	tabA := NewTabModel("t-a", "A")
	tabA.Root = NewLeaf(NewPaneModel("pane-a", 1024))
	tabB := NewTabModel("t-b", "B")
	tabB.Root = NewLeaf(NewPaneModel("pane-b", 1024))
	m := Model{
		cfg:           config.Default(),
		client:        &fakeSender{},
		notifications: NewNotificationCenter(30, 50),
		projects: []*ProjectModel{
			{ID: "proj-a", Dest: "hostA", tabs: []*TabModel{tabA}},
			{ID: "proj-b", Dest: "hostB", tabs: []*TabModel{tabB}},
		},
	}
	m.notifications.AddEvent(ipc.PaneEventPayload{ID: "evt-a", PaneID: "pane-a", Type: "bell"})
	m.notifications.AddEvent(ipc.PaneEventPayload{ID: "evt-b", PaneID: "pane-b", Type: "bell"})

	updated, _ := m.Update(eventDismissedMsg{dest: "hostA", eventID: ""})
	got := updated.(Model)

	if n := got.notifications.Count(); n != 1 {
		t.Fatalf("count after dismiss-all from hostA = %d, want 1", n)
	}
	if got.notifications.events[0].ID != "evt-b" {
		t.Errorf("remaining event = %q, want evt-b (hostB's card must survive hostA's dismiss-all)", got.notifications.events[0].ID)
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
