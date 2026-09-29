package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
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
	got, deferred := collectImportNotes(dir, mine, nil, len(enc)+10)
	if len(got) != 1 || got[0].PaneID != "pane-a" || !deferred {
		t.Errorf("notes = %+v deferred=%v, want pane-a alone and deferred", got, deferred)
	}
	if _, deferred := collectImportNotes(dir, mine, nil, 1<<20); deferred {
		t.Error("deferred reported with room for every note")
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

// decodeSetProjectGroup decodes the last set_project_group sent on conn.
func decodeSetProjectGroup(t *testing.T, conn *fakeConn) ipc.SetProjectGroupPayload {
	t.Helper()
	var p ipc.SetProjectGroupPayload
	if err := lastSent(t, conn, ipc.MsgSetProjectGroup).DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// I-1: an import in flight when the link drops can never be answered. The
// first shared frame after the reattach sends it again, and ITS answer opens
// group sends and replays the held assign. The old id's answer settles
// nothing.
func TestUpdate_ImportLostWithTheLink_ReattachFrameResendsAndItsAnswerReplays(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, oldID := importPayload(t, local)
	runCmd(m.moveProjectToGroup("", "proj-1", "Empty"))
	m = updateNoWait(t, m, linkLostMsg{dest: "", err: errLinkLost})
	m = updateNoWait(t, m, sharedFrame("r2", 1, "proj-1", ""))
	if n := countSent(local, ipc.MsgSharedImport); n != 2 {
		t.Fatalf("shared_import sent %d times, want it re-sent after the reattach", n)
	}
	p, newID := importPayload(t, local)
	if newID == oldID {
		t.Fatal("the re-sent import reused the old id")
	}
	// The file view already holds the held assign, so the re-sent import
	// carries it.
	for _, g := range p.Groups {
		if g.Name == "Empty" && (len(g.ProjectIDs) != 1 || g.ProjectIDs[0] != "proj-1") {
			t.Errorf("re-sent Empty = %+v, want proj-1", g)
		}
	}
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: oldID, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 0 {
		t.Fatalf("the dead link's answer opened group sends (%d sent)", n)
	}
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: newID, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups, ipc.ImportKindRecent}}})
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 1 {
		t.Fatalf("set_project_group sent %d times after the answer, want the held one", n)
	}
	if p := decodeSetProjectGroup(t, local); p.ProjectID != "proj-1" || p.Group != "Empty" {
		t.Errorf("replayed %+v", p)
	}
}

// The reattach path clears the in-flight import too.
func TestArmReattachReset_ForgetsTheImportInFlight(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	m.armReattachReset("")
	m = updateNoWait(t, m, sharedFrame("r2", 1, "proj-1", ""))
	if n := countSent(local, ipc.MsgSharedImport); n != 2 {
		t.Errorf("shared_import sent %d times, want it re-sent after the reattach", n)
	}
}

// I-1: an error reply to the import is no answer — the next frame sends it
// again, up to maxImportErrors error replies this session.
func TestUpdate_ImportErrorReply_NextFrameResendsUntilTheCap(t *testing.T) {
	m, local, _ := importTestModel(t)
	rev := uint64(1)
	m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", ""))
	for i := 1; i <= maxImportErrors; i++ {
		_, id := importPayload(t, local)
		m = updateNoWait(t, m, sharedImportErrMsg{dest: "", id: id, text: "bad_payload: x"})
		rev++
		m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", ""))
		want := i + 1
		if i == maxImportErrors {
			want = i
		}
		if n := countSent(local, ipc.MsgSharedImport); n != want {
			t.Fatalf("after error %d: shared_import sent %d times, want %d", i, n, want)
		}
	}
	if mk := loadImportMarker(config.SharedImportPath()); mk.Dests["local"].Groups || mk.Dests["local"].Recent {
		t.Errorf("an error reply set the marker: %+v", mk.Dests["local"])
	}
}

