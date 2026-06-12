package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// runCycle invokes cycleTabColor on a single-tab model with the given
// starting color and returns the decoded IPC payload the command sent.
func runCycle(t *testing.T, startColor string) (*TabModel, ipc.UpdateTabPayload) {
	t.Helper()

	fake := &fakeSender{}
	m := Model{activeTab: 0, client: fake}
	m.tabs = []*TabModel{{ID: "tab-1", Name: "Shell", Color: startColor}}

	cmd := m.cycleTabColor()
	if cmd == nil {
		t.Fatal("cycleTabColor returned nil Cmd")
	}
	cmd()

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	var payload ipc.UpdateTabPayload
	if err := fake.sent[0].DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	return m.tabs[0], payload
}

func TestCycleTabColor_AdvancesToNextColor(t *testing.T) {
	tab, payload := runCycle(t, "") // default → first palette color

	if tab.Color != tabColors[1] {
		t.Errorf("tab.Color = %q, want %q", tab.Color, tabColors[1])
	}
	if payload.Color != tabColors[1] || payload.ClearColor {
		t.Errorf("payload = {Color: %q, ClearColor: %v}, want {Color: %q, ClearColor: false}",
			payload.Color, payload.ClearColor, tabColors[1])
	}
}

// TestCycleTabColor_WrapsToDefault guards the loop-around: cycling past the
// last palette color must return to the default (empty) color, and the IPC
// payload must carry ClearColor so the daemon actually clears it instead of
// treating the empty color as "no change".
func TestCycleTabColor_WrapsToDefault(t *testing.T) {
	last := tabColors[len(tabColors)-1]
	tab, payload := runCycle(t, last)

	if tab.Color != "" {
		t.Errorf("tab.Color = %q, want empty (default)", tab.Color)
	}
	if payload.Color != "" || !payload.ClearColor {
		t.Errorf("payload = {Color: %q, ClearColor: %v}, want {Color: \"\", ClearColor: true}",
			payload.Color, payload.ClearColor)
	}
}
