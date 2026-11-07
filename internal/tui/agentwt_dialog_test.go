package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// agentWTModel is a model showing the view with the given entries, in a
// project rooted at /proj.
func agentWTModel(entries ...ipc.AgentWorktreeInfo) Model {
	m := modelWithPanes(&PaneModel{ID: "pane-plain", CWD: "/proj"})
	m.cfg = config.Default()
	m.projects[0].RootDir = "/proj"
	m.dialog = dialogAgentWorktrees
	m.agentWT = agentWTView{entries: entries}
	return m
}

func wtInfo(id, repo, branch string) ipc.AgentWorktreeInfo {
	return ipc.AgentWorktreeInfo{ID: id, Kind: "worktree", Repo: repo, Path: "/w/" + id, Branch: branch,
		PaneID: "pane-x", PaneName: "claude", Source: "claude"}
}

// The active project's repository comes first.
func TestAgentWTDialog_ListsGroupedByRepoActiveFirst(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/other", "o1"), wtInfo("b", "/proj", "p1"))
	rows := m.visibleAgentWT()
	if len(rows) != 2 || rows[0].ID != "b" {
		t.Errorf("rows = %+v, want /proj's entry first", rows)
	}
}

// The view never claims the agent MADE it.
func TestAgentWTDialog_LabelSaysAppearedWhile(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	out := stripANSI(m.renderAgentWTDialog())
	if !strings.Contains(out, "while claude") || strings.Contains(strings.ToLower(out), "made by") {
		t.Errorf("list wording:\n%s", out)
	}
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	if out := stripANSI(m.renderConfirmDialog()); !strings.Contains(out, "Appeared while claude ran") {
		t.Errorf("confirm wording:\n%s", out)
	}
}

// Picking an entry opens the close dialog's own rows: the remote row needs
// the switch, and only `y` sends it; the request names the entry by id.
func TestAgentWTDialog_DetailReusesCloseRowsAndRemoteNeedsY(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	m.cfg.Worktree.RemoteBranchDelete = true
	fake := &fakeSender{}
	m.client = fake
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	m.applyAgentWTFacts(agentWTFactsMsg{Gen: m.confirmWorktreeGen, Resp: ipc.AgentWorktreeFactsRespPayload{
		ID: "a", Status: ipc.WorktreeStatus{Changes: 1, Files: []ipc.WorktreeFile{{Code: "??", Path: "x.txt"}},
			Branch: "feat/a", BranchDeletable: true, Remote: "origin", RemoteBranch: "feat/a"},
	}})
	if out := stripANSI(m.renderConfirmDialog()); !strings.Contains(out, "x.txt") {
		t.Fatalf("the detail does not list the files:\n%s", out)
	}

	m = confirmPress(m, " ") // worktree
	m = confirmPress(m, "down")
	m = confirmPress(m, "down")
	m = confirmPress(m, " ") // remote
	fake.sent = nil
	m = confirmPress(m, "enter")
	if len(fake.sent) != 0 || m.dialog != dialogConfirm {
		t.Fatalf("Enter acted with a remote delete armed (sent %d)", len(fake.sent))
	}

	mdl, cmd := m.handleConfirmKey(keyPressFor("y"))
	m = mdl.(Model)
	runCmd(cmd)
	var req ipc.AgentWorktreeActReqPayload
	if !decodeSent(t, fake, ipc.MsgAgentWorktreeActReq, &req) {
		t.Fatal("y sent no act request")
	}
	if req.ID != "a" || !req.RemoveWorktree || !req.DeleteRemoteBranch || req.ConfirmedBranch != "feat/a" {
		t.Errorf("request = %+v", req)
	}
	if m.dialog != dialogAgentWorktrees {
		t.Error("the confirm did not return to the list")
	}
}

// Review Focus 5: nothing is removed before the file list is on screen. Enter
// with the worktree ticked waits while the facts are still on their way.
func TestAgentWTDialog_EnterWaitsForTheFacts(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	fake := &fakeSender{}
	m.client = fake
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	fake.sent = nil

	m = confirmPress(m, " ")
	m = confirmPress(m, "enter")

	if len(fake.sent) != 0 || m.dialog != dialogConfirm {
		t.Errorf("Enter acted before the facts arrived (sent %d)", len(fake.sent))
	}
}

// A list request nobody answers ends with a reason, never an endless Loading.
func TestAgentWTDialog_TimeoutShowsReason(t *testing.T) {
	m := agentWTModel()
	m.agentWT.gen, m.agentWT.loading = "g1", true
	m.applyAgentWTListTimeout(agentWTListTimeoutMsg{gen: "g1"})
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "did not answer") {
		t.Errorf("no reason after the timeout:\n%s", out)
	}
}

// The box never outgrows a 24-row terminal, however long the record.
func TestAgentWTDialog_FitsTerminal(t *testing.T) {
	var entries []ipc.AgentWorktreeInfo
	for i := 0; i < 40; i++ {
		entries = append(entries, wtInfo(fmt.Sprintf("e%02d", i), fmt.Sprintf("/repo%d", i%3), fmt.Sprintf("feat/%02d", i)))
	}
	m := agentWTModel(entries...)
	m.width, m.height = 100, 24
	lines := strings.Count(m.renderAgentWTDialog(), "\n") + 1
	if box := lines + dialogVChrome; box > m.height {
		t.Errorf("box is %d rows in a %d-row terminal", box, m.height)
	}
}

// Gone entries are hidden until asked for.
func TestAgentWTDialog_HidesGoneByDefault(t *testing.T) {
	gone := wtInfo("g", "/proj", "old")
	gone.Gone = true
	m := agentWTModel(gone, wtInfo("a", "/proj", "feat/a"))
	if rows := m.visibleAgentWT(); len(rows) != 1 || rows[0].ID != "a" {
		t.Errorf("rows = %+v, want only the live entry", rows)
	}
	m = applyModel(m.handleAgentWTKey(keyPressFor("g")))
	if rows := m.visibleAgentWT(); len(rows) != 2 {
		t.Errorf("g did not show gone entries: %+v", rows)
	}
}
