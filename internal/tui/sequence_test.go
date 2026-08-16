// sequence_test.go covers the prefix sequence machine: the tier-agnostic
// probe, the inert modes, cancellation, the literal escape, and the timeout
// generation guard.
//
// Everything here drives Update or handleKey rather than calling MatchSeq
// directly. A direct-call test and its mutation can both pass against a
// decision the call site makes unreachable, which this repo has hit before.
package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// seqModel builds a Model with three tabs, two panes on the active one, and a
// keymap rebuilt from cfg after mutate runs.
//
// The panes are load-bearing, not scenery: focus mode and pane navigation are
// no-ops on a paneless tab, so an action test against newModelForTest alone
// passes whether the action fired or not. Same for notifications — the sidebar
// guard short-circuits on sidebarFocused, so production never dereferences the
// nil, but a test that forces both halves true does.
func seqModel(t *testing.T, mutate func(*Model)) Model {
	t.Helper()
	m := newModelForTest([]string{"A", "B", "C"}, 0)
	m.notifications = NewNotificationCenter(30, 200)
	// Several of these tests deliberately let a key fall through to the pane,
	// and that path ends at client.Send — nil in a hand-built Model.
	m.client = &fakeSender{}
	tab := m.curTabs()[0]
	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	tab.Root = NewLeaf(p1)
	tab.Root.SplitLeaf("p1", SplitHorizontal)
	tab.Root.Right.Pane = p2
	tab.ActivePane = "p1"
	m.width, m.height = 100, 40
	tab.Resize(100, 38)
	if mutate != nil {
		mutate(&m)
	}
	m.initKeymap()
	return m
}

// press drives one key through Update and returns the resulting Model.
func press(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return got
}

func ctrlB() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl} }

// Late-tier completion, end to end. pane.focus_toggle is chosen because its
// effect is local Model state — a bare test Model has no daemon connection, so
// an action whose only effect is an IPC send would look identical whether it
// fired or not.
func TestSequence_CompletesThroughUpdate(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.FocusPane = "ctrl+b z" })

	before := m.activeTabModel().FocusMode()
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 1 {
		t.Fatalf("after ctrl+b, pendingSeq = %v, want one chord armed", m.pendingSeq)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'z'})
	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq not cleared after completion: %v", m.pendingSeq)
	}
	if got := m.activeTabModel().FocusMode(); got == before {
		t.Errorf("focus mode still %v — the sequence did not fire pane.focus_toggle", got)
	}
}

// Early-tier completion. sidebar.toggle flips local state too, and it exercises
// the other half of the tier split: an early action must run from the early
// switch, above the raw-key seam.
func TestSequence_EarlyTierSequenceCompletes(t *testing.T) {
	m := seqModel(t, func(m *Model) {
		m.cfg.Keybindings.SidebarToggle = "ctrl+b s"
		// toggleProjectSidebar refuses below minWidthForSidebar and only
		// flashes, which would make this pass for the wrong reason.
		m.width = minWidthForSidebar + 20
	})

	before := m.cfg.UI.SidebarOpen
	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: 's'})
	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq not cleared after an early-tier completion: %v", m.pendingSeq)
	}
	if m.cfg.UI.SidebarOpen == before {
		t.Errorf("SidebarOpen still %v — the early-tier sequence did not fire", before)
	}
}

// The property that makes the machine safe to merge: a chord bound to nothing
// must not arm the machine, and a chord bound normally must still fire from
// its own tier with the machine present.
func TestSequence_UnrelatedChordStillDispatches(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })

	before := m.activeTabModel().FocusMode()
	m = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}) // pane.focus_toggle, untouched
	if len(m.pendingSeq) != 0 {
		t.Errorf("an unrelated chord must not arm the machine: %v", m.pendingSeq)
	}
	if m.activeTabModel().FocusMode() == before {
		t.Error("ctrl+e stopped toggling focus mode once the sequence machine existed")
	}
}

// Tier-agnostic probe, end to end. pane.close is late-tier, so its opening
// chord is in neither tier map — a tier-scoped probe hangs here forever.
func TestSequence_LateTierSequenceArmsAndCompletes(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.ClosePane = "ctrl+b x" })

	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 1 {
		t.Fatalf("a late-tier sequence head must arm the machine, pendingSeq = %v", m.pendingSeq)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'x'})
	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq not cleared after a late-tier completion: %v", m.pendingSeq)
	}
}

func TestSequence_InertWhileSidebarFocused(t *testing.T) {
	m := seqModel(t, func(m *Model) {
		m.cfg.Keybindings.NewTab = "ctrl+b c"
		m.notifications.visible = true
		m.sidebarFocused = true
	})
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert while the sidebar has focus, got %v", m.pendingSeq)
	}
}

