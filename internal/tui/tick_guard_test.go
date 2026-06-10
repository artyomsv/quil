package tui

import "testing"

// TestStartSidebarTick_SingleChain: scheduling must be idempotent while a
// chain is in flight — without the guard every paneEventMsg with the sidebar
// visible stacked a new immortal 10 s chain (one extra full Update+View per
// chain per 10 s, accumulating for the whole session).
func TestStartSidebarTick_SingleChain(t *testing.T) {
	m := Model{}
	if cmd := m.startSidebarTick(); cmd == nil {
		t.Fatal("first startSidebarTick returned nil — chain never starts")
	}
	if cmd := m.startSidebarTick(); cmd != nil {
		t.Error("second startSidebarTick started a duplicate chain")
	}
	// Chain decided not to reschedule (sidebar hidden) → flag clears →
	// a new chain may start.
	m.sidebarTickRunning = false
	if cmd := m.startSidebarTick(); cmd == nil {
		t.Error("startSidebarTick after chain end returned nil — sidebar refresh dead")
	}
}

func TestStartNotesTick_SingleChain(t *testing.T) {
	m := Model{}
	if cmd := m.startNotesTick(); cmd == nil {
		t.Fatal("first startNotesTick returned nil")
	}
	if cmd := m.startNotesTick(); cmd != nil {
		t.Error("second startNotesTick started a duplicate chain")
	}
	// Chain decided not to reschedule (notes mode exited) → flag clears →
	// a new chain may start on the next notes-mode entry.
	m.notesTickRunning = false
	if cmd := m.startNotesTick(); cmd == nil {
		t.Error("startNotesTick after chain end returned nil — notes auto-save dead")
	}
}
