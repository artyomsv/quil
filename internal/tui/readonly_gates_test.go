package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// The read-only gates readonly_test.go did not reach. Each is driven through
// Update against read-only and full; full is the control that proves the
// path reaches the gate. Where every Update path is refused EARLIER (a menu
// row checked again at execute time), the gate is called directly and the
// test says so: an Update-driven test there passes with the gate deleted.

// otherTabModel is readOnlyModel with pane-9 in tab-2, sized: every jump to
// pane-9 leaves the tab a viewer's daemon has active.
func otherTabModel(t *testing.T, rights string) (Model, *fakeConn, *PaneModel) {
	t.Helper()
	m, conn := readOnlyModel(t, rights)
	p9 := NewPaneModel("pane-9", testRingBufSize)
	t.Cleanup(p9.Dispose)
	tab2 := m.projects[0].tabs[1]
	tab2.Root, tab2.ActivePane = NewLeaf(p9), "pane-9"
	m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
	clearSent(conn)
	return m, conn, p9
}

// Every other caller of leavesViewerTab: the attention queue, MCP's
// set_active_pane, pane history back and the palette's Go to pane. Each
// refuses with the flash and leaves the viewer on the daemon's tab, with its
// own state as it was (the history entry kept, the Active flag kept).
//
// set_active_pane's own gate sits in front of jumpToPane's, which refuses the
// same jump, so that case fails only with both gates deleted.
func TestReadOnly_JumpsOffTheViewerTabRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drive func(t *testing.T, m Model, p9 *PaneModel) Model
		check func(t *testing.T, m Model, readOnly bool)
	}{
		{
			name: "attention queue",
			drive: func(t *testing.T, m Model, p9 *PaneModel) Model {
				p9.blockedSince = time.Now()
				return roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt | tea.ModShift})
			},
		},
		{
			name: "set_active_pane",
			drive: func(t *testing.T, m Model, _ *PaneModel) Model {
				return roUpdate(t, m, setActivePaneMsg{PaneID: "pane-9"})
			},
		},
		{
			name: "history back",
			drive: func(t *testing.T, m Model, _ *PaneModel) Model {
				m.paneHistory = []PaneRef{{ProjectID: "proj-1", TabIndex: 1, PaneID: "pane-9"}}
				return roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt})
			},
			check: func(t *testing.T, m Model, readOnly bool) {
				if readOnly && len(m.paneHistory) != 1 {
					t.Fatalf("a refused back left %d history entries, want the entry kept", len(m.paneHistory))
				}
			},
		},
		{
			name: "palette go to pane",
			drive: func(t *testing.T, m Model, _ *PaneModel) Model {
				m.projects[0].tabs[0].Root.Pane.Active = true
				m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
				if m.dialog != dialogCommandPalette {
					t.Fatal("setup: the palette did not open")
				}
				row := -1
				for i, c := range m.paletteDisplay() {
					if c.action == palActGoToPane && c.arg == "pane-9" {
						row = i
					}
				}
				if row < 0 {
					t.Fatal("setup: no Go to pane row for pane-9")
				}
				m.palette.cursor = row
				return roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			},
			check: func(t *testing.T, m Model, readOnly bool) {
				// goToPane clears the old pane's Active flag before the jump;
				// a refused jump must not leave that write behind.
				if readOnly && !m.projects[0].tabs[0].Root.Pane.Active {
					t.Fatal("a refused jump cleared pane-1's Active flag")
				}
			},
		},
	} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(tc.name+"/"+rights, func(t *testing.T) {
				readOnly := rights == ipc.RightsReadOnly
				m, conn, p9 := otherTabModel(t, rights)
				m = tc.drive(t, m, p9)
				if moved := m.activeTabModel().ID == "tab-2"; moved == readOnly {
					t.Fatalf("jumped to tab-2 = %v on rights %q", moved, rights)
				}
				if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
					t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
				}
				if readOnly {
					if got := actSent(conn); len(got) != 0 {
						t.Fatalf("a refused jump sent %v", got)
					}
				}
				if tc.check != nil {
					tc.check(t, m, readOnly)
				}
			})
		}
	}
}

