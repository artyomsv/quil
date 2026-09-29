package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// sharedFrame is stateMsg plus the 3b fields: the daemon's group list and one
// project's group. dest "" unless overridden by the caller.
func sharedFrame(runID string, rev uint64, projectID, group string, groups ...string) WorkspaceStateMsg {
	f := stateMsg(runID, rev, "tab-"+projectID)
	f.SharedData = true
	f.Groups = groups
	f.Projects = []ProjectInfo{{ID: projectID, Name: projectID, Group: group, TabIDs: []string{"tab-" + projectID}}}
	f.ActiveProject = projectID
	return f
}

// twoDestModel routes "" and "hostA" to two fake conns so destConnected
// admits both (the shape dialdest_race_test.go uses).
func twoDestModel(t *testing.T) (Model, *fakeConn, *fakeConn) {
	t.Helper()
	local, remote := newFakeConn(), newFakeConn()
	close(local.recv)
	close(remote.recv)
	m := Model{cfg: config.Default(), client: NewRouter(map[string]Client{"": local, "hostA": remote}), tabDragFromIdx: -1}
	return m, local, remote
}

func groupNames(m Model) []string {
	out := make([]string, 0, len(m.groups.Groups))
	for _, g := range m.groups.Groups {
		out = append(out, g.Name)
	}
	return out
}

func TestUpdate_SharedFrame_BuildsMergedGroupsFromDaemonMembers(t *testing.T) {
	m := connectedTestModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "Infra", "Infra", "Empty"))
	if got := groupNames(m); len(got) != 2 || got[0] != "Infra" || got[1] != "Empty" {
		t.Fatalf("groups = %v", got)
	}
	if g := m.groups.groupOf("", "proj-1"); g != 0 {
		t.Errorf("proj-1 in group %d, want 0 (Infra)", g)
	}
	if !m.sharedData[""] {
		t.Error("local destination not marked shared")
	}
	// An unchanged frame changes nothing the file holds, so it asks for no
	// save: the git ticker alone sends one every 5 s.
	seq := m.groupsSeq
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "Infra", "Infra", "Empty"))
	if m.groupsSeq != seq {
		t.Errorf("an unchanged frame saved the groups (seq %d -> %d)", seq, m.groupsSeq)
	}
}

// The file is a CACHE of an authoritative destination's members: an assign
// made in another client (the frame moves a project's Group) replaces that
// destination's members in the saved file, and nothing else in it.
func TestUpdate_AuthoritativeDest_FrameReplacesItsMembersInTheFile(t *testing.T) {
	m := connectedTestModel(t)
	path := t.TempDir() + "/project-groups.json"
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{
		{Name: "A", Members: []groupMember{{Dest: "", ID: "proj-1"}, {Dest: "hostA", ID: "proj-far"}}},
		{Name: "B"},
	}}}, path)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "A", "A", "B"))
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "B", "A", "B"))
	saved, err := loadProjectGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.groupOf("", "proj-1") != saved.indexOf("B") {
		t.Errorf("file members = %+v, want proj-1 moved to B", saved.Groups)
	}
	if saved.groupOf("hostA", "proj-far") != saved.indexOf("A") {
		t.Errorf("another destination's cached member was touched: %+v", saved.Groups)
	}
	// An unchanged frame writes nothing.
	seq := m.groupsSeq
	m = updateWith(t, m, sharedFrame("r", 3, "proj-1", "B", "A", "B"))
	if m.groupsSeq != seq {
		t.Errorf("an unchanged frame saved (seq %d -> %d)", seq, m.groupsSeq)
	}
}

