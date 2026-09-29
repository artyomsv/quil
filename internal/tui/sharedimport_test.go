package tui

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// updateNoWait is updateWith for a twoDestModel: its Router synthesises one
// link-lost per closed conn and then blocks, so the third re-armed listen
// would hang a plain runCmd. Every send under test happens inside Update.
func updateNoWait(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	out, cmd := m.Update(msg)
	runCmdNoWait(cmd)
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("Update(%T) returned %T, want Model", msg, out)
	}
	return got
}

func importTestModel(t *testing.T) (Model, *fakeConn, *fakeConn) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	m, local, remote := twoDestModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: projectGroups{Groups: []projectGroup{
		{Name: "Infra", Members: []groupMember{{Dest: "", ID: "proj-1"}, {Dest: "hostA", ID: "proj-2"}}},
		{Name: "Empty"},
	}}}, config.ProjectGroupsPath())
	runCmd(m.saveGroupsCmd())
	m.SetSharedImportMarker(config.SharedImportPath())
	if err := SaveRecentCWDs(config.RecentCWDsPath("hostA"), []string{"/srv/a"}); err != nil {
		t.Fatal(err)
	}
	return m, local, remote
}

func importPayload(t *testing.T, conn *fakeConn) (ipc.SharedImportPayload, string) {
	t.Helper()
	sent := lastSent(t, conn, ipc.MsgSharedImport)
	var p ipc.SharedImportPayload
	if err := sent.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	return p, sent.ID
}

func hashDir(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		data, _ := os.ReadFile(dir + "/" + e.Name())
		h.Write([]byte(e.Name()))
		h.Write(data)
	}
	return string(h.Sum(nil))
}

func TestUpdate_FirstSharedFrame_SendsOneImportForPendingKinds(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	p, id := importPayload(t, local)
	if strings.Join(p.Kinds, ",") != "groups,recent" {
		t.Errorf("kinds = %v; notes are marked done at once for the local daemon", p.Kinds)
	}
	if id == "" {
		t.Error("import sent without an id")
	}
	// Groups: this destination's members only, plus every member-less group.
	if len(p.Groups) != 2 || p.Groups[0].Name != "Infra" || len(p.Groups[0].ProjectIDs) != 1 || p.Groups[0].ProjectIDs[0] != "proj-1" || p.Groups[1].Name != "Empty" {
		t.Errorf("groups = %+v", p.Groups)
	}
	if mk := loadImportMarker(config.SharedImportPath()); !mk.Dests["local"].Notes {
		t.Error("local notes marker not set at once (F-12)")
	}
	m = updateNoWait(t, m, sharedFrame("r", 2, "proj-1", ""))
	if countSent(local, ipc.MsgSharedImport) != 1 {
		t.Error("a second frame sent a second import")
	}
}

func TestUpdate_ImportResp_SetsMarkerForAnsweredKindsOnly(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}, GroupsApplied: true}})
	mk := loadImportMarker(config.SharedImportPath())
	if !mk.Dests["local"].Groups || mk.Dests["local"].Recent {
		t.Errorf("marker = %+v", mk.Dests["local"])
	}
}

func TestUpdate_Import_NoAnswer_MarkerUnchanged(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, sharedImportTimeoutMsg{id: id})
	if mk := loadImportMarker(config.SharedImportPath()); mk.Dests["local"].Groups || mk.Dests["local"].Recent {
		t.Errorf("marker set with no answer: %+v", mk.Dests["local"])
	}
}

// A late answer is still the daemon's answer: the timeout only logs.
func TestUpdate_Import_AnswerAfterTimeout_StillSetsTheMarker(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, sharedImportTimeoutMsg{id: id})
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups, ipc.ImportKindRecent}}})
	if mk := loadImportMarker(config.SharedImportPath()); !mk.Dests["local"].Groups || !mk.Dests["local"].Recent {
		t.Errorf("marker = %+v, want groups and recent", mk.Dests["local"])
	}
}

// An answer arriving from a daemon other than the one asked settles nothing.
func TestUpdate_ImportResp_FromAnotherDestination_IsIgnored(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "hostA", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
	if mk := loadImportMarker(config.SharedImportPath()); mk.Dests["local"].Groups || mk.Dests["hostA"].Groups {
		t.Errorf("marker = %+v", mk.Dests)
	}
}

