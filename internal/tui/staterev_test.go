package tui

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// connectedTestModelCapturingSends builds the smallest Model that can drive a
// workspace_state broadcast through Update on the local dest (""), with a
// fakeConn recording every send. The conn's receive side is closed up front:
// every test here drives IPC arrivals explicitly through Update, and the
// WorkspaceStateMsg/stateDecodeFailedMsg arms re-arm listenForMessages, which
// would otherwise block forever on an empty channel — closing it makes that
// re-armed listen resolve at once (linkLostMsg, discarded by runCmd), the
// same pattern TestBecomingMaster_ResendsSizes uses.
func connectedTestModelCapturingSends(t *testing.T) (Model, *fakeConn) {
	t.Helper()
	conn := newFakeConn()
	close(conn.recv)
	m := Model{
		cfg:            config.Default(),
		client:         conn,
		tabDragFromIdx: -1,
	}
	return m, conn
}

// connectedTestModel is connectedTestModelCapturingSends for tests that don't
// assert on the wire.
func connectedTestModel(t *testing.T) Model {
	t.Helper()
	m, _ := connectedTestModelCapturingSends(t)
	return m
}

// stateMsg builds a minimal full-state broadcast for the local dest: one
// project ("proj-1", reused across calls by ID), one tab named tabID with one
// pane. rev/runID drive acceptStateRev; rev 0 (with runID "") reproduces an
// older daemon that numbers nothing.
func stateMsg(runID string, rev uint64, tabID string) WorkspaceStateMsg {
	paneID := tabID + "-pane"
	return WorkspaceStateMsg{
		Dest: "", RunID: runID, Rev: rev,
		ActiveProject: "proj-1", ActiveTab: tabID,
		Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{tabID}}},
		Tabs:     []TabInfo{{ID: tabID, Name: "Shell", ProjectID: "proj-1", Panes: []string{paneID}}},
		Panes:    []PaneInfo{{ID: paneID, TabID: tabID, Type: "terminal"}},
	}
}

// updateWith drives one message through Update and returns the resulting
// Model, running the returned Cmd tree so its side effects (sends, the
// re-armed listen) actually happen.
func updateWith(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	out, cmd := m.Update(msg)
	runCmd(cmd)
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("Update(%T) returned %T, want Model", msg, out)
	}
	return got
}

// hasTab reports whether any project holds a tab with this ID.
func hasTab(m Model, tabID string) bool {
	for _, tab := range m.allTabs() {
		if tab.ID == tabID {
			return true
		}
	}
	return false
}

// countSent counts how many sent messages carry the given type.
func countSent(conn *fakeConn, msgType string) int {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	n := 0
	for _, msg := range conn.sent {
		if msg.Type == msgType {
			n++
		}
	}
	return n
}

// stateFromMap round-trips a hand-built map through JSON into ipc.WorkspaceState
// — the shape parseWorkspaceState now consumes — so the many existing tests
// that build a raw map[string]any keep exercising the same wire keys.
func stateFromMap(t *testing.T, raw map[string]any) ipc.WorkspaceState {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw workspace state: %v", err)
	}
	var ws ipc.WorkspaceState
	if err := json.Unmarshal(data, &ws); err != nil {
		t.Fatalf("unmarshal into ipc.WorkspaceState: %v", err)
	}
	return ws
}

func TestUpdate_StaleRev_IsDropped(t *testing.T) {
	m := connectedTestModel(t) // local dest "" connected
	m = updateWith(t, m, stateMsg("run-a", 5, "tab-new"))
	m = updateWith(t, m, stateMsg("run-a", 4, "tab-old"))
	if !hasTab(m, "tab-new") || hasTab(m, "tab-old") {
		t.Error("an older rev from the same run replaced newer state")
	}
}

// A dropped frame must have NO side effect — acceptStateRev has to run before
// anything else in the WorkspaceStateMsg arm, noteWorkspaceState included, or
// a stale frame's own "update" field would still overwrite what the last
// APPLIED frame said. Mutation-checked: moving the acceptStateRev call below
// noteWorkspaceState in Update leaves every other test in this file green.
func TestUpdate_StaleRev_NoSideEffectsBeforeTheDrop(t *testing.T) {
	m := connectedTestModel(t)
	baseline := stateMsg("run-a", 5, "tab-new")
	m = updateWith(t, m, baseline)
	if got := m.updateInfos[""]; got != nil {
		t.Fatalf("precondition: updateInfos[\"\"] = %+v, want nil", got)
	}

	stale := stateMsg("run-a", 4, "tab-old")
	stale.Update = &ipc.UpdateInfo{LatestVersion: "9.9.9"}
	m = updateWith(t, m, stale)

	if got := m.updateInfos[""]; got != nil {
		t.Errorf("updateInfos[\"\"] = %+v, want nil — a dropped frame's own Update field reached noteWorkspaceState", got)
	}
}

func TestUpdate_NewRunID_ResetsRev(t *testing.T) {
	m := connectedTestModel(t)
	m = updateWith(t, m, stateMsg("run-a", 50, "tab-a"))
	m = updateWith(t, m, stateMsg("run-b", 1, "tab-b"))
	if !hasTab(m, "tab-b") {
		t.Error("rev 1 from a restarted daemon was dropped as stale")
	}
}

