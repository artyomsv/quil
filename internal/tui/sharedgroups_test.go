package tui

import (
	"fmt"
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

// A remote daemon may not be honest about its caps: a frame over them keeps
// the first N of each list, and logs once however many frames repeat it.
func TestUpdate_OversizedSharedFrame_ListsCappedAndLoggedOnce(t *testing.T) {
	m := connectedTestModel(t)
	f := sharedFrame("r", 1, "proj-1", "")
	for i := 0; i < ipc.MaxGroupsPerDaemon+36; i++ {
		f.Groups = append(f.Groups, fmt.Sprintf("g%03d", i))
	}
	for i := 0; i < ipc.MaxRecentCWDs+15; i++ {
		f.RecentCWDs = append(f.RecentCWDs, fmt.Sprintf("/d%02d", i))
	}
	m = updateWith(t, m, f)
	groups, recent := m.daemonGroups[""], m.recentListFor("")
	if len(groups) != ipc.MaxGroupsPerDaemon || groups[0] != "g000" || groups[len(groups)-1] != fmt.Sprintf("g%03d", ipc.MaxGroupsPerDaemon-1) {
		t.Errorf("daemon groups = %d names (%v…), want the first %d", len(groups), groups[:min(3, len(groups))], ipc.MaxGroupsPerDaemon)
	}
	if len(recent) != ipc.MaxRecentCWDs || recent[0] != "/d00" {
		t.Errorf("recent = %v, want the first %d", recent, ipc.MaxRecentCWDs)
	}
	if got := len(groupNames(m)); got > ipc.MaxGroupsPerDaemon {
		t.Errorf("merged view holds %d groups, over the cap", got)
	}
	if len(m.sharedCapLogged) != 2 {
		t.Fatalf("cap log keys = %v, want one per list", m.sharedCapLogged)
	}
	f.Rev = 2
	m = updateWith(t, m, f)
	if len(m.sharedCapLogged) != 2 || len(m.daemonGroups[""]) != ipc.MaxGroupsPerDaemon {
		t.Errorf("second oversized frame: log keys %d, groups %d", len(m.sharedCapLogged), len(m.daemonGroups[""]))
	}
}

// frameNoListen applies a frame through Update without running its Cmd. A
// twoDestModel's router yields one link-lost per closed conn and then
// blocks, so from the third frame on a re-armed listen never returns.
func frameNoListen(t *testing.T, m Model, msg WorkspaceStateMsg) Model {
	t.Helper()
	out, _ := m.Update(msg)
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", out)
	}
	return got
}

// manyGroupedProjects is a shared frame from dest listing groups, with n
// projects, each filed under its own name prefix%03d.
func manyGroupedProjects(dest, prefix string, n int, groups ...string) WorkspaceStateMsg {
	f := WorkspaceStateMsg{Dest: dest, RunID: "r-" + dest, Rev: 1, SharedData: true, Groups: groups}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-proj-%03d", prefix, i)
		tab, pane := id+"-tab", id+"-pane"
		f.Projects = append(f.Projects, ProjectInfo{ID: id, Name: id, Group: fmt.Sprintf("%s%03d", prefix, i), TabIDs: []string{tab}})
		f.Tabs = append(f.Tabs, TabInfo{ID: tab, Name: "Shell", ProjectID: id, Panes: []string{pane}})
		f.Panes = append(f.Panes, PaneInfo{ID: pane, TabID: tab, Type: "terminal"})
	}
	f.ActiveProject, f.ActiveTab = f.Projects[0].ID, f.Tabs[0].ID
	return f
}

