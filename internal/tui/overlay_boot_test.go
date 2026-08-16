package tui

import (
	"testing"
	"time"
)

// arrivedOverlay returns a model whose tab has just adopted a freshly created
// overlay pane, as reconcileOverlayPane does when this client's Alt+G/Alt+D
// created it (pendingOverlayShow armed).
func arrivedOverlay(t *testing.T) (Model, *TabModel) {
	t.Helper()
	m := Model{pendingOverlayShow: map[string]bool{"tab-1": true}}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(state, "")
	tab := m.curTabs()[0]
	if tab.overlayPane == nil {
		t.Fatal("the overlay was not adopted")
	}
	tab.Resize(100, 38)
	return m, tab
}

// A pane created in the layout tree while the TUI is running is marked
// preparing, which is what puts the spinner and the boot label on screen while
// the child starts. Overlay panes live OUTSIDE that tree, and
// reconcileOverlayPane built them with a bare NewPaneModel — so lazygit and
// hunk showed an empty full-tab rectangle for their entire startup, which on a
// large repository is seconds.
func TestOverlayArrival_ShowsABootIndicator(t *testing.T) {
	_, tab := arrivedOverlay(t)
	if !tab.overlayPane.showRestoreIndicator() {
		t.Errorf("a freshly spawned overlay shows no boot indicator (preparing=%v resuming=%v pending=%v)",
			tab.overlayPane.preparing, tab.overlayPane.resuming, tab.overlayPane.Pending)
	}
}

// An overlay adopted on a plain REATTACH is a tool that has been running for
// hours; claiming it is starting up would be a confidently wrong answer, and it
// is hidden by default anyway.
func TestOverlayReattach_ClaimsNoBoot(t *testing.T) {
	m := Model{}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}
	m.applyWorkspaceState(state, "")
	tab := m.curTabs()[0]
	if tab.overlayPane == nil {
		t.Fatal("the overlay was not adopted")
	}
	if tab.overlayPane.preparing {
		t.Error("an overlay this client did not create is marked preparing")
	}
}

// Marking the pane is only half of it: the spinner tick resolves its pane by
// walking tab.Root, which an overlay pane is deliberately absent from. Without
// this the indicator renders one frozen frame and the tick chain stops on its
// first message.
func TestSpinnerTick_ReachesTheOverlayPane(t *testing.T) {
	m, tab := arrivedOverlay(t)
	updated, cmd := m.Update(spinnerTickMsg{paneID: tab.overlayPane.ID, frame: 3})
	got := updated.(Model)
	overlay := got.curTabs()[0].overlayPane

	if overlay.spinnerFrame != 3 {
		t.Errorf("overlay spinner frame = %d, want 3 — the tick never found the pane", overlay.spinnerFrame)
	}
	if cmd == nil {
		t.Error("the tick chain stopped: the overlay's spinner will never advance")
	}
}

// The other half of the settle: once the tool really paints, the indicator has
// to come DOWN. The test above pins that a blank boot frame does not settle it,
// which a permanently-stuck flag would also satisfy.
func TestOverlayRealContent_SettlesTheIndicator(t *testing.T) {
	m, tab := arrivedOverlay(t)
	// Past restoreMinDisplay, the way restore_indicator_test.go backdates it —
	// restoreSettled() is deliberately not a "first byte" test, so a settle
	// assertion inside the minimum display window would prove nothing.
	tab.overlayPane.resumeStart = time.Now().Add(-5 * time.Second)

	updated, _ := m.Update(PaneOutputMsg{PaneID: tab.overlayPane.ID, Data: []byte("lazygit")})
	got := updated.(Model)
	overlay := got.curTabs()[0].overlayPane

	if overlay.preparing {
		t.Error("the indicator is still up after the tool painted")
	}
}

// The settle condition claims to mirror the layout-tree branch, which reads
// `resuming || preparing`. No overlay arms resuming today, so a narrower
// condition is inert — and would silently strand the next overlay path that
// does, leaving only the 30 s safety cap to clear it.
func TestOverlaySettle_CoversResumingLikeTheTreeBranch(t *testing.T) {
	m, tab := arrivedOverlay(t)
	tab.overlayPane.preparing = false
	tab.overlayPane.resuming = true
	tab.overlayPane.resumeStart = time.Now().Add(-5 * time.Second)

	updated, _ := m.Update(PaneOutputMsg{PaneID: tab.overlayPane.ID, Data: []byte("lazygit")})
	got := updated.(Model)
	overlay := got.curTabs()[0].overlayPane

	if overlay.resuming {
		t.Error("a resuming overlay is never settled by its own output")
	}
}

// The tick-chain START site, driven through Update rather than by calling
// applyWorkspaceState directly — the loop that arms the chain lives in the
// WorkspaceStateMsg arm, so every test that calls applyWorkspaceState by hand
// proves the marking and the tick HANDLER while skipping the site that connects
// them. That is the bypassed-call-site shape this package has shipped before.
func TestWorkspaceState_ArmsTheSpinnerChainForANewOverlay(t *testing.T) {
	m := Model{width: 100, height: 40, pendingOverlayShow: map[string]bool{"tab-1": true}}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "t", Panes: []string{"pane-n", "pane-o"}}},
		Panes: []PaneInfo{
			{ID: "pane-n", TabID: "tab-1", Type: "terminal"},
			{ID: "pane-o", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}

	updated, _ := m.Update(state)
	got := updated.(Model)
	tab := got.curTabs()[0]

	if tab.overlayPane == nil {
		t.Fatal("the overlay was not adopted")
	}
	if !tab.overlayPane.spinnerTickRunning {
		t.Error("no spinner chain was armed for the overlay — its indicator cannot animate")
	}
}

// The overlay branch of handlePaneOutput cleared preparing on the FIRST byte,
// where the layout-tree branch asks restoreSettled(). A boot frame that only
// clears the screen leaves the pane blank, so clearing there takes the
// indicator down and returns the user to the empty rectangle it exists to
// replace.
func TestOverlayBootFrame_KeepsTheIndicatorWhileTheScreenIsStillBlank(t *testing.T) {
	m, tab := arrivedOverlay(t)
	updated, _ := m.Update(PaneOutputMsg{PaneID: tab.overlayPane.ID, Data: []byte("\x1b[2J")})
	got := updated.(Model)
	overlay := got.curTabs()[0].overlayPane

	if !overlay.showRestoreIndicator() {
		t.Errorf("a screen-clearing boot frame took the indicator down (preparing=%v blank=%v)",
			overlay.preparing, overlay.screenBlank())
	}
}