func TestUpdate_TwoDestinations_SameNameDifferentCase_OneMergedGroup(t *testing.T) {
	m, _, _ := twoDestModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "infra", "infra"))
	remote := sharedFrame("q", 1, "proj-1", "INFRA", "INFRA")
	remote.Dest = "hostA"
	m = updateWith(t, m, remote)
	if got := groupNames(m); len(got) != 1 {
		t.Fatalf("groups = %v, want one merged group", got)
	}
	if m.groups.groupOf("", "proj-1") != 0 || m.groups.groupOf("hostA", "proj-1") != 0 {
		t.Errorf("members not merged: %+v", m.groups.Groups[0].Members)
	}
}

// Review focus 5.
func TestUpdate_MixedDestinations_LegacyMembersStayInFileSharedComeFromFrame(t *testing.T) {
	m, _, _ := twoDestModel(t)
	dir := t.TempDir()
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{
		{Name: "Infra", Members: []groupMember{{Dest: "hostA", ID: "proj-old"}, {Dest: "", ID: "proj-stale"}}},
	}}}, dir+"/project-groups.json")
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "Infra", "Infra"))
	legacy := stateMsg("", 0, "tab-old") // no shared_data: hostA is a legacy daemon
	legacy.Dest = "hostA"
	legacy.Projects = []ProjectInfo{{ID: "proj-old", Name: "old", TabIDs: []string{"tab-old"}}}
	m = updateWith(t, m, legacy)
	grp := m.groups.Groups[m.groups.indexOf("Infra")]
	if m.groups.groupOf("hostA", "proj-old") < 0 || m.groups.groupOf("", "proj-1") < 0 {
		t.Errorf("merged members = %+v", grp.Members)
	}
	if m.groups.groupOf("", "proj-stale") >= 0 {
		t.Error("a shared destination's file member survived the frame")
	}
	// The file keeps the legacy member and caches the authoritative
	// destination's frame membership in place of its stale entry.
	runCmd(m.saveGroupsCmd())
	saved, err := loadProjectGroups(dir + "/project-groups.json")
	if err != nil {
		t.Fatal(err)
	}
	if saved.groupOf("hostA", "proj-old") < 0 || saved.groupOf("", "proj-1") < 0 || saved.groupOf("", "proj-stale") >= 0 {
		t.Errorf("file members = %+v", saved.Groups)
	}
}

func TestUpdate_LegacyDestinationOnly_BehavesAsToday(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{{Name: "Kept"}}}}, "")
	m = updateWith(t, m, stateMsg("", 0, "tab-a"))
	if got := groupNames(m); len(got) != 1 || got[0] != "Kept" {
		t.Errorf("an empty group was pruned with no shared destination: %v", got)
	}
	runCmd(m.moveProjectToGroup("", "proj-1", "Kept"))
	if n := countSent(conn, ipc.MsgSetProjectGroup); n != 0 {
		t.Errorf("set_project_group sent to a legacy daemon %d times", n)
	}
}

func TestUpdate_SharedFrame_DaemonNameUnknownToFileIsAppended(t *testing.T) {
	m := connectedTestModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{{Name: "First"}}}}, "")
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "New", "First"))
	if got := groupNames(m); len(got) != 2 || got[0] != "First" || got[1] != "New" {
		t.Errorf("groups = %v, want file order kept and New appended", got)
	}
}

// A name no daemon ever listed is never deleted — not on a destination's
// first frame, not on a later one: only a name that DISAPPEARS goes (I-1).
func TestUpdate_SharedLocal_EmptyFileGroupNobodyListedIsKept(t *testing.T) {
	m := connectedTestModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{{Name: "Kept"}}}}, "")
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Listed"))
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "", "Listed"))
	if got := groupNames(m); len(got) != 2 || got[0] != "Kept" || got[1] != "Listed" {
		t.Errorf("groups = %v, want Kept kept and Listed appended", got)
	}
}

