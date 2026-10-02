package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// A read-only viewer is never eligible for size master, so a daemon whose only
// client it is reports NO master. That must not hand the viewer its own
// geometry: it cannot resize a PTY, so a VT sized to its own box would rewrap
// output the child never redrew. It follows the daemon's size instead. A full
// client in the same position is a master-to-be and keeps sizing its panes to
// its boxes, sending the resize.
func TestReadOnly_NoMasterStillTakesTheDaemonSize(t *testing.T) {
	const daemonCols, daemonRows = 61, 17
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, conn := readOnlyModel(t, rights)
			m.SetClientID("me")
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			clearSent(conn)

			m = roUpdate(t, m, WorkspaceStateMsg{
				Dest: roDest, SizeMaster: "", Clients: 1,
				ActiveProject: "proj-1", ActiveTab: "tab-1",
				Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}, ActiveTab: "tab-1"}},
				Tabs:     []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1"}}},
				Panes: []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal",
					Cols: daemonCols, Rows: daemonRows, SizeSeq: 1}},
			})

			p := paneByID(t, m, "pane-1")
			w, h := vtSize(p)
			atDaemonSize := w == daemonCols && h == daemonRows
			if atDaemonSize != readOnly {
				t.Fatalf("VT %dx%d (box %dx%d): at the daemon's %dx%d = %v, want %v",
					w, h, p.Width, p.Height, daemonCols, daemonRows, atDaemonSize, readOnly)
			}
			if !m.isFollower(roDest) && readOnly {
				t.Fatal("a read-only destination with no master is not a follower")
			}
			// The full client resizes the PTY to its box; the router would drop
			// a viewer's resize anyway, so only the control can show the send.
			if !readOnly && !sentType(conn, ipc.MsgResizePanes) {
				t.Fatal("control: a full client with no master sent no resize_panes")
			}
		})
	}
}
