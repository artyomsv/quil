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

	"github.com/artyomsv/quil/internal/config"

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

	// Driven through Update, not stepSequence: the payoff is one \x02 reaching
	// the PANE, and asserting "unhandled" plus "keyToBytes encodes \x02"
	// separately is two halves that never meet. seqModel wires an inputCh, so
	// the bytes are observable where they actually land.
	m.inputCh = make(chan paneInput, inputForwardBuffer)
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must disarm on the literal escape, got %v", m.pendingSeq)
	}

	var forwarded []byte
	select {
	case in := <-m.inputCh:
		forwarded = in.data
	default:
	}
	if string(forwarded) != "\x02" {
		t.Errorf("the pane received %q, want one literal \\x02 — a tmux inside a "+
			"Quil pane has no reachable prefix without this", string(forwarded))
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
	m = press(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"}) // no ctrl+b z binding
	if m.seqFlash == "" {
		t.Fatal("a dropped sequence must set a flash message, not silently no-op")
	}
	got := m.renderStatusBar()
	if !strings.Contains(got, "ctrl+b") {
		t.Errorf("the flash must name the pending prefix, got %q", got)
	}
	// The unmatched chord must NOT be echoed. Ctrl+B is readline's
	// backward-char, so under the tmux preset the machine arms on an ordinary
	// shell keystroke — and the character that ends the sequence could be a
	// character of a password at an ssh or sudo prompt. It is swallowed either
	// way; painting it on the status bar is what this guards against.
	if strings.Contains(got, "ctrl+b z") {
		t.Errorf("the flash echoed the swallowed keystroke: %q", got)
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

// The notes-mode readers moved off raw config-string comparison onto the
// registry. A multi-binding is the cheapest proof: the fallback half of
// "alt+w,f10" can never match a whole-string compare, so these fail against the
// pre-migration code and pass after it.
func TestNotesKeyExempt_ResolvesThroughRegistry(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.CloseTab = "alt+w,f10" })

	if !m.notesKeyExempt("f10") {
		t.Error("a non-primary tab.close binding must be exempt in notes mode")
	}
	if !m.notesKeyExempt("alt+w") {
		t.Error("the primary tab.close binding must still be exempt")
	}
	if m.notesKeyExempt("ctrl+shift+f12") {
		t.Error("an unbound key must not be exempt")
	}
}

func TestNotesMode_StructuralKeyHonoursMultiBinding(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.ClosePane = "ctrl+w,f11" })
	m.notesMode = true
	m.notesPaneFocused = false

	// notesKeyExempt is the shared oracle for both notes readers; pane.close is
	// reached through the structural branch, which used the same comparison.
	if !m.notesKeyExempt("f11") {
		t.Error("a non-primary pane.close binding must be recognised in notes mode")
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

// TestSequence_BeatsPluginRawKeysOnFinalChord pins the ONE deliberate
// precedence change in the sequence machine.
//
// tryPluginRawKey normally lets a pane's tool claim a key outright — that is
// what the late tier loses to. A completed sequence must beat that claim on its
// final chord, or `pane.close = "ctrl+b x"` is dead on any pane whose plugin
// claims x. The shipped tmux preset binds exactly that, so this is the
// configuration users get, not a hypothetical.
//
// The control row is what makes the test meaningful: pressing the same final
// chord ALONE must still be claimed by the plugin. Without it, a keymap that
// simply stopped resolving anything would pass the first row.
func TestSequence_BeatsPluginRawKeysOnFinalChord(t *testing.T) {
	tests := []struct {
		name          string
		withPrefix    bool
		wantForwarded bool
	}{
		{"completed sequence beats the raw_keys claim", true, false},
		{"the same chord alone is still claimed", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := inputOrderTestModel(t, "pane-1", true)
			m.cfg = config.Default()
			m.cfg.Keybindings.ClosePane = "ctrl+b x"
			m.initKeymap()
			givePaneRawKeys(t, m, "pane-1", []string{"x"})

			// Guard the fixture: the plugin must really be claiming x, or the
			// first row passes for the wrong reason.
			if data := m.tryPluginRawKey("x", tea.KeyPressMsg{Code: 'x', Text: "x"}); data == nil {
				t.Fatal("fixture: the plugin does not claim x")
			}

			if tt.withPrefix {
				updated, _ := m.Update(ctrlB())
				got := updated.(Model)
				if len(got.pendingSeq) != 1 {
					t.Fatalf("ctrl+b did not arm the machine: %v", got.pendingSeq)
				}
				*m = got
			}
			_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

			var forwarded bool
			select {
			case <-m.inputCh:
				forwarded = true
			default:
			}
			if forwarded != tt.wantForwarded {
				if tt.wantForwarded {
					t.Error("the plugin's raw_keys claim must still win for a bare chord")
				} else {
					t.Error("the final chord reached the pane — the plugin's raw_keys claim " +
						"beat a completed sequence, so pane.close = \"ctrl+b x\" is dead on this pane")
				}
			}
		})
	}
}