func TestListenForMessages_SharedImportErrorReply_BecomesSharedImportErrMsg(t *testing.T) {
	conn := newFakeConn()
	m := Model{cfg: config.Default(), client: conn, tabDragFromIdx: -1}
	reply, err := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeBadPayload, Message: "import request too large", Type: ipc.MsgSharedImport})
	if err != nil {
		t.Fatal(err)
	}
	reply.ID = "imp-9"
	conn.recv <- reply
	got, ok := m.listenForMessages()().(sharedImportErrMsg)
	if !ok {
		t.Fatal("an error reply to shared_import did not become a sharedImportErrMsg")
	}
	if got.id != "imp-9" || !strings.Contains(got.text, "too large") {
		t.Errorf("got %+v", got)
	}
}

// I-2: notes the request budget left out keep the notes kind pending, and the
// next launch (a fresh Model) sends it again.
func TestUpdate_ImportNotesDeferredByBudget_NextLaunchSendsTheNotesKind(t *testing.T) {
	m, _, remote := importTestModel(t)
	rf := sharedFrame("q", 1, "proj-2", "")
	rf.Dest = "hostA"
	// One note more than the 8 MiB request can carry at the per-note cap.
	n := (ipc.MaxSharedImportBytes-importReserveBytes)/ipc.MaxNoteBytes + 1
	text := strings.Repeat("x", ipc.MaxNoteBytes)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("pane-n%02d", i)
		if err := persist.SaveNotes(config.NotesDir(), id, text); err != nil {
			t.Fatal(err)
		}
		rf.Panes = append(rf.Panes, PaneInfo{ID: id, TabID: "tab-proj-2", Type: "terminal"})
		rf.Tabs[0].Panes = append(rf.Tabs[0].Panes, id)
	}
	m = updateNoWait(t, m, rf)
	p, id := importPayload(t, remote)
	if len(p.Notes) == 0 || len(p.Notes) >= n {
		t.Fatalf("notes sent = %d of %d, want some deferred", len(p.Notes), n)
	}
	all := []string{ipc.ImportKindGroups, ipc.ImportKindRecent, ipc.ImportKindNotes}
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "hostA", id: id, resp: ipc.SharedImportRespPayload{Answered: all, NotesApplied: len(p.Notes)}})
	mk := loadImportMarker(config.SharedImportPath())
	if k := mk.Dests[config.DestFileKey("hostA")]; !k.Groups || !k.Recent || k.Notes {
		t.Fatalf("marker = %+v, want groups+recent done and notes pending", k)
	}

	next, _, remote2 := twoDestModel(t)
	next.SetSharedImportMarker(config.SharedImportPath())
	next = updateNoWait(t, next, rf)
	p2, _ := importPayload(t, remote2)
	if strings.Join(p2.Kinds, ",") != ipc.ImportKindNotes || len(p2.Notes) == 0 {
		t.Errorf("next launch: kinds = %v, %d notes; want the notes kind alone", p2.Kinds, len(p2.Notes))
	}
}

// M-1: a rename made before the groups answer is held for every destination
// that will list the name once its import lands, and replayed after.
func TestUpdate_GroupRenameBeforeImportAnswer_IsHeldThenReplayed(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m.beginGroupEdit(groupEditState{mode: groupEditRename, target: "Infra", input: "Platform"})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	m = out.(Model)
	if n := countSent(local, ipc.MsgGroupOp); n != 0 {
		t.Fatalf("group_op sent %d times before the import answer", n)
	}
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
	if n := countSent(local, ipc.MsgGroupOp); n != 1 {
		t.Fatalf("group_op sent %d times after the answer, want the held rename", n)
	}
	var p ipc.GroupOpPayload
	if err := lastSent(t, local, ipc.MsgGroupOp).DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.Op != ipc.GroupOpRename || p.Name != "Infra" || p.NewName != "Platform" {
		t.Errorf("replayed %+v", p)
	}
}