// Test 1 (C-1): a shared daemon holding no groups yet has not been imported
// into, so the file's members for it are the only record — shown and saved.
func TestUpdate_SharedDaemonWithNoGroups_FileMembersStay(t *testing.T) {
	m := connectedTestModel(t)
	path := t.TempDir() + "/project-groups.json"
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{
		{Name: "G", Members: []groupMember{{Dest: "", ID: "proj-1"}}},
	}}}, path)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "")) // shared, lists no groups
	if m.groups.groupOf("", "proj-1") != m.groups.indexOf("G") || m.groups.indexOf("G") < 0 {
		t.Errorf("displayed groups = %+v, want proj-1 still in G", m.groups.Groups)
	}
	runCmd(m.saveGroupsCmd())
	saved, err := loadProjectGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.indexOf("G") < 0 || saved.groupOf("", "proj-1") != saved.indexOf("G") {
		t.Errorf("file = %+v, want G holding proj-1", saved.Groups)
	}
}

// Test 2 (D-1a/D-5): order and collapsed state survive a launch. "GPU" is
// first and collapsed with members only on hostA; the local frame arrives
// first, then hostA's listing GPU.
func TestUpdate_FileOrderAndCollapsedSurviveTheFirstFrames(t *testing.T) {
	m, _, _ := twoDestModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{
		{Name: "GPU", Collapsed: true, Members: []groupMember{{Dest: "hostA", ID: "proj-2"}}},
		{Name: "Local"},
	}}}, "")
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "Local", "Local"))
	rf := sharedFrame("q", 1, "proj-2", "GPU", "GPU")
	rf.Dest = "hostA"
	m = updateWith(t, m, rf)
	if got := groupNames(m); len(got) != 2 || got[0] != "GPU" || got[1] != "Local" {
		t.Fatalf("groups = %v, want GPU still first", got)
	}
	if !m.groups.Groups[0].Collapsed {
		t.Error("GPU lost its collapsed state")
	}
	if m.groups.groupOf("hostA", "proj-2") != 0 || m.groups.groupOf("", "proj-1") != 1 {
		t.Errorf("members = %+v", m.groups.Groups)
	}
}

// Test 4 (I-1, TC-5): a delete made in another client shows here — hostA
// listed "X" in its previous frame, drops it now, and X has no member.
func TestUpdate_GroupNameDisappears_GroupIsRemoved(t *testing.T) {
	m, _, _ := twoDestModel(t)
	f1 := sharedFrame("q", 1, "proj-2", "Y", "X", "Y")
	f1.Dest = "hostA"
	m = updateWith(t, m, f1)
	if got := groupNames(m); len(got) != 2 {
		t.Fatalf("setup: groups = %v", got)
	}
	f2 := sharedFrame("q", 2, "proj-2", "Y", "Y")
	f2.Dest = "hostA"
	m = updateWith(t, m, f2)
	if got := groupNames(m); len(got) != 1 || got[0] != "Y" {
		t.Errorf("groups = %v, want X removed", got)
	}
}

// A vanished name that still has a member anywhere is kept: here a legacy
// destination's cached member.
func TestUpdate_GroupNameDisappears_KeptWhileItHasMembers(t *testing.T) {
	m, _, _ := twoDestModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{
		{Name: "X", Members: []groupMember{{Dest: "hostA", ID: "proj-old"}}},
	}}}, "")
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "X", "Y"))
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "", "Y"))
	if m.groups.indexOf("X") < 0 || m.groups.groupOf("hostA", "proj-old") < 0 {
		t.Errorf("groups = %+v, want X kept with its legacy member", m.groups.Groups)
	}
}

// An in-flight frame during an optimistic rename still lists the OLD name;
// the new one was never listed, so it cannot vanish, and it keeps its slot.
func TestUpdate_OptimisticRename_InFlightFrameKeepsTheNewNamesSlot(t *testing.T) {
	m := connectedTestModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "A", "X", "A", "Y"))
	m.beginGroupEdit(groupEditState{mode: groupEditRename, target: "A", input: "B"})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	m = out.(Model)
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "A", "X", "A", "Y")) // in flight
	m = updateWith(t, m, sharedFrame("r", 3, "proj-1", "B", "X", "B", "Y"))
	if got := groupNames(m); len(got) != 3 || got[0] != "X" || got[1] != "B" || got[2] != "Y" {
		t.Errorf("groups = %v, want X,B,Y", got)
	}
	if m.groups.groupOf("", "proj-1") != 1 {
		t.Errorf("proj-1 in group %d, want 1 (B)", m.groups.groupOf("", "proj-1"))
	}
}

