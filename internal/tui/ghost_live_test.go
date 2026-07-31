package tui

import (
	"strings"
	"testing"
)

// The ghost→live transition must PRESERVE what was replayed, for every pane
// type.
//
// A pane only receives ghost frames when its plugin sets ghost_buffer = true,
// which is the plugin stating that its replayed content is wanted. Resetting on
// the first live frame throws away precisely that content: ResetVT installs a
// fresh emulator, and the emulator's scrollback is where the replayed history
// lives.
//
// The bug this pins was reported from manual testing on 2026-07-31 and had a
// very specific shape — the history was there after reconnecting and vanished
// on the first keystroke, because typing is what makes the child emit the live
// frame that triggered the reset.
//
// Table-driven over the type, and claude-code is in it deliberately: the removed
// branch keyed on that exact string, so a fix that merely moved the hardcode
// somewhere else has to fail here.
func TestGhostToLive_PreservesReplayedScrollback(t *testing.T) {
	types := []string{"claude-code", "opencode", "terminal", "lazygit", ""}

	for _, typ := range types {
		name := typ
		if name == "" {
			name = "(unset)"
		}
		t.Run(name, func(t *testing.T) {
			m := newReconnectTestModel(t, 1)
			p := reconnectTestPanes(m)[0]
			p.Type = typ

			// Enough lines to push earlier ones off a 24-row screen and into
			// scrollback, which is the content the user scrolls back to.
			var replay strings.Builder
			for i := 0; i < 60; i++ {
				replay.WriteString("REPLAYED line\r\n")
			}
			deliverGhost(t, m, p.ID, replay.String())

			before := p.vt.ScrollbackLen()
			if before == 0 {
				t.Fatalf("fixture produced no scrollback, so this test could not detect the reset it exists for")
			}

			// The first live frame — what the child emits when the user types.
			updated, _ := m.Update(PaneOutputMsg{PaneID: p.ID, Data: []byte("live\r\n")})
			*m = updated.(Model)

			if got := p.vt.ScrollbackLen(); got < before {
				t.Errorf("scrollback %d → %d on the first live frame: the replayed history was discarded, "+
					"which is what the user sees as the pane emptying the moment they type", before, got)
			}
			if p.ghost {
				t.Error("pane still marked as showing replayed content after a live frame")
			}
		})
	}
}

// The dimming preference must not decide whether history survives.
//
// Pane.ghost is only set when GhostBuffer.Dimmed is on, so the reset was gated
// on a COSMETIC knob: turning dimming off silently changed whether a reattached
// pane kept its scrollback. Nothing should now depend on that, and this states
// it so a future re-introduction of type-specific reset logic cannot quietly
// bring the coupling back with it.
func TestGhostToLive_ScrollbackSurvivesRegardlessOfDimming(t *testing.T) {
	for _, dimmed := range []bool{true, false} {
		name := "dimmed"
		if !dimmed {
			name = "not dimmed"
		}
		t.Run(name, func(t *testing.T) {
			m := newReconnectTestModel(t, 1)
			m.cfg.GhostBuffer.Dimmed = dimmed
			p := reconnectTestPanes(m)[0]
			p.Type = "claude-code"

			var replay strings.Builder
			for i := 0; i < 60; i++ {
				replay.WriteString("REPLAYED line\r\n")
			}
			deliverGhost(t, m, p.ID, replay.String())

			before := p.vt.ScrollbackLen()
			if before == 0 {
				t.Fatalf("fixture produced no scrollback")
			}

			updated, _ := m.Update(PaneOutputMsg{PaneID: p.ID, Data: []byte("live\r\n")})
			*m = updated.(Model)

			if got := p.vt.ScrollbackLen(); got < before {
				t.Errorf("scrollback %d → %d with dimmed=%v — a display preference decided whether history survived",
					before, got, dimmed)
			}
		})
	}
}