// A daemon's list is capped, and so are the names its projects carry: a
// host listing one group and filing each project under a name of its own
// adds at most the cap to the sidebar. The rest are shown ungrouped, the cap
// logs once, and another destination's names are not counted against it.
func TestUpdate_ProjectGroupNamesOverTheCap_RestShownUngrouped(t *testing.T) {
	m, _, _ := twoDestModel(t)
	m = frameNoListen(t, m, manyGroupedProjects("", "g", ipc.MaxGroupsPerDaemon+10, "Listed"))
	if got := len(groupNames(m)); got != ipc.MaxGroupsPerDaemon {
		t.Fatalf("merged view holds %d groups, want the cap %d (Listed + %d project names)", got, ipc.MaxGroupsPerDaemon, ipc.MaxGroupsPerDaemon-1)
	}
	last := ipc.MaxGroupsPerDaemon - 2 // the last project name that fits
	if g := m.groups.groupOf("", fmt.Sprintf("g-proj-%03d", last)); g < 0 || m.groups.Groups[g].Name != fmt.Sprintf("g%03d", last) {
		t.Errorf("project %d not in its own group (index %d)", last, g)
	}
	for i := last + 1; i < ipc.MaxGroupsPerDaemon+10; i++ {
		if g := m.groups.groupOf("", fmt.Sprintf("g-proj-%03d", i)); g >= 0 {
			t.Fatalf("project %d over the cap is in group %q, want ungrouped", i, m.groups.Groups[g].Name)
		}
	}
	key := "\x00project group names"
	if !m.sharedCapLogged[key] {
		t.Fatalf("cap log keys = %v, want %q", m.sharedCapLogged, key)
	}
	logged := len(m.sharedCapLogged)
	again := manyGroupedProjects("", "g", ipc.MaxGroupsPerDaemon+10, "Listed")
	again.Rev = 2
	m = frameNoListen(t, m, again)
	if len(m.sharedCapLogged) != logged || len(groupNames(m)) != ipc.MaxGroupsPerDaemon {
		t.Errorf("second frame: log keys %d (want %d), groups %d", len(m.sharedCapLogged), logged, len(groupNames(m)))
	}

	remote := manyGroupedProjects("hostA", "r", 3, "Far")
	m = frameNoListen(t, m, remote)
	for i := 0; i < 3; i++ {
		if g := m.groups.groupOf("hostA", fmt.Sprintf("r-proj-%03d", i)); g < 0 || m.groups.Groups[g].Name != fmt.Sprintf("r%03d", i) {
			t.Errorf("hostA project %d not in its group (index %d): the local cap reached another destination", i, g)
		}
	}
	if m.sharedCapLogged["hostA\x00project group names"] {
		t.Error("hostA logged over the cap with 4 names")
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
	// The file must exist and hold both groups first: a missing file loads
	// as empty, and every -1 below would then compare equal.
	a, b := saved.indexOf("A"), saved.indexOf("B")
	if a < 0 || b < 0 {
		t.Fatalf("saved file = %+v, want groups A and B", saved.Groups)
	}
	if !hasMember(saved.Groups[b], "", "proj-1") || hasMember(saved.Groups[a], "", "proj-1") {
		t.Errorf("file members = %+v, want proj-1 in B and not in A", saved.Groups)
	}
	if !hasMember(saved.Groups[a], "hostA", "proj-far") {
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
// first frame, not on a later one: only a name that DISAPPEARS goes.
func TestUpdate_SharedLocal_EmptyFileGroupNobodyListedIsKept(t *testing.T) {
	m := connectedTestModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{{Name: "Kept"}}}}, "")
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Listed"))
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "", "Listed"))
	if got := groupNames(m); len(got) != 2 || got[0] != "Kept" || got[1] != "Listed" {
		t.Errorf("groups = %v, want Kept kept and Listed appended", got)
	}
}

// A shared daemon holding no groups yet has not been imported
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