func TestMoveProjectToGroup_SharedDest_SendsIDBearingSetProjectGroup(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	runCmd(m.moveProjectToGroup("", "proj-1", "Infra"))
	if n := countSent(conn, ipc.MsgSetProjectGroup); n != 1 {
		t.Fatalf("set_project_group sent %d times", n)
	}
	conn.mu.Lock()
	sent := conn.sent[len(conn.sent)-1]
	conn.mu.Unlock()
	var p ipc.SetProjectGroupPayload
	if err := sent.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if sent.ID == "" || p.ProjectID != "proj-1" || p.Group != "Infra" {
		t.Errorf("sent %+v id=%q", p, sent.ID)
	}
	if m.groups.groupOf("", "proj-1") < 0 {
		t.Error("no optimistic local assign")
	}
}

func TestUpdate_GroupOpRefusal_FlashesTheHost(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	runCmd(m.sendGroupOpEverywhere(ipc.GroupOpDelete, "Infra", ""))
	conn.mu.Lock()
	id := conn.sent[len(conn.sent)-1].ID
	conn.mu.Unlock()
	// The same id from a DIFFERENT host settles nothing.
	m = updateWith(t, m, sharedOpRespMsg{dest: "hostA", id: id, resp: ipc.OpRespPayload{OK: false, Error: "delete: forged"}})
	if m.flashText != "" {
		t.Errorf("another host's answer flashed %q", m.flashText)
	}
	m = updateWith(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: false, Error: "delete: no such group"}})
	// hostLabel names the local daemon "this machine" (project.go).
	if !strings.Contains(m.flashText, hostLabel("")) || !strings.Contains(m.flashText, "no such group") {
		t.Errorf("flash = %q", m.flashText)
	}
}

// The answer reaches Update through the real listen loop: a group_op_resp
// read off the conn becomes a sharedOpRespMsg carrying the request's id.
func TestListenForMessages_GroupOpResp_BecomesSharedOpResp(t *testing.T) {
	conn := newFakeConn()
	m := Model{cfg: config.Default(), client: conn, tabDragFromIdx: -1}
	resp, err := ipc.NewMessage(ipc.MsgGroupOpResp, ipc.OpRespPayload{ID: "Infra", OK: false, Error: "rename: taken"})
	if err != nil {
		t.Fatal(err)
	}
	resp.ID = "grp-7"
	conn.recv <- resp
	got, ok := m.listenForMessages()().(sharedOpRespMsg)
	if !ok {
		t.Fatal("group_op_resp did not become a sharedOpRespMsg")
	}
	if got.id != "grp-7" || got.resp.OK || got.resp.Error != "rename: taken" {
		t.Errorf("got %+v", got)
	}
}

func TestCommitGroupEdit_RenameToAMergedNameIsRefusedBeforeSending(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "A", "B"))
	m.beginGroupEdit(groupEditState{mode: groupEditRename, target: "A", input: "b"})
	out, _ := m.commitGroupEdit()
	m = out.(Model)
	if m.flashText != groupNameTakenFlash {
		t.Errorf("flash = %q", m.flashText)
	}
	if n := countSent(conn, ipc.MsgGroupOp); n != 0 {
		t.Errorf("group_op sent %d times for a refused rename", n)
	}
}