func TestUpdate_RevGap_Applies(t *testing.T) {
	m := connectedTestModel(t)
	m = updateWith(t, m, stateMsg("run-a", 1, "tab-a"))
	m = updateWith(t, m, stateMsg("run-a", 9, "tab-b"))
	if !hasTab(m, "tab-b") {
		t.Error("a gap dropped a full snapshot")
	}
}

func TestUpdate_NoRev_AppliesEveryFrame(t *testing.T) {
	m := connectedTestModel(t)
	m = updateWith(t, m, stateMsg("", 0, "tab-a"))
	m = updateWith(t, m, stateMsg("", 0, "tab-b"))
	if !hasTab(m, "tab-b") {
		t.Error("an old daemon's unnumbered frame was dropped")
	}
}

// Review focus 1.
func TestUpdate_UndecodableState_DropsAndAsksOnce(t *testing.T) {
	m, sent := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, stateMsg("run-a", 3, "tab-a"))
	m = updateWith(t, m, stateDecodeFailedMsg{Dest: ""})
	m = updateWith(t, m, stateDecodeFailedMsg{Dest: ""})
	if !hasTab(m, "tab-a") {
		t.Fatal("an undecodable frame emptied the workspace")
	}
	if n := countSent(sent, ipc.MsgStateReq); n != 1 {
		t.Errorf("state_req sent %d times, want exactly 1 (single-flight)", n)
	}
}

func TestUpdate_UndecodableState_OldDaemon_NoStateReq(t *testing.T) {
	m, sent := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, stateMsg("", 0, "tab-a"))
	m = updateWith(t, m, stateDecodeFailedMsg{Dest: ""})
	if n := countSent(sent, ipc.MsgStateReq); n != 0 {
		t.Errorf("state_req sent to a daemon that never numbered a frame")
	}
}

// decode fail -> 1 state_req; an applied rev 4 clears the pending request; a
// second decode fail -> a 2nd state_req, now pending again. A
// stateReqTimeoutMsg carrying an OLD generation must not clear that newer
// pending entry; one carrying the CURRENT generation must.
func TestUpdate_StateReqPending_ClearsOnAppliedFrameAndOnTimeout(t *testing.T) {
	m, sent := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, stateMsg("run-a", 3, "tab-a"))

	m = updateWith(t, m, stateDecodeFailedMsg{Dest: ""})
	if n := countSent(sent, ipc.MsgStateReq); n != 1 {
		t.Fatalf("state_req sent %d times, want 1", n)
	}
	oldGen, pending := m.stateReqGen[""]
	if !pending {
		t.Fatal("no pending state_req recorded after the first decode failure")
	}

	m = updateWith(t, m, stateMsg("run-a", 4, "tab-b"))
	if !hasTab(m, "tab-b") {
		t.Fatal("the applied frame did not reach the workspace")
	}
	if _, pending := m.stateReqGen[""]; pending {
		t.Fatal("an applied frame must clear the outstanding state_req")
	}

	m = updateWith(t, m, stateDecodeFailedMsg{Dest: ""})
	if n := countSent(sent, ipc.MsgStateReq); n != 2 {
		t.Fatalf("state_req sent %d times, want 2 (a second round after the first cleared)", n)
	}
	newGen, pending := m.stateReqGen[""]
	if !pending {
		t.Fatal("no pending state_req recorded after the second decode failure")
	}
	if newGen == oldGen {
		t.Fatal("the second state_req reused the first generation — a stale timeout could clear it")
	}

	m = updateWith(t, m, stateReqTimeoutMsg{Dest: "", Gen: oldGen})
	if _, pending := m.stateReqGen[""]; !pending {
		t.Error("a timeout carrying an OLD generation cleared the current pending request")
	}

	m = updateWith(t, m, stateReqTimeoutMsg{Dest: "", Gen: newGen})
	if _, pending := m.stateReqGen[""]; pending {
		t.Error("a timeout carrying the CURRENT generation did not clear the pending request")
	}
}

func TestArmReattachReset_ForgetsStateMark(t *testing.T) {
	m := connectedTestModel(t)
	m = updateWith(t, m, stateMsg("run-a", 40, "tab-a"))
	m.armReattachReset("")
	m = updateWith(t, m, stateMsg("run-a", 1, "tab-b"))
	if !hasTab(m, "tab-b") {
		t.Error("after a reattach, rev 1 was still compared against the old mark")
	}
}

// The listen loop is where an undecodable workspace_state is turned into
// stateDecodeFailedMsg rather than an empty state, and where Origin becomes
// Dest — see TestListenForMessages_PluginListCarriesItsOrigin for the same
// seam with a decodable response.
func TestListen_UndecodableWorkspaceState_ReturnsDecodeFailed(t *testing.T) {
	resp, err := ipc.NewMessage(ipc.MsgWorkspaceState, map[string]any{"tabs": "oops"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	resp.Origin = "hostA"

	m := Model{client: &oneShotClient{msg: resp}}

	msg := m.listenForMessages()()
	got, ok := msg.(stateDecodeFailedMsg)
	if !ok {
		t.Fatalf("msg is %T, want stateDecodeFailedMsg", msg)
	}
	if got.Dest != "hostA" {
		t.Errorf("Dest = %q, want %q — the answering daemon was not carried through", got.Dest, "hostA")
	}
}