// Order and collapsed state survive a RELAUNCH. "GPU" is
// first and collapsed with its only member on hostA. Launch 1 sees both
// destinations and saves; launch 2 loads that saved file, and its local
// frame arrives before hostA's. Stripping hostA's member from the file (the
// old rule) left GPU empty on disk, and launch 2's local frame then dropped
// it; hostA's frame re-added it last and expanded.
func TestUpdate_FileOrderAndCollapsedSurviveARelaunch(t *testing.T) {
	path := t.TempDir() + "/project-groups.json"
	launch := func(groups projectGroups) Model {
		m, _, _ := twoDestModel(t)
		m.SetProjectGroups(ProjectGroupsState{groups: groups}, path)
		m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "Local", "Local"))
		rf := sharedFrame("q", 1, "proj-2", "GPU", "GPU")
		rf.Dest = "hostA"
		return updateWith(t, m, rf)
	}

	m := launch(projectGroups{Groups: []projectGroup{
		{Name: "GPU", Collapsed: true, Members: []groupMember{{Dest: "hostA", ID: "proj-2"}}},
		{Name: "Local", Members: []groupMember{{Dest: "", ID: "proj-1"}}},
	}})
	// Any later change saves the whole view — a collapse toggle, a drag.
	runCmd(m.saveGroupsCmd())

	saved, err := loadProjectGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.indexOf("GPU") < 0 {
		t.Fatalf("saved file = %+v, want GPU in it", saved.Groups)
	}
	m = launch(saved)
	if got := groupNames(m); len(got) != 2 || got[0] != "GPU" || got[1] != "Local" {
		t.Fatalf("groups after relaunch = %v, want GPU still first", got)
	}
	if !m.groups.Groups[0].Collapsed {
		t.Error("GPU lost its collapsed state across the relaunch")
	}
	if m.groups.groupOf("hostA", "proj-2") != 0 || m.groups.groupOf("", "proj-1") != 1 {
		t.Errorf("members = %+v", m.groups.Groups)
	}
}

// hasMember reports whether grp holds (dest, id).
func hasMember(grp projectGroup, dest, id string) bool {
	for _, mb := range grp.Members {
		if mb.Dest == dest && mb.ID == id {
			return true
		}
	}
	return false
}

// A delete made in another client shows here — hostA
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

// groupOpsSent decodes every group_op sent on conn, in order.
func groupOpsSent(t *testing.T, conn *fakeConn) []ipc.GroupOpPayload {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	var out []ipc.GroupOpPayload
	for _, msg := range conn.sent {
		if msg.Type != ipc.MsgGroupOp {
			continue
		}
		var p ipc.GroupOpPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func commitRename(t *testing.T, m Model, from, to string) Model {
	t.Helper()
	m.beginGroupEdit(groupEditState{mode: groupEditRename, target: from, input: to})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	return out.(Model)
}

// A second rename made before the frame of the first reaches the daemon: its
// list still says A, but the op for B goes to the daemon the first went to.
func TestCommitGroupEdit_RenameTwiceBeforeTheFrame_SendsBoth(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "A", "A"))
	m = commitRename(t, m, "A", "B")
	m = commitRename(t, m, "B", "C")
	ops := groupOpsSent(t, conn)
	if len(ops) != 2 || ops[0].Name != "A" || ops[0].NewName != "B" || ops[1].Name != "B" || ops[1].NewName != "C" {
		t.Fatalf("group_ops = %+v, want rename A→B then B→C", ops)
	}
}

// Create and delete before the create's frame: the delete must reach the
// daemon too, or the group comes back with the next frame.
func TestSendGroupOpEverywhere_CreateThenDeleteBeforeTheFrame_SendsBoth(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", ""))
	runCmd(m.sendGroupOpEverywhere(ipc.GroupOpCreate, "Tmp", ""))
	runCmd(m.sendGroupOpEverywhere(ipc.GroupOpDelete, "Tmp", ""))
	ops := groupOpsSent(t, conn)
	if len(ops) != 2 || ops[0].Op != ipc.GroupOpCreate || ops[1].Op != ipc.GroupOpDelete || ops[1].Name != "Tmp" {
		t.Fatalf("group_ops = %+v, want create then delete of Tmp", ops)
	}
}