// d and D in the notification sidebar dismiss locally and tell the daemon; a
// viewer would drop events every other client still shows. Refused before the
// local removal.
func TestReadOnly_NotificationDismissRefused(t *testing.T) {
	for _, key := range []rune{'d', 'D'} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(string(key)+"/"+rights, func(t *testing.T) {
				readOnly := rights == ipc.RightsReadOnly
				m, conn, _ := otherTabModel(t, rights)
				m.notifications.visible = true
				m.notifications.AddEvent(ipc.PaneEventPayload{ID: "e1", PaneID: "pane-9", TabID: "tab-2",
					Type: "command_complete", Title: "done", Severity: "info"})
				m.sidebarFocused = true
				m = roUpdate(t, m, tea.KeyPressMsg{Code: key, Text: string(key)})
				if kept := m.notifications.Count() == 1; kept != readOnly {
					t.Fatalf("event kept = %v after %q on rights %q", kept, key, rights)
				}
				if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
					t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
				}
				if readOnly && sentType(conn, ipc.MsgDismissEvent) {
					t.Fatal("a viewer's dismissal reached the daemon")
				}
			})
		}
	}
}

// A press on a project row or a tab row in the project sidebar arms a
// reorder drag — never for a read-only destination, whose order is its
// daemon's.
func TestReadOnly_SidebarPressArmsNoDrag(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, _ := readOnlyModel(t, rights)
			m.sidebarOpen = true
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})

			y := sidebarRowY(t, m, sidebarRowProject)
			m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
			if armed := m.projectDragging; armed == readOnly {
				t.Fatalf("project drag armed = %v on rights %q", armed, rights)
			}
			m = roUpdate(t, m, tea.MouseReleaseMsg{X: 1, Y: y, Button: tea.MouseLeft})

			y = sidebarRowY(t, m, sidebarRowTab)
			m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
			if armed := m.sidebarTabDragging; armed == readOnly {
				t.Fatalf("sidebar tab drag armed = %v on rights %q", armed, rights)
			}
		})
	}
}

// A tab drag armed while the destination was full, whose rights then turn
// read-only before the pointer moves: the motion reorders nothing, flashes
// and ends the drag. Both the sidebar tab drag and the tab-bar drag.
func TestReadOnly_TabDragDowngradedMidDragReordersNothing(t *testing.T) {
	for _, where := range []string{"sidebar", "tab bar"} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(where+"/"+rights, func(t *testing.T) {
				readOnly := rights == ipc.RightsReadOnly
				m, conn := readOnlyModel(t, ipc.RightsFull)
				p9 := NewPaneModel("pane-9", testRingBufSize)
				t.Cleanup(p9.Dispose)
				tab2 := m.projects[0].tabs[1]
				tab2.Root, tab2.ActivePane = NewLeaf(p9), "pane-9"
				m.sidebarOpen = true
				m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})

				var motion tea.MouseMotionMsg
				if where == "sidebar" {
					m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowTab), Button: tea.MouseLeft})
					if !m.sidebarTabDragging {
						t.Fatal("setup: the sidebar tab drag did not arm")
					}
					// The LAST row of tab-2's group: past its middle.
					ty := -1
					for y, r := range m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight()) {
						if r.inTab && r.tabIdx == 1 {
							ty = y
						}
					}
					if ty < 0 {
						t.Fatal("setup: no sidebar row for tab-2")
					}
					motion = tea.MouseMotionMsg{X: 1, Y: ty, Button: tea.MouseLeft}
				} else {
					spans := m.tabSpans()
					off := m.projectSidebarWidth()
					m = roUpdate(t, m, tea.MouseClickMsg{X: off + spans[0].start + 1, Y: 0, Button: tea.MouseLeft})
					if m.tabDragFromIdx != 0 {
						t.Fatal("setup: the tab-bar drag did not arm")
					}
					motion = tea.MouseMotionMsg{X: off + spans[1].start + spans[1].width - 1, Y: 0, Button: tea.MouseLeft}
				}
				m.SetDestRights(roDest, rights)
				clearSent(conn)
				m = roUpdate(t, m, motion)
				if moved := m.curTabs()[0].ID != "tab-1"; moved == readOnly {
					t.Fatalf("tab moved = %v on rights %q", moved, rights)
				}
				if readOnly {
					if m.flashText != readOnlyFlash {
						t.Fatalf("flash = %q, want the read-only flash", m.flashText)
					}
					if m.sidebarTabDragging || m.tabDragFromIdx >= 0 {
						t.Fatal("the refused drag is still armed")
					}
					if sentType(conn, ipc.MsgReorderTab) {
						t.Fatal("a refused drag sent a reorder")
					}
				}
			})
		}
	}
}

