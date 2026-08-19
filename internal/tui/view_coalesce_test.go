package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// Render coalescing.
//
// Bubble Tea calls model.View() once per message (tea.go: `p.render(model)`
// after every Update); its FPS option throttles the terminal flush, not the
// View construction. A frame costs ~2.8 ms at 37 tabs, so every message the
// TUI receives — including timer ticks that change nothing — pays a full
// rebuild. Production 2026-08-18: ~16 frames/s, of which the overwhelming
// majority changed no pixel.
//
// The design is deliberately fail-SAFE: rendering is the default and only
// message types audited as provably inert may skip. A branch nobody has
// examined renders, exactly as before.
//
// These tests are the audit. For each skipping branch they assert BOTH halves:
// that the rebuild really was skipped (so the optimisation exists at all), and
// that the delivered frame matches a FORCED REBUILD of the model Update
// returned (so the skip was honest).
//
// The second half is why the comparison is against a forced rebuild rather than
// against the previous frame. See TestView_InertMessagesReuseTheCachedFrame.

// coalesceModel builds a rendering-capable Model with the frame cache
// installed, mirroring what NewModel wires up in production.
//
// Takes *testing.T so every pane it mints is disposed: each one owns a VT
// emulator with a parked drain goroutine and a scrollback allocation, and this
// helper is called once per subtest.
func coalesceModel(t *testing.T) Model {
	t.Helper()
	m := benchModel(6, 2)
	m.viewCache = &viewCacheBox{}
	t.Cleanup(func() {
		for _, proj := range m.projects {
			for _, tab := range proj.tabs {
				for _, p := range tab.Leaves() {
					if p != nil {
						p.Dispose()
					}
				}
			}
		}
	})
	return m
}

// The honesty check compares the delivered frame against a FORCED REBUILD of
// the very model that was returned — never against the frame from before the
// message.
//
// Comparing before/after cannot fail by construction: when the skip works,
// View returns the cached struct, so `after` and `before` are the same value
// and the assertion is a tautology. That tautology is exactly why the
// context-menu case below escaped the first audit of this feature. Rebuilding
// the returned model is the only way to ask "would an honest render have
// produced something different?".
func TestView_InertMessagesReuseTheCachedFrame(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "sizePoll", msg: sizePollMsg{}},
		{name: "memoryTick", msg: memoryTickMsg{}},
		{name: "listenContinue", msg: listenContinueMsg{}},
		// The 1 s poll's echo: Bubble Tea re-reports a size that already
		// matches. Update's own early return does nothing with it.
		{name: "windowSizeEcho", msg: tea.WindowSizeMsg{Width: 200, Height: 50}},
		// A message type Update has no case for cannot have changed the model.
		{name: "unhandledType", msg: struct{ unknownToUpdate bool }{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := coalesceModel(t)
			// Prime: the size-echo case needs the model to already agree with
			// the size it will be told about.
			m.sized = true
			m.pendingWidth, m.pendingHeight = m.width, m.height

			if m.View(); m.viewCache.builds == 0 {
				t.Fatal("first View() must build a frame")
			}
			builds := m.viewCache.builds

			next, _ := m.Update(tc.msg)
			nextModel := next.(Model)
			delivered := nextModel.View()
			skipped := m.viewCache.builds == builds

			// What an honest render of the SAME returned model produces.
			forced := nextModel
			forced.skipRender = false
			honest := forced.View()

			if delivered.Content != honest.Content {
				t.Errorf("%s: the delivered frame is STALE — an honest rebuild of the "+
					"same model differs, so something in Update moved the screen while "+
					"this message was treated as inert", tc.name)
			}
			if delivered.MouseMode != honest.MouseMode {
				t.Errorf("%s: delivered MouseMode %v != honest %v — a cached frame "+
					"carries the mouse mode, so a stale one leaves the terminal in the "+
					"wrong reporting mode", tc.name, delivered.MouseMode, honest.MouseMode)
			}
			if !skipped {
				t.Errorf("%s: View rebuilt instead of reusing the cached frame; "+
					"the optimisation did not happen", tc.name)
			}
		})
	}
}