// The project menu's New group sends only set_project_group, which creates
// the group on the daemon. A rename or delete of it before the daemon's next
// frame must still reach that daemon.
func TestNewGroupFromProjectMenu_FollowUpBeforeTheFrame_IsSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(t *testing.T, m Model) Model
		want ipc.GroupOpPayload
	}{
		{"rename", func(t *testing.T, m Model) Model { return commitRename(t, m, "Tmp", "Tmp2") },
			ipc.GroupOpPayload{Op: ipc.GroupOpRename, Name: "Tmp", NewName: "Tmp2"}},
		{"delete", func(t *testing.T, m Model) Model {
			runCmd(m.sendGroupOpEverywhere(ipc.GroupOpDelete, "Tmp", ""))
			return m
		}, ipc.GroupOpPayload{Op: ipc.GroupOpDelete, Name: "Tmp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, conn := connectedTestModelCapturingSends(t)
			m = updateWith(t, m, sharedFrame("r", 1, "proj-1", ""))
			m.beginGroupEdit(groupEditState{mode: groupEditNew, input: "Tmp", dest: "", projectID: "proj-1"})
			out, cmd := m.commitGroupEdit()
			runCmd(cmd)
			m = out.(Model)
			if n := countSent(conn, ipc.MsgSetProjectGroup); n != 1 {
				t.Fatalf("set_project_group sent %d times", n)
			}
			m = tc.act(t, m)
			ops := groupOpsSent(t, conn)
			if len(ops) != 1 || ops[0] != tc.want {
				t.Errorf("group_ops = %+v, want %+v", ops, tc.want)
			}
		})
	}
}

// Two assignments to a new group before the next frame: the first creates
// it, the second is refused (its project went away). The refusal must not
// drop the name, or a rename right after it reaches nobody.
func TestUpdate_OneOfTwoAssignsRefused_RenameStillReachesTheDaemon(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", ""))
	runCmd(m.sendSetProjectGroup("", "proj-1", "Tmp"))
	okID := lastSent(t, conn, ipc.MsgSetProjectGroup).ID
	runCmd(m.sendSetProjectGroup("", "proj-gone", "Tmp"))
	refusedID := lastSent(t, conn, ipc.MsgSetProjectGroup).ID
	m = updateWith(t, m, sharedOpRespMsg{dest: "", id: okID, resp: ipc.OpRespPayload{OK: true}})
	m = updateWith(t, m, sharedOpRespMsg{dest: "", id: refusedID, resp: ipc.OpRespPayload{OK: false, Error: "set group: no such project"}})
	runCmd(m.sendGroupOpEverywhere(ipc.GroupOpRename, "Tmp", "Tmp2"))
	ops := groupOpsSent(t, conn)
	if len(ops) != 1 || ops[0].Op != ipc.GroupOpRename || ops[0].Name != "Tmp" {
		t.Errorf("group_ops = %+v, want the rename of Tmp", ops)
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
	// Typed and saved through Update, the path the dialog's Enter takes. The
	// send is synchronous in Update, so the Cmd is not run (see frameNoListen).
	m.beginGroupEdit(groupEditState{mode: groupEditNew})
	m = grpType(m, "Fresh")
	m, _ = grpKey(m, tea.KeyEnter)
	if m.dialog != dialogNone {
		t.Fatal("Enter on a valid name must close the dialog")
	}
	if countSent(remote, ipc.MsgGroupOp) != 0 {
		t.Errorf("group_op sent to the legacy host: %d", countSent(remote, ipc.MsgGroupOp))
	}
	if ops := groupOpsSent(t, local); len(ops) != 1 || ops[0].Op != ipc.GroupOpCreate || ops[0].Name != "Fresh" {
		t.Errorf("local group_ops = %+v, want one create of Fresh", ops)
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

// The header drag is keyed by NAME, through the real press,
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

// A shared daemon records the folder itself: the client neither
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