func TestSequence_InertDuringSelection(t *testing.T) {
	m := seqModel(t, func(m *Model) {
		m.cfg.Keybindings.NewTab = "ctrl+b c"
		m.selection = &Selection{PaneID: "p1"}
	})
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert during an active selection, got %v", m.pendingSeq)
	}
}

func TestSequence_InertWhileDialogOpen(t *testing.T) {
	m := seqModel(t, func(m *Model) {
		m.cfg.Keybindings.NewTab = "ctrl+b c"
		m.dialog = dialogSettings
	})
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert while a dialog owns the keyboard, got %v", m.pendingSeq)
	}
}

func TestSequence_InertWhileRenaming(t *testing.T) {
	m := seqModel(t, func(m *Model) {
		m.cfg.Keybindings.NewTab = "ctrl+b c"
		m.renaming = true
	})
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert during a rename, got %v", m.pendingSeq)
	}
}

func TestSequence_EscCancels(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(m.pendingSeq) != 0 {
		t.Errorf("esc must cancel a pending sequence, got %v", m.pendingSeq)
	}
}

// A click can change the active pane, so a sequence completed after one would
// target a different pane than the one the prefix was pressed in.
func TestSequence_MouseClickCancels(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m = press(t, m, ctrlB())
	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if got := updated.(Model); len(got.pendingSeq) != 0 {
		t.Errorf("a mouse click must cancel a pending sequence, got %v", got.pendingSeq)
	}
}

// Paste bypasses handleKey and lands in the PTY; an armed prefix would read
// the next keystroke as a sequence step.
func TestSequence_PasteCancels(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m = press(t, m, ctrlB())
	updated, _ := m.Update(tea.PasteMsg{Content: "hello"})
	if got := updated.(Model); len(got.pendingSeq) != 0 {
		t.Errorf("a paste must cancel a pending sequence, got %v", got.pendingSeq)
	}
}

// prefix prefix sends ONE literal chord downstream. Without this, a pane
// running ssh to a host running tmux has no reachable prefix at all.
func TestSequence_LiteralEscape(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })

	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 1 {
		t.Fatalf("setup: ctrl+b did not arm the machine, got %v", m.pendingSeq)
	}

	next, _, consumed := m.stepSequence(ctrlB(), ctrlB().String())
	if consumed {
		t.Fatal("the second prefix press must fall through to the pane, not be consumed")
	}
	if len(next.pendingSeq) != 0 {
		t.Errorf("the machine must disarm on the literal escape, got %v", next.pendingSeq)
	}
	if got := string(keyToBytes(ctrlB())); got != "\x02" {
		t.Errorf("keyToBytes(ctrl+b) = %q, want \\x02 — the literal escape relies on this", got)
	}
}

func TestSequence_StatusBarShowsPending(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m.lastWidth, m.lastHeight = 120, 40

	m = press(t, m, ctrlB())
	if got := m.renderStatusBar(); !strings.Contains(got, "ctrl+b") {
		t.Errorf("the status bar must show the pending chords, got %q", got)
	}
}

func TestSequence_DroppedSequenceFlashes(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m.lastWidth, m.lastHeight = 120, 40

	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: 'z'}) // no ctrl+b z binding
	if m.seqFlash == "" {
		t.Fatal("a dropped sequence must set a flash message, not silently no-op")
	}
	if got := m.renderStatusBar(); !strings.Contains(got, "ctrl+b z") {
		t.Errorf("the flash must name the sequence that was dropped, got %q", got)
	}
}

// A stale tick from a cancelled sequence must not clear the one started after
// it. Without the generation guard this is a race that only shows up under a
// fast typist, which is the worst way to find it.
func TestSequence_StaleTimeoutTickIsIgnored(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m.seqTimeout = time.Second

	m = press(t, m, ctrlB())
	stale := m.pendingGen
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // cancel
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 1 {
		t.Fatalf("setup: the second ctrl+b did not arm, got %v", m.pendingSeq)
	}

	updated, _ := m.Update(sequenceTimeoutMsg{gen: stale})
	if got := updated.(Model); len(got.pendingSeq) != 1 {
		t.Errorf("a stale tick cleared a live sequence: pendingSeq = %v", got.pendingSeq)
	}
}

func TestSequence_CurrentTimeoutTickClears(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	m.seqTimeout = time.Second

	m = press(t, m, ctrlB())
	updated, _ := m.Update(sequenceTimeoutMsg{gen: m.pendingGen})
	if got := updated.(Model); len(got.pendingSeq) != 0 {
		t.Errorf("the current tick must clear the sequence, got %v", got.pendingSeq)
	}
}

// Off by default, matching tmux. A zero duration must arm no timer at all.
func TestSequence_TimeoutOffArmsNoTimer(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.NewTab = "ctrl+b c" })
	if m.seqTimeout != 0 {
		t.Fatalf("the shipped default must be off, got %v", m.seqTimeout)
	}
	if cmd := m.armSequenceTimeout(); cmd != nil {
		t.Error("a zero timeout must arm no timer")
	}
}