// The spinner is the counter-case: it changes the screen ten times a second by
// design, so it must never be coalesced away. If this passes while the test
// above also passes, the predicate is discriminating rather than constant.
func TestView_VisibleChangeStillRebuilds(t *testing.T) {
	m := coalesceModel(t)
	// A pane mid-turn is what keeps the spinner ticking at all.
	m.activeTabModel().Leaves()[0].working = true

	before := m.View()
	builds := m.viewCache.builds

	next, _ := m.Update(workSpinnerTickMsg{})
	after := next.(Model).View()

	if m.viewCache.builds == builds {
		t.Error("a work-spinner tick must rebuild the frame — it advances a visible glyph")
	}
	if after.Content == before.Content {
		t.Error("spinner tick produced an identical frame; fixture is not animating, so this test proves nothing")
	}
}

// The hazard this design had to survive.
//
// ackFocusedPane runs BEFORE Update's switch, on every message, and clears the
// focused pane's `unseen` flag — which the tab bar and sidebar both render. So
// an "inert" message can still move the screen, via a code path that has
// nothing to do with the message itself. A pane marked unseen by one message
// would have its badge cleared by the NEXT message's ack, and if that message
// were on the skip list the badge would stay painted on a stale frame.
//
// Skipping therefore defers to whether the ack actually mutated anything.
func TestView_InertMessageStillRebuildsWhenAckClearsUnseen(t *testing.T) {
	m := coalesceModel(t)
	tab := m.activeTabModel()
	focused := tab.Leaves()[0]
	tab.ActivePane = focused.ID

	// Render once so a cached frame exists, THEN mark unseen — reproducing the
	// real ordering, where output marks the pane after the last paint.
	before := m.View()
	builds := m.viewCache.builds
	focused.unseen = true

	next, _ := m.Update(sizePollMsg{})
	after := next.(Model).View()

	if m.viewCache.builds == builds {
		t.Error("ackFocusedPane cleared `unseen` during an inert message, so the " +
			"frame moved and MUST be rebuilt — skipping here paints a stale badge")
	}
	if focused.unseen {
		t.Fatal("fixture wrong: ackFocusedPane did not clear unseen, so the hazard was never exercised")
	}
	_ = after
	_ = before
}

// The second prologue hazard, and the one the first audit of this feature
// missed — which is why the gate is now named for the whole region.
//
// Update prunes a context menu whose target pane has vanished, before the type
// switch, on EVERY message. View both DRAWS that menu and derives v.MouseMode
// from it. So an inert message can close the menu and, without the fold into
// prologueChangedView, hand back a cached frame that still shows it: a menu the
// model believes is closed, painted on screen, with clicks routing to the pane
// underneath — and the terminal left in all-motion mouse reporting.
//
// Reachable in ordinary use: another client destroys the pane (MCP
// destroy_pane, a second TUI, daemon reconciliation) while the menu is open,
// and the 1 s size poll prunes it a moment later.
func TestView_InertMessageStillRebuildsWhenProloguePrunesCtxMenu(t *testing.T) {
	m := coalesceModel(t)
	m.sized = true
	m.pendingWidth, m.pendingHeight = m.width, m.height
	m.ctxMenu = ctxMenuState{
		paneID: "pane-destroyed-elsewhere",
		title:  "gone",
		items:  []ctxMenuItem{{label: "Close", enabled: true}},
	}

	m.View()
	builds := m.viewCache.builds

	next, _ := m.Update(sizePollMsg{})
	nextModel := next.(Model)
	delivered := nextModel.View()

	if m.viewCache.builds == builds {
		t.Error("the prologue closed the context menu during an inert message, so the " +
			"frame moved and MUST be rebuilt — reusing the cache paints a menu the " +
			"model considers closed")
	}
	if nextModel.ctxMenu.open() {
		t.Fatal("fixture wrong: the prologue did not prune the menu, so the hazard was never exercised")
	}

	forced := nextModel
	forced.skipRender = false
	if honest := forced.View(); delivered.Content != honest.Content {
		t.Error("delivered frame still differs from an honest rebuild")
	}
}

// A Model built without the cache (every test in this package that constructs
// Model{} directly, and any future caller) must still render normally rather
// than panicking or returning an empty frame.
func TestView_NilCacheAlwaysRenders(t *testing.T) {
	m := benchModel(3, 1)
	m.viewCache = nil

	first := m.View()
	next, _ := m.Update(sizePollMsg{})
	second := next.(Model).View()

	if first.Content == "" || second.Content == "" {
		t.Error("View with no cache installed must still build a real frame")
	}
}