// M-1: a held create makes a later delete of that name held too, in order.
func TestUpdate_HeldCreateThenDelete_BothReplayedInOrder(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	m.beginGroupEdit(groupEditState{mode: groupEditNew, input: "Fresh"})
	out, cmd := m.commitGroupEdit()
	runCmd(cmd)
	m = out.(Model)
	runCmd(m.sendGroupOpEverywhere(ipc.GroupOpDelete, "Fresh", ""))
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
	if n := countSent(local, ipc.MsgGroupOp); n != 2 {
		t.Fatalf("group_op sent %d times, want create then delete", n)
	}
	var p ipc.GroupOpPayload
	if err := lastSent(t, local, ipc.MsgGroupOp).DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.Op != ipc.GroupOpDelete || p.Name != "Fresh" {
		t.Errorf("last replayed %+v, want the delete", p)
	}
}

// M-2: two clients sharing a QUIL_HOME save the marker at once. Each save
// goes through its own uniquely named temp file, so none fails, the file
// always parses as one of the two, and no temp file is left behind. A fixed
// path+".tmp" makes one writer rename the other's file away (ENOENT) or
// publish a torn write.
func TestSaveImportMarker_ConcurrentSavesNeverCollide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared-import.json")
	long := sharedImportMarker{Version: 1, Dests: map[string]importMarkerKinds{}}
	for i := 0; i < 60; i++ {
		long.Dests[fmt.Sprintf("host-%02d-%s", i, strings.Repeat("x", 20))] = importMarkerKinds{Groups: true, Recent: true}
	}
	short := sharedImportMarker{Version: 1, Dests: map[string]importMarkerKinds{"local": {Notes: true}}}
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for _, mk := range []sharedImportMarker{long, short} {
		wg.Add(1)
		go func(mk sharedImportMarker) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if err := saveImportMarker(path, mk); err != nil {
					errs <- err
					return
				}
			}
		}(mk)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent save failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got sharedImportMarker
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the marker does not parse after concurrent saves: %v", err)
	}
	if !reflect.DeepEqual(got, long) && !reflect.DeepEqual(got, short) {
		t.Errorf("the marker holds %d dests, neither snapshot", len(got.Dests))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "shared-import.json" {
		t.Errorf("dir holds %v, want the marker alone", entries)
	}
}

// N-1: panes the daemon already has note history for (NoteRev > 0) are not
// sent, so the next launch's import carries the notes the budget deferred
// instead of the same first batch again.
func TestUpdate_DeferredNotes_NextLaunchSendsTheRestNotTheFirstBatch(t *testing.T) {
	m, _, remote := importTestModel(t)
	rf := sharedFrame("q", 1, "proj-2", "")
	rf.Dest = "hostA"
	n := (ipc.MaxSharedImportBytes-importReserveBytes)/ipc.MaxNoteBytes + 1
	text := strings.Repeat("x", ipc.MaxNoteBytes)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("pane-n%02d", i)
		if err := persist.SaveNotes(config.NotesDir(), id, text); err != nil {
			t.Fatal(err)
		}
		rf.Panes = append(rf.Panes, PaneInfo{ID: id, TabID: "tab-proj-2", Type: "terminal"})
		rf.Tabs[0].Panes = append(rf.Tabs[0].Panes, id)
	}
	m = updateNoWait(t, m, rf)
	p, id := importPayload(t, remote)
	sent1 := map[string]bool{}
	for _, nt := range p.Notes {
		sent1[nt.PaneID] = true
	}
	if len(sent1) == 0 || len(sent1) >= n {
		t.Fatalf("first import sent %d of %d notes, want some deferred", len(sent1), n)
	}
	all := []string{ipc.ImportKindGroups, ipc.ImportKindRecent, ipc.ImportKindNotes}
	m = updateNoWait(t, m, sharedImportRespMsg{dest: "hostA", id: id, resp: ipc.SharedImportRespPayload{Answered: all, NotesApplied: len(p.Notes)}})

	// Next launch: the daemon holds a note for every pane of the first batch.
	rf2 := rf
	rf2.Panes = append([]PaneInfo(nil), rf.Panes...)
	for i := range rf2.Panes {
		if sent1[rf2.Panes[i].ID] {
			rf2.Panes[i].NoteRev = 1
		}
	}
	next, _, remote2 := twoDestModel(t)
	next.SetSharedImportMarker(config.SharedImportPath())
	next = updateNoWait(t, next, rf2)
	p2, id2 := importPayload(t, remote2)
	if len(p2.Notes) != n-len(sent1) {
		t.Errorf("next launch sent %d notes, want the %d deferred", len(p2.Notes), n-len(sent1))
	}
	for _, nt := range p2.Notes {
		if sent1[nt.PaneID] {
			t.Errorf("next launch re-sent %s, which the daemon already has", nt.PaneID)
		}
	}
	// Nothing deferred this time, so the answer finishes the kind.
	next = updateNoWait(t, next, sharedImportRespMsg{dest: "hostA", id: id2, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindNotes}}})
	if !loadImportMarker(config.SharedImportPath()).Dests[config.DestFileKey("hostA")].Notes {
		t.Error("notes kind still pending after every note was sent")
	}
}