func TestCommitGroupEdit_RenameFansOutToEveryDestListingTheName(t *testing.T) {
	m, local, remote := twoDestModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Shared"))
	rf := sharedFrame("q", 1, "proj-2", "", "Shared")
	rf.Dest = "hostA"
	m = updateWith(t, m, rf)
	m.beginGroupEdit(groupEditState{mode: groupEditRename, target: "Shared", input: "Renamed"})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	m = out.(Model)
	if countSent(local, ipc.MsgGroupOp) != 1 || countSent(remote, ipc.MsgGroupOp) != 1 {
		t.Errorf("group_op sent local=%d remote=%d, want 1 each", countSent(local, ipc.MsgGroupOp), countSent(remote, ipc.MsgGroupOp))
	}
	if m.groups.indexOf("Renamed") < 0 {
		t.Error("no optimistic local rename")
	}
}

func TestExecuteGroupCtxMenuItem_Delete_SendsGroupOpDelete(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "Infra", "Infra"))
	out, cmd := m.executeGroupCtxMenuItem("Infra", ctxMenuItem{id: ctxActDeleteGroup, enabled: true})
	runCmd(cmd)
	m = out.(Model)
	if n := countSent(conn, ipc.MsgGroupOp); n != 1 {
		t.Fatalf("group_op sent %d times", n)
	}
	var p ipc.GroupOpPayload
	if err := conn.lastSent().DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.Op != ipc.GroupOpDelete || p.Name != "Infra" {
		t.Errorf("sent %+v", p)
	}
	if m.groups.indexOf("Infra") >= 0 {
		t.Error("no optimistic local delete")
	}
}

// A create while a LEGACY project is active goes to the shared local daemon,
// where empty groups live: sent nowhere, no other client would ever see it.
func TestCommitGroupEdit_NewEmptyGroupWithLegacyActive_CreatesOnLocal(t *testing.T) {
	m, local, remote := twoDestModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", ""))
	legacy := stateMsg("", 0, "tab-old")
	legacy.Dest = "hostA"
	legacy.Projects = []ProjectInfo{{ID: "proj-old", Name: "old", TabIDs: []string{"tab-old"}}}
	m = updateWith(t, m, legacy)
	for i, p := range m.projects {
		if p.Dest == "hostA" {
			m.activeProject = i
		}
	}
	if m.activeDest() != "hostA" {
		t.Fatalf("active dest = %q, want hostA", m.activeDest())
	}
	m.beginGroupEdit(groupEditState{mode: groupEditNew, input: "Fresh"})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	m = out.(Model)
	if countSent(local, ipc.MsgGroupOp) != 1 || countSent(remote, ipc.MsgGroupOp) != 0 {
		t.Errorf("group_op sent local=%d remote=%d, want 1/0", countSent(local, ipc.MsgGroupOp), countSent(remote, ipc.MsgGroupOp))
	}
	if m.groups.indexOf("Fresh") < 0 {
		t.Error("no optimistic local create")
	}
}

// sharedSidebarModel is a sidebar-painting Model whose local destination
// frames are driven through Update (the conn's receive side is closed, as in
// connectedTestModelCapturingSends).
func sharedSidebarModel(t *testing.T) Model {
	t.Helper()
	m := *newSplitDragTestModel(t)
	conn := newFakeConn()
	close(conn.recv)
	m.client = conn
	m.sidebarOpen = true
	m.sidebarWidth = 22
	return m
}

// headerRow is the sidebar row of the group named name, or fails.
func headerRow(t *testing.T, m Model, name string) int {
	t.Helper()
	rows, _ := m.sidebarRows(22)
	for y, r := range rows {
		if r.kind == sidebarRowGroup && r.index < len(m.groups.Groups) && m.groups.Groups[r.index].Name == name {
			return y
		}
	}
	t.Fatalf("no header row for group %q in %v", name, groupNames(m))
	return -1
}

