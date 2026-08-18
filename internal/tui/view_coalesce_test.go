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
// the frame is byte-identical across the message (so skipping is honest), and
// the rebuild really was skipped (so the optimisation exists at all).

// coalesceModel builds a rendering-capable Model with the frame cache
// installed, mirroring what NewModel wires up in production.
func coalesceModel() Model {
	m := benchModel(6, 2)
	m.viewCache = &viewCacheBox{}
	return m
}

func TestView_InertMessagesReuseTheCachedFrame(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"sizePoll", sizePollMsg{}},
		{"memoryTick", memoryTickMsg{}},
		{"listenContinue", listenContinueMsg{}},
		// The 1 s poll's echo: Bubble Tea re-reports a size that already
		// matches. Update's own early return does nothing with it.
		{"windowSizeEcho", tea.WindowSizeMsg{Width: 200, Height: 50}},
		// A message type Update has no case for cannot have changed the model.
		{"unhandledType", struct{ unknownToUpdate bool }{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := coalesceModel()
			// Prime: the size-echo case needs the model to already agree with
			// the size it will be told about.
			m.sized = true
			m.pendingWidth, m.pendingHeight = m.width, m.height

			before := m.View()
			builds := m.viewCache.builds
			if builds == 0 {
				t.Fatal("first View() must build a frame")
			}

			next, _ := m.Update(tc.msg)
			after := next.(Model).View()

			if got := m.viewCache.builds; got != builds {
				t.Errorf("View rebuilt after an inert %s (builds %d -> %d); "+
					"the whole point is to reuse the cached frame", tc.name, builds, got)
			}
			if after.Content != before.Content {
				t.Errorf("%s was treated as inert but the frame CHANGED — "+
					"skipping it would have left a stale screen", tc.name)
			}
		})
	}
}

// The spinner is the counter-case: it changes the screen ten times a second by
// design, so it must never be coalesced away. If this passes while the test
// above also passes, the predicate is discriminating rather than constant.
func TestView_VisibleChangeStillRebuilds(t *testing.T) {
	m := coalesceModel()
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
	m := coalesceModel()
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
