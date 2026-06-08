package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// conhost coalesces/drops WINDOW_BUFFER_SIZE_EVENTs during rapid resize →
// maximize, so the final WindowSizeMsg can simply never arrive and the TUI
// renders at a stale size until the user presses the redraw key. A periodic
// size poll (tea.RequestWindowSize) closes the gap; the WindowSizeMsg
// handler no-ops when nothing changed so unchanged polls cost nothing.

func TestUpdate_SizePoll_RequestsWindowSizeAndReschedules(t *testing.T) {
	m, _ := cursorTestModel("claude-code")

	_, cmd := m.Update(sizePollMsg{})
	if cmd == nil {
		t.Fatal("size poll produced no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("size poll produced %T, want tea.BatchMsg", cmd())
	}
	if len(batch) != 2 {
		t.Fatalf("size poll batch has %d cmds, want 2 (query + reschedule)", len(batch))
	}
	// First element queries the terminal; second is the next tick (not
	// executed here — it sleeps for the poll interval).
	if got, want := batch[0](), tea.RequestWindowSize(); got != want {
		t.Errorf("batch[0] = %T, want tea.RequestWindowSize message", got)
	}
}

func TestUpdate_WindowSizeMsg_UnchangedSizeIsNoOp(t *testing.T) {
	m := Model{
		cfg:           config.Default(),
		notifications: NewNotificationCenter(30, 50),
		attached:      true,
		width:         80,
		height:        24,
		pendingWidth:  80,
		pendingHeight: 24,
	}

	out, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := out.(Model)
	if cmd != nil {
		t.Error("unchanged WindowSizeMsg scheduled work — poll echoes must be free")
	}
	if got.resizeSeq != m.resizeSeq {
		t.Error("unchanged WindowSizeMsg bumped resizeSeq")
	}
}

func TestUpdate_WindowSizeMsg_ChangedSizeStillDebounces(t *testing.T) {
	m := Model{
		cfg:           config.Default(),
		notifications: NewNotificationCenter(30, 50),
		attached:      true,
		width:         80,
		height:        24,
		pendingWidth:  80,
		pendingHeight: 24,
	}

	out, cmd := m.Update(tea.WindowSizeMsg{Width: 240, Height: 60})
	got := out.(Model)
	if cmd == nil {
		t.Fatal("changed WindowSizeMsg must schedule the debounce tick")
	}
	if got.pendingWidth != 240 || got.pendingHeight != 60 {
		t.Errorf("pending size = %dx%d, want 240x60", got.pendingWidth, got.pendingHeight)
	}
}
