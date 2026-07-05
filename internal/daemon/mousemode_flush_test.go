package daemon

import (
	"strings"
	"testing"
)

// TestFlushPaneOutput_DetectsMouseTracking drives the real output path
// (flushPaneOutput) with the exact DEC mouse-enable burst opencode emits at
// startup and asserts the daemon records the authoritative mouse-mode state on
// the pane. This is the integration link the TUI relies on when it reattaches
// to an already-running app and cannot reconstruct the state from its own
// emulator. Also asserts the snapshot exposes the flag for the broadcast.
func TestFlushPaneOutput_DetectsMouseTracking(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	pane.PluginMu.Lock()
	pre := pane.MouseModes.tracking()
	pane.PluginMu.Unlock()
	if pre {
		t.Fatal("MouseTracking true before any output")
	}

	// opencode's real startup sequence (captured from `opencode` on macOS).
	d.flushPaneOutput(pane.ID, []byte("\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h"))

	pane.PluginMu.Lock()
	gotTrack, gotSGR := pane.MouseModes.tracking(), pane.MouseModes.sgr
	pane.PluginMu.Unlock()
	if !gotTrack {
		t.Error("MouseTracking = false after opencode mouse-enable burst, want true")
	}
	if !gotSGR {
		t.Error("MouseSGR = false after ?1006, want true")
	}

	// The broadcast snapshot must carry the runtime flags so the TUI sees them.
	state := d.buildWorkspaceState()
	panes, _ := state["panes"].([]map[string]any)
	if len(panes) == 0 {
		t.Fatal("workspace state has no panes")
	}
	var found bool
	for _, p := range panes {
		if p["id"] == pane.ID {
			found = true
			if p["mouse_tracking"] != true {
				t.Errorf("snapshot mouse_tracking = %v, want true", p["mouse_tracking"])
			}
			if p["mouse_sgr"] != true {
				t.Errorf("snapshot mouse_sgr = %v, want true", p["mouse_sgr"])
			}
		}
	}
	if !found {
		t.Fatalf("pane %s not in snapshot", pane.ID)
	}

	// Reset clears it (app exit). The scanned state updates regardless of the
	// broadcast cooldown, so the pane's own flags reflect the reset immediately.
	d.flushPaneOutput(pane.ID, []byte("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l"))
	pane.PluginMu.Lock()
	afterTrack := pane.MouseModes.tracking()
	pane.PluginMu.Unlock()
	if afterTrack {
		t.Error("MouseTracking = true after reset sequence, want false")
	}
}

// TestFlushPaneOutput_PlainOutputDoesNotTrigger guards against false positives:
// ordinary colored output must not flip the mouse-tracking flag.
func TestFlushPaneOutput_PlainOutputDoesNotTrigger(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	d.flushPaneOutput(pane.ID, []byte("\x1b[31mhello\x1b[0m\r\n"+strings.Repeat("x", 500)))
	pane.PluginMu.Lock()
	got := pane.MouseModes.tracking()
	pane.PluginMu.Unlock()
	if got {
		t.Error("MouseTracking = true after plain output, want false")
	}
}