// finishProjectDrag's own gate: the press never arms a viewer's drag, so this
// is reached by a drag armed while the destination was full and released
// after its rights came back read-only (a reconnect re-applies them).
func TestReadOnly_ProjectDragReleaseRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir()) // a regroup saves groups.json
			readOnly := rights == ipc.RightsReadOnly
			m, _ := readOnlyModel(t, ipc.RightsFull)
			m.groups = grpFromNames("G")
			m.sidebarOpen = true
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})

			m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseLeft})
			gy := sidebarRowY(t, m, sidebarRowGroup)
			m = roUpdate(t, m, tea.MouseMotionMsg{X: 1, Y: gy, Button: tea.MouseLeft})
			if !m.projectDragging || m.projectDrop.group != "G" {
				t.Fatalf("setup: drag=%v drop=%+v, want a drag over G", m.projectDragging, m.projectDrop)
			}
			m.SetDestRights(roDest, rights)
			m = roUpdate(t, m, tea.MouseReleaseMsg{X: 1, Y: gy, Button: tea.MouseLeft})
			if joined := grpMembers(m.groups, 0) == roDest+"/proj-1"; joined == readOnly {
				t.Fatalf("project joined G = %v on rights %q (members %q)", joined, rights, grpMembers(m.groups, 0))
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
			}
		})
	}
}

// commitGroupEdit's own gate: the menus never open the name editor for a
// viewer, so the editor is open when the rights turn read-only. Enter then
// closes it and changes nothing — for a new group holding the project and for
// a rename of a group holding it.
func TestReadOnly_GroupEditCommitRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  groupEditState
		typed string
		after string // group 0's name on success
	}{
		{"new", groupEditState{mode: groupEditNew, dest: roDest, projectID: "proj-1"}, "N", "N"},
		{"rename", groupEditState{mode: groupEditRename, target: "G", input: "G"}, "2", "G2"},
	} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(tc.name+"/"+rights, func(t *testing.T) {
				t.Setenv("QUIL_HOME", t.TempDir()) // a commit saves groups.json
				readOnly := rights == ipc.RightsReadOnly
				m, _ := readOnlyModel(t, ipc.RightsFull)
				if tc.edit.mode == groupEditRename {
					m.groups = projectGroups{Groups: []projectGroup{
						{Name: "G", Members: []groupMember{{Dest: roDest, ID: "proj-1"}}},
					}}
				}
				m.beginGroupEdit(tc.edit)
				m.SetDestRights(roDest, rights)
				for _, r := range tc.typed {
					m = roUpdate(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
				}
				m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
				changed := len(m.groups.Groups) > 0 && m.groups.Groups[0].Name == tc.after
				if changed == readOnly {
					t.Fatalf("groups changed = %v on rights %q (%+v)", changed, rights, m.groups.Groups)
				}
				if readOnly {
					if m.dialog == dialogGroupName {
						t.Fatal("a refused commit left the name editor open")
					}
					if m.flashText != readOnlyFlash {
						t.Fatalf("flash = %q, want the read-only flash", m.flashText)
					}
				}
			})
		}
	}
}

// The new-project form opens for any destination, so its submit is the gate:
// on a read-only destination it says so in the form and sends nothing.
func TestReadOnly_NewProjectSubmitRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, conn := readOnlyModel(t, rights)
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModAlt | tea.ModShift})
			if m.dialog != dialogProjectNew || m.projectFormDest != roDest {
				t.Fatalf("setup: dialog=%v dest=%q, want the new-project form for %s", m.dialog, m.projectFormDest, roDest)
			}
			m = roUpdate(t, m,
				tea.KeyPressMsg{Code: 'x', Text: "x"},
				tea.KeyPressMsg{Code: tea.KeyEnter},
			)
			if refused := m.projectFormErr == readOnlyFlash; refused != readOnly {
				t.Fatalf("form error = %q on rights %q", m.projectFormErr, rights)
			}
			if readOnly && sentType(conn, ipc.MsgCreateProject) {
				t.Fatal("a viewer's new project reached the daemon")
			}
		})
	}
}

// aboutCursorOn reports whether the About dialog's selected ("> ") row reads
// label.
func aboutCursorOn(m Model, label string) bool {
	for _, line := range strings.Split(m.renderAboutDialog(), "\n") {
		if strings.HasPrefix(stripANSI(line), "> ") {
			return strings.Contains(stripANSI(line), label)
		}
	}
	return false
}