// N-2: after maxImportErrors error replies, a held assign on a daemon that
// lists a group is sent, not stripped by the authoritative frame and held
// forever. The marker is untouched: next launch still imports.
func TestUpdate_ImportErrorCap_DaemonListingGroups_SendsTheHeldAssign(t *testing.T) {
	m, local, _ := importTestModel(t)
	rev := uint64(1)
	m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", ""))
	for i := 1; i <= maxImportErrors; i++ {
		_, id := importPayload(t, local)
		m = updateNoWait(t, m, sharedImportErrMsg{dest: "", id: id, text: "bad_payload: x"})
		rev++
		m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", ""))
	}
	runCmd(m.moveProjectToGroup("", "proj-1", "Empty"))
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 0 {
		t.Fatalf("set_project_group sent %d times while the daemon lists no group", n)
	}
	rev++
	m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", "", "Other"))
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 1 {
		t.Fatalf("set_project_group sent %d times, want the held assign once the daemon lists a group", n)
	}
	if p := decodeSetProjectGroup(t, local); p.ProjectID != "proj-1" || p.Group != "Empty" {
		t.Errorf("sent %+v", p)
	}
	if mk := loadImportMarker(config.SharedImportPath()); mk.Dests["local"].Groups {
		t.Error("a capped import marked groups done; the next launch would never import")
	}
}

// N-2, at the cap itself: a daemon that already lists a group opens sends on
// the third error reply.
func TestUpdate_ImportErrorCap_AlreadyListingGroups_OpensOnTheLastError(t *testing.T) {
	m, local, _ := importTestModel(t)
	rev := uint64(1)
	m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", "", "Other"))
	runCmd(m.moveProjectToGroup("", "proj-1", "Empty"))
	for i := 1; i <= maxImportErrors; i++ {
		_, id := importPayload(t, local)
		m = updateNoWait(t, m, sharedImportErrMsg{dest: "", id: id, text: "bad_payload: x"})
		if i < maxImportErrors {
			rev++
			m = updateNoWait(t, m, sharedFrame("r", rev, "proj-1", "", "Other"))
		}
	}
	if n := countSent(local, ipc.MsgSetProjectGroup); n != 1 {
		t.Errorf("set_project_group sent %d times, want the held assign on the last error", n)
	}
}

// N-3: disconnecting a host forgets its in-flight import, so re-adding it in
// the same session sends the import again.
func TestDisconnectDest_ForgetsTheImportInFlight(t *testing.T) {
	m, _, _ := importTestModel(t)
	rf := sharedFrame("q", 1, "proj-2", "")
	rf.Dest = "hostA"
	m = updateNoWait(t, m, rf)
	if !m.importAsked["hostA"] {
		t.Fatal("setup: no import asked for hostA")
	}
	m.disconnectDest("hostA")
	for _, p := range m.pendingImports {
		if p.dest == "hostA" {
			t.Error("a pending import for the disconnected host survived")
		}
	}
	if m.importAsked["hostA"] {
		t.Error("importAsked survived the disconnect; a re-added host never re-sends")
	}
}