// A three-step sequence completes through Update. MatchSeq is chord-count
// agnostic and unit-tested at three steps, but nothing drove one through the
// real dispatch path.
func TestSequence_ThreeStepCompletesThroughUpdate(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.FocusPane = "ctrl+b w z" })

	before := m.activeTabModel().FocusMode()
	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: 'w'})
	if len(m.pendingSeq) != 2 {
		t.Fatalf("after ctrl+b w, pendingSeq = %v, want two chords armed", m.pendingSeq)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'z'})
	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq not cleared: %v", m.pendingSeq)
	}
	if m.activeTabModel().FocusMode() == before {
		t.Error("the three-step sequence did not fire pane.focus_toggle")
	}
}

// notesSeqModel is seqModel with notes mode open over the active pane.
func notesSeqModel(t *testing.T, mutate func(*Model)) Model {
	t.Helper()
	m := seqModel(t, mutate)
	ne, err := NewNotesEditor(t.TempDir(), "p1", "Shell", 40, 20)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	m.notesMode = true
	m.notesEditor = ne
	m.notesPaneFocused = true // the pane side, where a sequence can reach the machine
	return m
}

// A completed sequence bypasses the notes block, so the four actions that block
// treats specially have to be handled on the sequence path too.
//
// pane.right is the sharp one: run through the ordinary late-tier arm it
// NAVIGATES to another pane, and the next workspace broadcast re-syncs the
// active pane back to the notes-bound one — so the move silently undoes itself.
// Under a vim-style prefix keymap that is the normal way to press it.
func TestSequence_NotesFocusSwitchIsNotPaneNavigation(t *testing.T) {
	m := notesSeqModel(t, func(m *Model) { m.cfg.Keybindings.PaneRight = "ctrl+b l" })

	before := m.curTabs()[0].ActivePane
	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: 'l', Text: "l"})

	if m.curTabs()[0].ActivePane != before {
		t.Errorf("the sequence navigated panes (%s -> %s); in notes mode it must switch FOCUS",
			before, m.curTabs()[0].ActivePane)
	}
	if m.notesPaneFocused {
		t.Error("pane.right must move focus to the editor")
	}
	if !m.notesMode {
		t.Error("a focus switch must not tear down notes mode")
	}
}

// Structural actions destroy or restructure the bound pane, so notes must be
// flushed and torn down before one runs — the teardown the notes block does and
// the sequence path would otherwise skip.
func TestSequence_StructuralActionTearsDownNotes(t *testing.T) {
	m := notesSeqModel(t, func(m *Model) { m.cfg.Keybindings.SplitHorizontal = "ctrl+b %" })

	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: '%', Text: "%"})

	if m.notesMode {
		t.Error("a structural action reached the layout with notes still bound to a pane that moved")
	}
}

// The control: a NON-structural action completing while the pane has focus
// leaves notes open, matching the notes block's own fall-through. Without this
// the test above would pass against a teardown that fires on everything.
func TestSequence_OrdinaryActionLeavesNotesOpen(t *testing.T) {
	m := notesSeqModel(t, func(m *Model) { m.cfg.Keybindings.MutePane = "ctrl+b m" })

	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})

	if !m.notesMode {
		t.Error("an ordinary action must not tear down notes mode")
	}
}

// A daemon broadcast can move the active pane with no keypress at all, and a
// sequence completed afterwards would act on a pane the user never armed it in
// — the hazard the mouse-click cancel covers, by a route no input event sees.
func TestSequence_PaneChangeUnderAnArmedPrefixCancels(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.FocusPane = "ctrl+b z" })

	before := m.activeTabModel().FocusMode()
	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 1 {
		t.Fatalf("setup: ctrl+b did not arm, got %v", m.pendingSeq)
	}

	// What a broadcast does: the active pane moves, with no key involved.
	m.curTabs()[0].ActivePane = "p2"

	m = press(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"})
	if m.activeTabModel().FocusMode() != before {
		t.Error("the sequence completed against a pane it was never armed in")
	}
	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq = %v, want cleared", m.pendingSeq)
	}
}

// A dialog can open with no keypress (a plugin error matched against pane
// output, an upgrade prompt from a resize), and while one is up the view draws
// only the dialog — so the pending indicator is not on screen either.
func TestSequence_DialogCancelsAPendingPrefix(t *testing.T) {
	m := seqModel(t, func(m *Model) { m.cfg.Keybindings.FocusPane = "ctrl+b z" })

	m = press(t, m, ctrlB())
	if len(m.pendingSeq) != 1 {
		t.Fatalf("setup: ctrl+b did not arm, got %v", m.pendingSeq)
	}

	m.dialog = dialogPluginError // as a daemon message would set it
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq = %v — dismissing the dialog left a prefix armed the user "+
			"cannot see, and the next character would complete it", m.pendingSeq)
	}
}
