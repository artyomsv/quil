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
