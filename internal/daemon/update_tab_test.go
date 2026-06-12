package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestHandleUpdateTab_ColorTransitions covers the color half of
// handleUpdateTab, in particular the ClearColor flag that lets the tab-color
// cycle wrap from the last palette color back to the default. Before the
// flag existed, an empty color sent together with a name was treated as "no
// change", so the cycle appeared stuck on the last color.
func TestHandleUpdateTab_ColorTransitions(t *testing.T) {
	cases := []struct {
		name      string
		initial   string
		payload   ipc.UpdateTabPayload
		wantColor string
	}{
		{
			name:      "set color",
			initial:   "",
			payload:   ipc.UpdateTabPayload{Name: "Shell", Color: "1"},
			wantColor: "1",
		},
		{
			name:      "rename keeps existing color",
			initial:   "208",
			payload:   ipc.UpdateTabPayload{Name: "Build"},
			wantColor: "208",
		},
		{
			name:      "cycle wrap clears color via ClearColor despite name present",
			initial:   "208",
			payload:   ipc.UpdateTabPayload{Name: "Shell", ClearColor: true},
			wantColor: "",
		},
		{
			name:      "legacy clear: empty name and empty color",
			initial:   "4",
			payload:   ipc.UpdateTabPayload{},
			wantColor: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New(config.Default())
			tab := d.session.CreateTab("Shell")
			tab.Color = tc.initial
			tc.payload.TabID = tab.ID

			msg, err := ipc.NewMessage(ipc.MsgUpdateTab, tc.payload)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			d.handleUpdateTab(msg)

			if tab.Color != tc.wantColor {
				t.Errorf("tab.Color = %q, want %q", tab.Color, tc.wantColor)
			}
		})
	}
}
