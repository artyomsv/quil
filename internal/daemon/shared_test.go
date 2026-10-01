package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

func helloAsScript(t *testing.T, client *ipc.Client) {
	t.Helper()
	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{Kind: "script", Proto: 1, PID: os.Getpid()})
}

func groupsOf(t *testing.T, d *Daemon) []string {
	t.Helper()
	groups, _ := d.session.SharedSnapshot()
	return groups
}

func TestHandleMessage_SetProjectGroup_FilesProjectAndAppendsName(t *testing.T) {
	d, client := mcpTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	resp := roundTrip(t, client, ipc.MsgSetProjectGroup, ipc.MsgProjectOpResp,
		ipc.SetProjectGroupPayload{ProjectID: p.ID, Group: "  Infra "})
	if op := decodeInto[ipc.OpRespPayload](t, resp); !op.OK {
		t.Fatalf("set_project_group refused: %+v", op)
	}
	ws := d.buildWorkspaceState()
	if got := groupsOf(t, d); len(got) != 1 || got[0] != "Infra" {
		t.Errorf("groups = %v, want [Infra] (trimmed, name appended)", got)
	}
	found := false
	for _, ps := range ws.Projects {
		if ps.ID == p.ID && ps.Group == "Infra" {
			found = true
		}
	}
	if !found || !ws.SharedData {
		t.Errorf("frame: shared_data=%v projects=%+v", ws.SharedData, ws.Projects)
	}
}

func TestHandleMessage_SetProjectGroup_ExistingNameAdoptsItsSpelling(t *testing.T) {
	d, client := mcpTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: "Infra"})
	roundTrip(t, client, ipc.MsgSetProjectGroup, ipc.MsgProjectOpResp, ipc.SetProjectGroupPayload{ProjectID: p.ID, Group: "infra"})
	if got := groupsOf(t, d); len(got) != 1 || got[0] != "Infra" {
		t.Errorf("groups = %v, want one name Infra", got)
	}
	for _, ps := range d.buildWorkspaceState().Projects {
		if ps.ID == p.ID && ps.Group != "Infra" {
			t.Errorf("project group = %q, want the list's spelling", ps.Group)
		}
	}
}

func TestHandleMessage_SetProjectGroup_EmptyUngroupsAndKeepsName(t *testing.T) {
	d, client := mcpTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	roundTrip(t, client, ipc.MsgSetProjectGroup, ipc.MsgProjectOpResp, ipc.SetProjectGroupPayload{ProjectID: p.ID, Group: "g"})
	roundTrip(t, client, ipc.MsgSetProjectGroup, ipc.MsgProjectOpResp, ipc.SetProjectGroupPayload{ProjectID: p.ID, Group: ""})
	for _, ps := range d.buildWorkspaceState().Projects {
		if ps.ID == p.ID && ps.Group != "" {
			t.Errorf("project still grouped: %q", ps.Group)
		}
	}
	if got := groupsOf(t, d); len(got) != 1 {
		t.Errorf("an empty group must survive: %v", got)
	}
}

func TestHandleMessage_SetProjectGroup_UnknownProjectRefused(t *testing.T) {
	_, client := mcpTestDaemon(t)
	resp := roundTrip(t, client, ipc.MsgSetProjectGroup, ipc.MsgProjectOpResp, ipc.SetProjectGroupPayload{ProjectID: "proj-nope", Group: "g"})
	if op := decodeInto[ipc.OpRespPayload](t, resp); op.OK || op.Error == "" {
		t.Errorf("want refusal with a reason, got %+v", op)
	}
}

// Review focus 1.
func TestHandleMessage_GroupOp_RefusesStrippedRunes(t *testing.T) {
	d, client := mcpTestDaemon(t)
	// Built from the code point (U+202E, RIGHT-TO-LEFT OVERRIDE) rather than
	// an escape literal in source: this file must never carry the raw rune.
	bidiOverride := "a" + string(rune(0x202e)) + "b"
	for _, name := range []string{"", "   ", bidiOverride, "a\x1b[31mb", "a\u009bb", "abcdefghijklmnopqrstuvwxyzabcdefg"} {
		resp := roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: name})
		if op := decodeInto[ipc.OpRespPayload](t, resp); op.OK {
			t.Errorf("name %q accepted", name)
		}
	}
	if got := groupsOf(t, d); len(got) != 0 {
		t.Errorf("a refused name was stored: %v", got)
	}
}