// The optimisation only exists in production if NewModel wires the cache up.
func TestNewModel_InstallsFrameCache(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := NewModel(nil, config.Config{}, "test", nil, nil, nil)
	if m.viewCache == nil {
		t.Error("NewModel must install the frame cache, or coalescing is dead code in production")
	}
}

// primeHiddenPane drives one chunk of output through a pane so its
// once-per-pane transitions are spent.
//
// A fresh pane's FIRST live chunk sets liveOutputSeen and may settle a restore;
// both legitimately force a rebuild, so measuring steady-state coalescing
// without this measures the boot path instead and always sees a rebuild.
func primeHiddenPane(t *testing.T, m Model, paneID string) Model {
	t.Helper()
	primed, _ := m.Update(PaneOutputMsg{PaneID: paneID, Data: []byte("prime\r\n")})
	out := primed.(Model)
	_ = out.View()
	return out
}

// hiddenPaneID returns a pane on a tab other than the active one.
func hiddenPaneID(t *testing.T, m *Model) string {
	t.Helper()
	active := m.activeTabModel()
	for _, tab := range m.allTabs() {
		if active != nil && tab.ID == active.ID {
			continue
		}
		if tab.Root == nil {
			continue
		}
		if leaves := tab.Leaves(); len(leaves) > 0 {
			return leaves[0].ID
		}
	}
	t.Fatal("fixture must have a second tab with at least one pane")
	return ""
}

func TestPaneIsVisible_ActiveTabPanesVisibleOthersNot(t *testing.T) {
	m := coalesceModel(t)

	active := m.activeTabModel()
	if active == nil || active.Root == nil {
		t.Fatal("fixture must have an active tab with a layout tree")
	}
	visibleID := active.Leaves()[0].ID
	if !m.paneIsVisible(visibleID) {
		t.Errorf("pane %s is in the active tab but reported hidden", visibleID)
	}

	hiddenID := hiddenPaneID(t, &m)
	if m.paneIsVisible(hiddenID) {
		t.Errorf("pane %s is on an inactive tab but reported visible", hiddenID)
	}
}

func TestPaneIsVisible_UnknownPaneIsNotVisible(t *testing.T) {
	m := coalesceModel(t)
	if m.paneIsVisible("pane-that-does-not-exist") {
		t.Error("an unknown pane id must not report visible")
	}
}

func TestPaneIsVisible_ActiveTabOverlayIsVisible(t *testing.T) {
	m := coalesceModel(t)
	active := m.activeTabModel()
	if active == nil {
		t.Fatal("fixture must have an active tab")
	}

	overlay := NewPaneModel("overlay-1", 4096)
	active.overlayPane = overlay
	// Every PaneModel owns a VT emulator with a parked drain goroutine and a
	// scrollback allocation. Nilling the field reclaims neither.
	t.Cleanup(func() {
		active.overlayPane = nil
		overlay.Dispose()
	})

	if !m.paneIsVisible("overlay-1") {
		t.Error("the active tab's overlay pane is on screen and must report visible")
	}
}

// An overlay belonging to a tab the user is NOT looking at is not on screen.
func TestPaneIsVisible_InactiveTabOverlayIsNotVisible(t *testing.T) {
	m := coalesceModel(t)
	active := m.activeTabModel()

	var other *TabModel
	for _, tab := range m.allTabs() {
		if active == nil || tab.ID != active.ID {
			other = tab
			break
		}
	}
	if other == nil {
		t.Fatal("fixture must have a second tab")
	}

	overlay := NewPaneModel("overlay-hidden", 4096)
	other.overlayPane = overlay
	t.Cleanup(func() {
		other.overlayPane = nil
		overlay.Dispose()
	})

	if m.paneIsVisible("overlay-hidden") {
		t.Error("an overlay on an inactive tab is not rendered and must report hidden")
	}
}