// Keys and an About row that open something only an acting client may use:
// input history, the restart confirm, F1 → Processes, and take control (bound
// here; it ships unbound).
func TestReadOnly_ActingDialogsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		drive  func(t *testing.T, m Model) Model
		opened func(m Model, conn *fakeConn) bool
	}{
		{
			name: "input history",
			drive: func(t *testing.T, m Model) Model {
				return roUpdate(t, m, tea.KeyPressMsg{Code: 'i', Mod: tea.ModAlt | tea.ModShift})
			},
			opened: func(m Model, _ *fakeConn) bool { return m.dialog == dialogCommandHistory },
		},
		{
			name: "restart",
			drive: func(t *testing.T, m Model) Model {
				return roUpdate(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt})
			},
			opened: func(m Model, _ *fakeConn) bool {
				return m.dialog == dialogConfirm && m.confirmKind == confirmKindRestartPane
			},
		},
		{
			name: "F1 processes",
			drive: func(t *testing.T, m Model) Model {
				m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyF1})
				if m.dialog != dialogAbout {
					t.Fatal("setup: F1 did not open About")
				}
				// Found by its label, walking the cursor down the rendered
				// dialog, so a reordered About list moves the test with it.
				for i := 0; !aboutCursorOn(m, "Processes"); i++ {
					if i > aboutStopDaemonIndex {
						t.Fatal("setup: no Processes row in About")
					}
					m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
				}
				return roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			},
			opened: func(m Model, _ *fakeConn) bool { return m.dialog == dialogProcesses },
		},
		{
			name: "take control",
			drive: func(t *testing.T, m Model) Model {
				press := layBind(t, &m, "client.take_control")
				return roUpdate(t, m, press)
			},
			opened: func(_ Model, conn *fakeConn) bool { return sentType(conn, ipc.MsgTakeControl) },
		},
	} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(tc.name+"/"+rights, func(t *testing.T) {
				t.Setenv("QUIL_HOME", t.TempDir())
				readOnly := rights == ipc.RightsReadOnly
				m, conn := readOnlyModel(t, rights)
				m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
				clearSent(conn)
				m = tc.drive(t, m)
				if opened := tc.opened(m, conn); opened == readOnly {
					t.Fatalf("took effect = %v on rights %q", opened, rights)
				}
				if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
					t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
				}
			})
		}
	}
}

// A palette row is greyed for a viewer when the palette is BUILT, so a row
// built while the destination was full is still live after its rights turn
// read-only; the handler behind it is then the gate. New from template and
// Rename project.
func TestReadOnly_PaletteRowBuiltBeforeRightsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action paletteAction
		arg    string
		dialog dialogScreen
	}{
		{"new from template", palActNewTemplate, "", dialogNewTemplate},
		{"rename project", palActRenameProject, "proj-1", dialogProjectRename},
	} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(tc.name+"/"+rights, func(t *testing.T) {
				t.Setenv("QUIL_HOME", t.TempDir()) // templates.toml is read on open
				readOnly := rights == ipc.RightsReadOnly
				m, _ := readOnlyModel(t, ipc.RightsFull)
				m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
				m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
				if m.dialog != dialogCommandPalette {
					t.Fatal("setup: the palette did not open")
				}
				row := -1
				for i, c := range m.paletteDisplay() {
					if c.action == tc.action && c.arg == tc.arg && c.enabled {
						row = i
					}
				}
				if row < 0 {
					t.Fatalf("setup: no live %s row", tc.name)
				}
				m.SetDestRights(roDest, rights)
				m.palette.cursor = row
				m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
				if opened := m.dialog == tc.dialog; opened == readOnly {
					t.Fatalf("dialog opened = %v on rights %q", opened, rights)
				}
				if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
					t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
				}
			})
		}
	}
}

// Ungroup and the two move pickers are reached only from context-menu rows,
// and executeCtxMenuItem re-checks readOnlyGreyedItems against the rights at
// execute time — ahead of these gates, on every Update path. A test driven
// through Update therefore passes with these deleted, so they are called
// directly: they are the line that holds if that menu check regresses.
func TestReadOnly_MenuOnlyGatesRefuse(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir()) // an ungroup saves groups.json
			readOnly := rights == ipc.RightsReadOnly
			m, _ := readOnlyModel(t, rights)
			m.groups = projectGroups{Groups: []projectGroup{
				{Name: "G", Members: []groupMember{{Dest: roDest, ID: "proj-1"}}},
			}}

			m.ungroupProject(roDest, "proj-1")
			if left := grpMembers(m.groups, 0) == ""; left == readOnly {
				t.Fatalf("ungrouped = %v on rights %q", left, rights)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("ungroup: read-only flash = %v (flash %q)", flashed, m.flashText)
			}

			m.flashText = ""
			next, _ := m.openMoveTabPicker("tab-1")
			if opened := next.(Model).dialog == dialogProjectPick; opened == readOnly {
				t.Fatalf("move-tab picker opened = %v on rights %q", opened, rights)
			}
			if flashed := next.(Model).flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("move tab: read-only flash = %v (flash %q)", flashed, next.(Model).flashText)
			}

			next, _ = m.openMovePanePicker("pane-1")
			if opened := next.(Model).dialog == dialogTabPick; opened == readOnly {
				t.Fatalf("move-pane picker opened = %v on rights %q", opened, rights)
			}
			if flashed := next.(Model).flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("move pane: read-only flash = %v (flash %q)", flashed, next.(Model).flashText)
			}
		})
	}
}