// Review focus 4, plus the remote recent list and the size bound.
func TestUpdate_Import_AmbiguousPaneIDIsSkippedAndKept(t *testing.T) {
	m, _, remote := importTestModel(t)
	for id, text := range map[string]string{"pane-shared": "ambiguous\n", "pane-only": "mine\n", "pane-big": strings.Repeat("x", ipc.MaxNoteBytes+1)} {
		if err := persist.SaveNotes(config.NotesDir(), id, text); err != nil {
			t.Fatal(err)
		}
	}
	before := hashDir(t, config.NotesDir())
	localFrame := sharedFrame("r", 1, "proj-1", "")
	localFrame.Panes = append(localFrame.Panes, PaneInfo{ID: "pane-shared", TabID: "tab-proj-1", Type: "terminal"})
	localFrame.Tabs[0].Panes = append(localFrame.Tabs[0].Panes, "pane-shared")
	m = updateNoWait(t, m, localFrame)
	rf := sharedFrame("q", 1, "proj-2", "")
	rf.Dest = "hostA"
	rf.Panes = append(rf.Panes, PaneInfo{ID: "pane-shared", TabID: "tab-proj-2", Type: "terminal"},
		PaneInfo{ID: "pane-only", TabID: "tab-proj-2", Type: "terminal"}, PaneInfo{ID: "pane-big", TabID: "tab-proj-2", Type: "terminal"})
	rf.Tabs[0].Panes = append(rf.Tabs[0].Panes, "pane-shared", "pane-only", "pane-big")
	m = updateNoWait(t, m, rf)
	p, _ := importPayload(t, remote)
	if strings.Join(p.Kinds, ",") != "groups,recent,notes" {
		t.Errorf("kinds = %v", p.Kinds)
	}
	if len(p.Notes) != 1 || p.Notes[0].PaneID != "pane-only" || p.Notes[0].Text != "mine\n" {
		t.Errorf("notes = %+v, want only pane-only", p.Notes)
	}
	if len(p.Recent) != 1 || p.Recent[0] != "/srv/a" {
		t.Errorf("recent = %v", p.Recent)
	}
	if len(p.Groups) != 1 || p.Groups[0].ProjectIDs[0] != "proj-2" {
		t.Errorf("groups = %+v (member-less groups go to the LOCAL daemon only)", p.Groups)
	}
	if hashDir(t, config.NotesDir()) != before {
		t.Error("an old note file was modified by the import")
	}
	saved, _ := os.ReadFile(config.ProjectGroupsPath())
	var file projectGroupsFile
	if err := json.Unmarshal(saved, &file); err != nil || len(file.Groups) != 2 {
		t.Errorf("project-groups.json changed shape: %s", saved)
	}
}

// The request budget counts each note as encoded: a note that fits alone but
// not beside the others waits for the next launch.
func TestCollectImportNotes_BudgetDefersTheRest(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"pane-a", "pane-b"} {
		if err := persist.SaveNotes(dir, id, strings.Repeat("y", 100)); err != nil {
			t.Fatal(err)
		}
	}
	mine := map[string]bool{"pane-a": true, "pane-b": true}
	enc, err := json.Marshal(ipc.SharedImportNote{PaneID: "pane-a", Text: strings.Repeat("y", 100)})
	if err != nil {
		t.Fatal(err)
	}
	got := collectImportNotes(dir, mine, nil, len(enc)+10)
	if len(got) != 1 || got[0].PaneID != "pane-a" {
		t.Errorf("notes = %+v, want pane-a alone", got)
	}
}