// Output from a pane on a tab the user is not looking at cannot change the
// frame: View() renders only the active tab.
func TestUpdate_HiddenPaneOutputServesCachedFrame(t *testing.T) {
	m := coalesceModel(t)
	// benchModel leaves perfStats nil — the recorders tolerate that, but reading
	// the counter field directly does not. Install a real one.
	m.perfStats = newEventLoopStats()

	if m.View(); m.viewCache.builds == 0 {
		t.Fatal("first View() must build a frame")
	}

	hiddenID := hiddenPaneID(t, &m)
	m = primeHiddenPane(t, m, hiddenID)

	builds := m.viewCache.builds
	before := m.perfStats.viewHidden

	updated, _ := m.Update(PaneOutputMsg{PaneID: hiddenID, Data: []byte("noise\r\n")})
	after := updated.(Model)

	if !after.skipRender {
		t.Fatal("hidden-pane output must mark the message inert")
	}
	_ = after.View()
	if after.viewCache.builds != builds {
		t.Error("View rebuilt instead of reusing the cached frame; the optimisation did not happen")
	}
	if after.perfStats.viewHidden != before+1 {
		t.Errorf("viewHidden = %d, want %d — the skip was not attributed to a hidden pane",
			after.perfStats.viewHidden, before+1)
	}
}

// The honesty check. A cached frame must equal what a forced rebuild produces.
//
// Compares against a REBUILD of the returned model — not against a frame taken
// before the Update — because when the skip works those two are the same cached
// struct and the assertion would be a tautology (round-1 code-quality/6).
func TestUpdate_HiddenPaneCachedFrameMatchesForcedRebuild(t *testing.T) {
	m := coalesceModel(t)
	_ = m.View()

	hiddenID := hiddenPaneID(t, &m)
	m = primeHiddenPane(t, m, hiddenID)

	updated, _ := m.Update(PaneOutputMsg{PaneID: hiddenID, Data: []byte("noise\r\n")})
	after := updated.(Model)

	delivered := after.View()

	forced := after
	forced.skipRender = false
	honest := forced.View()

	if delivered.Content != honest.Content {
		t.Error("the delivered frame is STALE — an honest rebuild of the same model " +
			"differs, so hidden-pane output moved the screen after all")
	}
	if delivered.MouseMode != honest.MouseMode {
		t.Errorf("delivered MouseMode %v != honest %v — a cached frame carries the "+
			"mouse mode, so a stale one leaves the terminal in the wrong reporting mode",
			delivered.MouseMode, honest.MouseMode)
	}
}

// Output from the pane the user IS looking at must always rebuild.
func TestUpdate_VisiblePaneOutputAlwaysRebuilds(t *testing.T) {
	m := coalesceModel(t)
	_ = m.View()

	visibleID := m.activeTabModel().Leaves()[0].ID
	updated, _ := m.Update(PaneOutputMsg{PaneID: visibleID, Data: []byte("hello\r\n")})
	if updated.(Model).skipRender {
		t.Fatal("visible-pane output must never be coalesced")
	}
}

// A CWD change forces a rebuild even off-screen. Not because anything outside
// the pane draws a CWD today — nothing does — but because it fires at most a
// handful of times per pane and is the obvious candidate for a future sidebar
// column. Pinned so the conservatism stays deliberate rather than accidental.
//
// OSC 7 updates Pane.CWD synchronously inside AppendOutput via the VT callback,
// so this drives the real path.
func TestUpdate_HiddenPaneCWDChangeStillRebuilds(t *testing.T) {
	m := coalesceModel(t)
	_ = m.View()

	hiddenID := hiddenPaneID(t, &m)
	// Primed, or the first-live-output branch would force the rebuild and this
	// test would pass without the CWD branch existing at all.
	m = primeHiddenPane(t, m, hiddenID)

	osc7 := []byte("\x1b]7;file://host/tmp/quil-cwd-probe\x07")
	updated, _ := m.Update(PaneOutputMsg{PaneID: hiddenID, Data: osc7})
	if updated.(Model).skipRender {
		t.Fatal("a CWD change must force a rebuild even for an off-screen pane")
	}
}

// An unknown pane id touches nothing, so it must not force a rebuild either.
func TestUpdate_UnknownPaneOutputIsInert(t *testing.T) {
	m := coalesceModel(t)
	_ = m.View()

	updated, _ := m.Update(PaneOutputMsg{PaneID: "no-such-pane", Data: []byte("x")})
	if !updated.(Model).skipRender {
		t.Error("output for a pane that does not exist changed nothing and must be inert")
	}
}
