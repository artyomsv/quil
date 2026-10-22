package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// A split or replace names the pane it is made from, and picks no folder of
// its own for a plugin without a setup step: the daemon opens the new pane
// where that pane is (tmux's rule), falling back to the project root.
func TestSplitAndReplace_SubmitNamesThePaneSplitFrom(t *testing.T) {
	for name, cursor := range map[string]int{"split": 0, "replace": 2} {
		t.Run(name, func(t *testing.T) {
			m := newBranchModel(t)
			f := &fakeSender{}
			m.client = f
			m.selectedPlugin = "terminal"
			m.dialogCursor = cursor
			fromID := m.curTabs()[0].ActivePaneModel().ID

			_, cmd := m.handleCreatePaneSplit()
			runCmd(cmd)

			var payload *ipc.CreatePanePayload
			for _, msg := range f.sent {
				if msg.Type != ipc.MsgCreatePane {
					continue
				}
				var p ipc.CreatePanePayload
				if err := msg.DecodePayload(&p); err != nil {
					t.Fatal(err)
				}
				payload = &p
			}
			if payload == nil {
				t.Fatalf("no create_pane sent; sent %v", sentMsgTypes(f))
			}
			if payload.FromPaneID != fromID {
				t.Errorf("from_pane_id = %q, want the active pane %q", payload.FromPaneID, fromID)
			}
			if payload.CWD != "" {
				t.Errorf("cwd = %q, want none: the daemon picks the folder", payload.CWD)
			}
		})
	}
}