// refusedGroupsAnswer is the daemon's answer to an import it refused because
// it already held groups: groups answered, not applied.
func refusedGroupsAnswer(id string) sharedImportRespMsg {
	return sharedImportRespMsg{dest: "", id: id, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups, ipc.ImportKindRecent}}}
}

func loadBackup(t *testing.T, path string) projectGroupsFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no backup at %s: %v", path, err)
	}
	var f projectGroupsFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("backup does not parse: %v", err)
	}
	return f
}

// R-8 (1): a second client joins a daemon that already holds groups. Its
// import is refused and the frame replaces its cached members, so the
// membership the daemon does not share is backed up — old members only for
// this destination — and the cache is still replaced.
func TestUpdate_RefusedImport_DifferingMembership_BacksUpTheOldGroups(t *testing.T) {
	m, local, _ := importTestModel(t)
	m.groups.Groups[0].Collapsed = true
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "Other", "Other"))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, refusedGroupsAnswer(id))
	f := loadBackup(t, groupsBackupPath(""))
	if len(f.Groups) != 1 || f.Groups[0].Name != "Infra" || !f.Groups[0].Collapsed ||
		len(f.Groups[0].Members) != 1 || f.Groups[0].Members[0] != (groupMember{Dest: "", ID: "proj-1"}) {
		t.Errorf("backup = %+v, want Infra (collapsed) holding the local proj-1 only", f.Groups)
	}
	if info, err := os.Stat(groupsBackupPath("")); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", info.Mode().Perm())
	}
	if g := m.groups.groupOf("", "proj-1"); g != m.groups.indexOf("Other") {
		t.Errorf("the cache was not replaced: proj-1 in group %d, want Other", g)
	}
	if !strings.Contains(m.flashText, "came from another client") || !strings.Contains(m.flashText, filepath.Base(groupsBackupPath(""))) {
		t.Errorf("flash = %q", m.flashText)
	}
}

// R-8 (2): the daemon files every cached member the same way — nothing would
// be lost, so no backup.
func TestUpdate_RefusedImport_IdenticalMembership_NoBackup(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "infra", "infra"))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, refusedGroupsAnswer(id))
	if _, err := os.Stat(groupsBackupPath("")); !os.IsNotExist(err) {
		t.Errorf("a backup was written with nothing to lose (stat err %v)", err)
	}
}

// R-8 (3): an applied import gave the daemon this client's groups.
func TestUpdate_AppliedImport_NoBackup(t *testing.T) {
	m, local, _ := importTestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
	_, id := importPayload(t, local)
	resp := refusedGroupsAnswer(id)
	resp.resp.GroupsApplied = true
	// The daemon's next frame already disagrees (another client moved it):
	// still no backup, the import was applied.
	m = updateNoWait(t, m, sharedFrame("r", 2, "proj-1", "Other", "Infra", "Empty", "Other"))
	m = updateNoWait(t, m, resp)
	if _, err := os.Stat(groupsBackupPath("")); !os.IsNotExist(err) {
		t.Errorf("a backup was written for an applied import (stat err %v)", err)
	}
}

// R-8 (4): an earlier backup is the one holding the pre-shared state; it is
// never overwritten.
func TestUpdate_RefusedImport_ExistingBackupIsNeverOverwritten(t *testing.T) {
	m, local, _ := importTestModel(t)
	path := groupsBackupPath("")
	earlier := []byte(`{"version":1,"groups":[{"name":"Earlier","collapsed":false,"members":[]}]}`)
	if err := os.WriteFile(path, earlier, 0o600); err != nil {
		t.Fatal(err)
	}
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "Other", "Other"))
	_, id := importPayload(t, local)
	m = updateNoWait(t, m, refusedGroupsAnswer(id))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(earlier) {
		t.Errorf("the earlier backup was overwritten: %s", got)
	}
}