func TestHandleMessage_GroupOp_CreateRenameDelete(t *testing.T) {
	d, client := mcpTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: "a"})
	dup := roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: "A"})
	if op := decodeInto[ipc.OpRespPayload](t, dup); op.OK {
		t.Error("case-insensitive duplicate accepted")
	}
	roundTrip(t, client, ipc.MsgSetProjectGroup, ipc.MsgProjectOpResp, ipc.SetProjectGroupPayload{ProjectID: p.ID, Group: "a"})
	roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpRename, Name: "a", NewName: "b"})
	if got := groupsOf(t, d); len(got) != 1 || got[0] != "b" {
		t.Errorf("after rename groups = %v", got)
	}
	for _, ps := range d.buildWorkspaceState().Projects {
		if ps.ID == p.ID && ps.Group != "b" {
			t.Errorf("rename did not follow the project: %q", ps.Group)
		}
	}
	roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpDelete, Name: "B"})
	if got := groupsOf(t, d); len(got) != 0 {
		t.Errorf("after delete groups = %v", got)
	}
	for _, ps := range d.buildWorkspaceState().Projects {
		if ps.ID == p.ID && ps.Group != "" {
			t.Errorf("delete did not ungroup: %q", ps.Group)
		}
	}
	missing := roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpDelete, Name: "zzz"})
	if op := decodeInto[ipc.OpRespPayload](t, missing); op.OK {
		t.Error("deleting an unknown group answered ok")
	}
}

func TestHandleMessage_GroupOp_CapsAt64(t *testing.T) {
	d, client := mcpTestDaemon(t)
	for i := 0; i < ipc.MaxGroupsPerDaemon; i++ {
		roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: "g" + time.Duration(i).String()})
	}
	resp := roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: "one-too-many"})
	if op := decodeInto[ipc.OpRespPayload](t, resp); op.OK {
		t.Error("65th group accepted")
	}
	if got := groupsOf(t, d); len(got) != ipc.MaxGroupsPerDaemon {
		t.Errorf("groups = %d", len(got))
	}
}

func TestHandleMessage_GroupOp_BadPayloadAnsweredToHelloedConn(t *testing.T) {
	_, client := mcpTestDaemon(t)
	helloAsScript(t, client)
	for _, typ := range []string{ipc.MsgSetProjectGroup, ipc.MsgGroupOp} {
		msg := &ipc.Message{Type: typ, ID: "bad-" + typ, Payload: []byte(`"not an object"`)}
		if err := client.Send(msg); err != nil {
			t.Fatal(err)
		}
		waitFrameWithID(t, client, msg.ID, 5*time.Second)
	}
}

func TestHandleMessage_GroupChange_BroadcastsOnce(t *testing.T) {
	d, client := mcpTestDaemon(t)
	before := d.buildWorkspaceState().Rev
	roundTrip(t, client, ipc.MsgGroupOp, ipc.MsgGroupOpResp, ipc.GroupOpPayload{Op: ipc.GroupOpCreate, Name: "a"})
	// The coalescer fires 50 ms after the op; wait past it, then count frames
	// carrying the group. Exactly one broadcast — the TUI's must-deliver
	// queue is 64 slots.
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer client.SetReadDeadline(time.Time{})
	frames := 0
	for {
		m, err := client.Receive()
		if err != nil {
			break
		}
		if m.Type == ipc.MsgWorkspaceState {
			ws := decodeInto[ipc.WorkspaceState](t, m)
			if ws.Rev > before && len(ws.Groups) == 1 {
				frames++
			}
		}
	}
	if frames != 1 {
		t.Errorf("got %d state frames for one group op, want 1", frames)
	}
}

func TestRecordRecentCWD_CapOrderDedup(t *testing.T) {
	sm := NewSessionManager(1024)
	for _, d := range []string{"/a", "/b", "/a", "/c", "/d", "/e", "/f"} {
		sm.RecordRecentCWD(d)
	}
	_, got := sm.SharedSnapshot()
	want := []string{"/f", "/e", "/d", "/c", "/a"}
	if len(got) != len(want) {
		t.Fatalf("recent = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recent[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	sm.RecordRecentCWD("")
	if _, again := sm.SharedSnapshot(); len(again) != 5 {
		t.Error("a blank dir was recorded")
	}
}

func TestHandleMessage_CreatePane_RecordsRequestedCWDNotDefault(t *testing.T) {
	d, client := mcpTestDaemon(t)
	dir := t.TempDir()
	tab := d.session.CreateTab("t")
	// A named folder is recorded ...
	roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp, ipc.CreatePaneReqPayload{TabID: tab.ID, CWD: dir})
	_, recent := d.session.SharedSnapshot()
	if len(recent) != 1 || !samePath(recent[0], dir) {
		t.Fatalf("recent = %v, want [%s]", recent, dir)
	}
	// ... a defaulted one (empty request) is not.
	roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp, ipc.CreatePaneReqPayload{TabID: tab.ID})
	if _, again := d.session.SharedSnapshot(); len(again) != 1 {
		t.Errorf("a defaulted CWD was recorded: %v", again)
	}
	// ... and one that does not exist is not.
	roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp, ipc.CreatePaneReqPayload{TabID: tab.ID, CWD: dir + "/gone"})
	if _, again := d.session.SharedSnapshot(); len(again) != 1 {
		t.Errorf("an unusable CWD was recorded: %v", again)
	}
}