// F-6 / I-2: the header drag is keyed by NAME, through the real press,
// motion and release. Another client deletes "A" — BEFORE the dragged "C" —
// while the drag is armed, so C's index shifts under it; an index-keyed drag
// would then move D instead.
func TestGroupDrag_FrameDeletesAGroupBeforeTheDraggedOne_MovesTheRightGroup(t *testing.T) {
	m := sharedSidebarModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "A", "B", "C", "D"))
	if got := strings.Join(groupNames(m), ","); got != "A,B,C,D" {
		t.Fatalf("setup: groups = %s", got)
	}
	m, _ = grpPress(m, headerRow(t, m, "C"), tea.MouseLeft)
	if !m.groupDragging || m.groupDragName != "C" {
		t.Fatalf("setup: drag = (%v, %q), want armed on C", m.groupDragging, m.groupDragName)
	}
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "", "B", "C", "D"))
	if got := strings.Join(groupNames(m), ","); got != "B,C,D" {
		t.Fatalf("the other client's delete did not show: %s", got)
	}
	if !m.groupDragging {
		t.Fatal("the frame ended the drag")
	}
	y := headerRow(t, m, "D")
	m, _ = grpMotion(m, y)
	m, _ = grpRelease(m, y)
	if got := strings.Join(groupNames(m), ","); got != "B,D,C" {
		t.Errorf("groups = %s, want B,D,C (C dropped onto D's slot, B untouched)", got)
	}
	if m.groupDragging {
		t.Error("release did not end the drag")
	}
}

func TestRecentListFor_SharedDestReadsTheFrame(t *testing.T) {
	m := connectedTestModel(t)
	m.SetRecentCWDs([]string{"/file"})
	f := sharedFrame("r", 1, "proj-1", "")
	f.RecentCWDs = []string{"/daemon-one", "/daemon-two"}
	m = updateWith(t, m, f)
	if got := m.recentListFor(""); len(got) != 2 || got[0] != "/daemon-one" {
		t.Errorf("recent for a shared dest = %v", got)
	}
	if got := m.recentListFor("legacy"); len(got) != 1 || got[0] != "/file" {
		t.Errorf("recent for a legacy dest = %v", got)
	}
}

// The Ctrl+N pick list asks the daemon about ITS list for a shared
// destination, not the client file's.
func TestEnterSetup_SharedDest_AsksAboutTheDaemonsRecentList(t *testing.T) {
	m := connectedTestModel(t)
	m.SetRecentCWDs([]string{"/file"})
	f := sharedFrame("r", 1, "proj-1", "")
	f.RecentCWDs = []string{"/daemon-one"}
	m = updateWith(t, m, f)
	p := &plugin.PanePlugin{Name: "ai", Command: plugin.CommandConfig{PromptsCWD: true}}
	m.enterSetupOrSplit(p)
	if got := m.recentScan.asked; len(got) != 1 || got[0] != "/daemon-one" {
		t.Errorf("existence check asked about %v, want the daemon's list", got)
	}
}

// A shared daemon records the folder itself (spec 4.4): the client neither
// pushes it onto its own list nor writes recent-cwds.json.
func TestHandleCreatePaneSplit_SharedDest_RecordsNothingLocally(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := connectedTestModel(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", ""))
	m.selectedCWD = t.TempDir()
	out, _ := m.handleCreatePaneSplit()
	got := out.(Model)
	if len(got.recentCWDs) != 0 {
		t.Errorf("in-memory recentCWDs = %v, want untouched", got.recentCWDs)
	}
	if disk := LoadRecentCWDs(config.RecentCWDsPath("")); len(disk) != 0 {
		t.Errorf("persisted %v for a shared destination", disk)
	}
}

func TestClearDragState_ClearsGroupDragName(t *testing.T) {
	m := Model{groupDragging: true, groupDragName: "x", groupDragMoved: true}
	m.clearDragState()
	if m.groupDragging || m.groupDragName != "" || m.groupDragMoved {
		t.Error("group drag survived clearDragState")
	}
}

var _ tea.Msg = sharedOpRespMsg{}