func TestUpdate_Import_WithoutMarkerPath_IsOff(t *testing.T) {
	m, local := connectedTestModelCapturingSends(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	if countSent(local, ipc.MsgSharedImport) != 0 {
		t.Error("a Model with no marker path imported")
	}
}

// R-5: the daemon creates a group on set_project_group and refuses an import
// once it holds one, so a group send ahead of the import answer would make
// it drop every member the import carries. Before the answer the change goes
// to the file only; the answer replays it; after it, sends go straight out.
func TestUpdate_GroupAssignBeforeImportAnswer_IsHeldThenReplayed(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	runCmd(m.moveProjectToGroup("", "proj-1", "Empty"))
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 0 {
		t.Fatalf("set_project_group sent %d times before the import answer", n)
	}
	// A frame in between keeps the file's view: nothing is authoritative yet.
	m = updateNoWait(t, m, sharedFrame("r", 2, "proj-1", ""))
	if m.groups.groupOf("", "proj-1") != m.groups.indexOf("Empty") {
		t.Errorf("the local assign was lost: %+v", m.groups.Groups)
	}
	saved, err := loadProjectGroups(config.ProjectGroupsPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved.groupOf("hostA", "proj-2") != saved.indexOf("Infra") || saved.indexOf("Infra") < 0 {
		t.Errorf("another destination's cached member was lost: %+v", saved.Groups)
	}
	if saved.groupOf("", "proj-1") != saved.indexOf("Empty") {
		t.Errorf("the file does not hold the assign: %+v", saved.Groups)
	}

	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups, ipc.ImportKindRecent}, GroupsApplied: true}})
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 1 {
		t.Fatalf("set_project_group sent %d times after the answer, want the held one", n)
	}
	var p ipc.SetProjectGroupPayload
	if err := lastSent(t, local, ipc.MsgSetProjectGroup).DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.ProjectID != "proj-1" || p.Group != "Empty" {
		t.Errorf("replayed %+v", p)
	}

	runCmd(m.moveProjectToGroup("", "proj-1", "Infra"))
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 2 {
		t.Errorf("set_project_group sent %d times, want the post-answer assign sent at once", n)
	}
}

// Two held changes for one project replay as the last one only.
func TestUpdate_HeldGroupSends_OneProjectReplaysItsLastChange(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	runCmd(m.moveProjectToGroup("", "proj-1", "Empty"))
	runCmd(m.ungroupProject("", "proj-1"))
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 1 {
		t.Fatalf("set_project_group sent %d times, want 1", n)
	}
	var p ipc.SetProjectGroupPayload
	if err := lastSent(t, local, ipc.MsgSetProjectGroup).DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.ProjectID != "proj-1" || p.Group != "" {
		t.Errorf("replayed %+v, want the ungroup", p)
	}
}

// A group op (create) is held the same way as an assign.
func TestUpdate_GroupCreateBeforeImportAnswer_IsHeld(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m.beginGroupEdit(groupEditState{mode: groupEditNew, input: "Fresh"})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	m = out.(Model)
	if n := countSent(local, ipc.MsgGroupOp); n != 0 {
		t.Fatalf("group_op sent %d times before the import answer", n)
	}
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
	if n := countSent(local, ipc.MsgGroupOp); n != 1 {
		t.Errorf("group_op sent %d times after the answer, want 1", n)
	}
}

// With groups marked done by an earlier launch, the daemon is authoritative at
// once — even holding no group (another client deleted them all) — and group
// sends go straight out.
func TestUpdate_GroupsMarkerDone_FrameIsAuthoritativeAndSendsAreOpen(t *testing.T) {
	m, local, _ := importTestModel(t)
	if err := saveImportMarker(config.SharedImportPath(), sharedImportMarker{Version: 1, Dests: map[string]importMarkerKinds{"local": {Groups: true, Recent: true, Notes: true}}}); err != nil {
		t.Fatal(err)
	}
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	if countSent(local, ipc.MsgSharedImport) != 0 {
		t.Error("an import was sent with every kind marked done")
	}
	if g := m.groups.groupOf("", "proj-1"); g >= 0 {
		t.Errorf("proj-1 still cached in group %d; the frame (no group) is authoritative", g)
	}
	if m.groups.groupOf("hostA", "proj-2") != m.groups.indexOf("Infra") {
		t.Errorf("hostA's cached member was touched: %+v", m.groups.Groups)
	}
	runCmd(m.moveProjectToGroup("", "proj-1", "Empty"))
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 1 {
		t.Errorf("set_project_group sent %d times, want 1", n)
	}
}

func TestListenForMessages_SharedImportResp_BecomesSharedImportRespMsg(t *testing.T) {
	conn := newFakeConn()
	m := Model{cfg: config.Default(), client: conn, tabDragFromIdx: -1}
	resp, err := ipc.NewMessage(ipc.MsgSharedImportResp, ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindRecent}, RecentApplied: true})
	if err != nil {
		t.Fatal(err)
	}
	resp.ID = "imp-3"
	conn.recv <- resp
	got, ok := m.listenForMessages()().(sharedImportRespMsg)
	if !ok {
		t.Fatal("shared_import_resp did not become a sharedImportRespMsg")
	}
	if got.id != "imp-3" || !got.resp.RecentApplied || len(got.resp.Answered) != 1 {
		t.Errorf("got %+v", got)
	}
}