// TestHandleMessage_CreatePane_TUIPathRecordsRealCWD covers the ORDINARY
// create_pane arm (Ctrl+N and everything else that spawns a pane into an
// existing tab), which is a fire-and-forget message with no create_pane_resp
// — the only recording site the CreatePane_RecordsRequestedCWDNotDefault
// test above does NOT exercise, since that one goes through create_pane_req
// (the MCP path). Without this test, reverting the ordinary arm's call back
// to the non-recording resolveRequestedCWD fails nothing.
func TestHandleMessage_CreatePane_TUIPathRecordsRealCWD(t *testing.T) {
	d, client := mcpTestDaemon(t)
	dir := t.TempDir()
	tab := d.session.CreateTab("t")
	sendNoID(t, client, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, CWD: dir})
	roundTrip(t, client, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{}) // ordered on one conn: the create ran
	_, recent := d.session.SharedSnapshot()
	if len(recent) != 1 || !samePath(recent[0], dir) {
		t.Errorf("recent = %v, want [%s]", recent, dir)
	}
}

// TestHandleMessage_CreatePane_TUIPathEmptyCWDRecordsNothing is the
// defaulted-CWD half of the test above: an empty request falls back to
// d.defaultCWD(conn), which must never be recorded as something the user
// picked.
func TestHandleMessage_CreatePane_TUIPathEmptyCWDRecordsNothing(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")
	sendNoID(t, client, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID})
	roundTrip(t, client, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	if _, recent := d.session.SharedSnapshot(); len(recent) != 0 {
		t.Errorf("a defaulted CWD was recorded: %v", recent)
	}
}

// TestHandleMessage_CreatePane_OverlayDoesNotRecordItsRepoRoot: the
// lazygit/hunk overlay (internal/tui/overlay.go) sends an ordinary
// create_pane naming its host tab's repo root as CWD with Overlay: true.
// That is not a folder the user or agent picked, so every Alt+G
// must not push the repo root to the front of the recent-folder list.
func TestHandleMessage_CreatePane_OverlayDoesNotRecordItsRepoRoot(t *testing.T) {
	d, client := mcpTestDaemon(t)
	dir := t.TempDir()
	tab := d.session.CreateTab("t")
	sendNoID(t, client, ipc.MsgCreatePane, ipc.CreatePanePayload{
		TabID:   tab.ID,
		CWD:     dir,
		Type:    "terminal",
		Overlay: true,
	})
	roundTrip(t, client, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{}) // ordered: the overlay create ran
	if _, recent := d.session.SharedSnapshot(); len(recent) != 0 {
		t.Errorf("overlay create recorded its repo root: %v", recent)
	}
}

func TestHandleMessage_CreateTab_RecordsFirstPaneCWD(t *testing.T) {
	d, client := mcpTestDaemon(t)
	dir := t.TempDir()
	sendNoID(t, client, ipc.MsgCreateTab, ipc.CreateTabPayload{Name: "n", FirstPane: &ipc.FirstPaneSpec{CWD: dir}})
	roundTrip(t, client, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{}) // ordered on one conn: the create ran
	_, recent := d.session.SharedSnapshot()
	if len(recent) != 1 || !samePath(recent[0], dir) {
		t.Errorf("recent = %v, want [%s]", recent, dir)
	}
}

// TestHandleMessage_CreatePane_WorktreeRecordsBrowsedDirNotWorktree sends a
// TUI-shaped create_pane naming a worktree through the stubbed
// addWorktreeFn, and checks that what lands in the recent-folder list is the
// browsed directory the request named — not the checkout path the stub
// answered with. A worktree create's pane does not spawn in the browsed
// directory, but the client still recorded it there, and so must the daemon.
func TestHandleMessage_CreatePane_WorktreeRecordsBrowsedDirNotWorktree(t *testing.T) {
	d, client := mcpTestDaemon(t)
	dir := t.TempDir()
	tab := d.session.CreateTab("t")

	// createPaneInWorktree stats the checkout after the (stubbed) add, so it
	// must actually exist on disk — same setup TestWorktreeAdd_ClassifiesWrappedSubdirError
	// uses.
	checkout := gitworktree.DerivePath(dir, "feat/x")
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	stubAdd(t, func(context.Context, string, string, string) error { return nil })

	resp := roundTrip(t, client, ipc.MsgCreatePane, ipc.MsgCreatePaneResp, ipc.CreatePanePayload{
		TabID: tab.ID,
		CWD:   dir,
		Worktree: &ipc.WorktreeSpec{
			RepoRoot: dir,
			Branch:   "feat/x",
		},
	})
	respPayload := decodeInto[ipc.CreatePaneRespPayload](t, resp)
	if respPayload.Error != "" {
		t.Fatalf("create_pane_resp error: %s", respPayload.Error)
	}
	_, recent := d.session.SharedSnapshot()
	if len(recent) != 1 || !samePath(recent[0], dir) {
		t.Errorf("recent = %v, want [%s] (the browsed dir, not the worktree path)", recent, dir)
	}
	for _, r := range recent {
		if samePath(r, checkout) {
			t.Errorf("worktree checkout path recorded instead of browsed dir: %v", recent)
		}
	}
}
