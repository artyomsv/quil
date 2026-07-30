package tui

import (
	"fmt"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// A response for a directory the user has already left must be dropped. Alt+G
// can be pressed again before the first answer lands, and acting on a
// superseded one would open an overlay for the wrong repository.
func TestApplyGitRepos_StaleResponseIgnored(t *testing.T) {
	t.Parallel()
	m, fake, tab := overlayTestModel(t, "/a")
	m.repoScan = repoScanState{cwd: "/a", tabID: tab.ID}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/b", Repos: []string{"/b"}}))

	if m.repoScan.cwd != "/a" {
		t.Error("a stale response cleared the in-flight scan")
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgCreatePane {
			t.Fatal("a stale response created an overlay; it would be for the wrong repo")
		}
	}
}

// TestApplyGitRepos_ErrorIsNotReportedAsNoRepo is the distinction the whole
// phase is about.
//
// A failed scan is not evidence that there is no repository. Flashing "no git
// repo here" because a scan timed out is a wrong answer stated confidently —
// exactly the class of bug that made Alt+G report on the laptop's filesystem
// while claiming to describe the remote host.
func TestApplyGitRepos_ErrorIsNotReportedAsNoRepo(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "/a")
	m.repoScan = repoScanState{cwd: "/a", tabID: tab.ID}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{
		CWD:   "/a",
		Error: "another repository scan is already running",
	}))

	if m.flashText == "" {
		t.Fatal("a failed scan flashed nothing; the keypress appears to do nothing")
	}
	if m.flashText == "no git repo here" {
		t.Error("a failed scan was reported as an absent repository")
	}
	if m.repoScan.cwd != "" {
		t.Error("the in-flight scan was not cleared on error")
	}
}

// An empty result with no error IS a finding, and keeps the original wording.
func TestApplyGitRepos_EmptyResultFlashesNoRepo(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "/a")
	m.repoScan = repoScanState{cwd: "/a", tabID: tab.ID}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a"}))

	if m.flashText != "no git repo here" {
		t.Errorf("flashText = %q, want %q", m.flashText, "no git repo here")
	}
}

// The tab is resolved again on arrival rather than captured: a round trip sits
// between asking and acting, and the user may have closed that tab meanwhile.
func TestApplyGitRepos_VanishedTabDropsTheIntent(t *testing.T) {
	t.Parallel()
	m, fake, tab := overlayTestModel(t, "/a")
	m.repoScan = repoScanState{cwd: "/a", tabID: tab.ID}
	m.tabs = nil // the tab went away while the request was in flight

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Repos: []string{"/a"}}))

	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgCreatePane {
			t.Fatal("an overlay was created for a tab that no longer exists")
		}
	}
}

// A never-answered request must become diagnosable rather than a keypress that
// silently did nothing.
func TestApplyGitScanTimeout_FlashesAndClears(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "/a")
	m.repoScan = repoScanState{cwd: "/a", tabID: tab.ID}

	runCmd(m.applyGitScanTimeout("/a"))

	if m.flashText == "" {
		t.Error("a timed-out scan flashed nothing")
	}
	if m.repoScan.cwd != "" {
		t.Error("the in-flight scan was not cleared")
	}
}

// A late tick from a superseded request must not cancel a live one.
func TestApplyGitScanTimeout_StaleTickIgnored(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "/a")
	m.repoScan = repoScanState{cwd: "/current", tabID: tab.ID}

	runCmd(m.applyGitScanTimeout("/old"))

	if m.repoScan.cwd != "/current" {
		t.Error("a stale timeout cancelled the live scan")
	}
	if m.flashText != "" {
		t.Errorf("a stale timeout flashed %q over a live scan", m.flashText)
	}
}

// A pane with no CWD must not round-trip. The daemon would substitute its own
// default and answer about a directory the user is not in, so an overlay could
// open on an unrelated repository.
func TestHandleToggleLazygit_NoCWD_DoesNotAskTheDaemon(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "")

	runCmd(m.handleToggleLazygit())

	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgGitReposReq {
			t.Fatal("asked the daemon to discover repos for an unknown directory")
		}
	}
	if m.flashText != "no git repo here" {
		t.Errorf("flashText = %q, want the no-candidates wording", m.flashText)
	}
}

// The happy path: Alt+G with a known CWD asks the daemon rather than reading
// the local disk. This is the whole point of RD-021 — the TUI's filesystem is
// the wrong one whenever the daemon is remote.
func TestHandleToggleLazygit_AsksTheDaemon(t *testing.T) {
	t.Parallel()
	m, fake, tab := overlayTestModel(t, "/srv/work")

	runCmd(m.handleToggleLazygit())

	var asked bool
	for i, msg := range fake.sent {
		if msg.Type == ipc.MsgGitReposReq {
			asked = true
			var p ipc.GitReposReqPayload
			decodeSentPayload(t, fake, i, &p)
			if p.CWD != "/srv/work" {
				t.Errorf("asked about %q, want the pane's CWD", p.CWD)
			}
		}
	}
	if !asked {
		t.Fatalf("Alt+G sent no discovery request (sent: %v)", debugSentTypes(fake))
	}
	if m.repoScan.cwd != "/srv/work" || m.repoScan.tabID != tab.ID {
		t.Errorf("repoScan = %+v, want the cwd and tab recorded for staleness matching", m.repoScan)
	}
}

// ---------------------------------------------------------------------------
// Pick-list purpose (RD-021 remainder): the setup dialog's discover="git"
// candidate list now asks the daemon through the same requestGitRepos /
// applyGitRepos pair Alt+G uses. These tests pin the discriminator that keeps
// the two apart — a response is only ever routed to ONE of them.
// ---------------------------------------------------------------------------

// The core split: a pick-list response fills the setup dialog's candidate
// list and must not touch the Alt+G overlay machinery at all.
func TestApplyGitRepos_PickList_PopulatesCandidates_NoOverlay(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePaneSetup
	m.repoScan = repoScanState{cwd: "/a", purpose: repoScanPickList}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Repos: []string{"/a", "/a/sub"}}))

	if len(m.repoCandidates) != 2 || m.repoCandidates[0] != "/a" || m.repoCandidates[1] != "/a/sub" {
		t.Errorf("repoCandidates = %v, want [/a /a/sub]", m.repoCandidates)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgCreatePane {
			t.Fatal("a pick-list response created an overlay pane")
		}
	}
}

// The mirror image: an overlay response must never populate the pick list,
// even if the setup dialog happens to be open at the same time (e.g. an
// Alt+G scan still in flight when Ctrl+N is opened for a different plugin).
func TestApplyGitRepos_Overlay_DoesNotPopulatePickList(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePaneSetup
	m.repoCandidates = []string{"/leftover"}
	m.repoScan = repoScanState{cwd: "/a", tabID: tab.ID, purpose: repoScanOverlay}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Repos: []string{"/a"}}))

	if len(m.repoCandidates) != 1 || m.repoCandidates[0] != "/leftover" {
		t.Errorf("repoCandidates = %v, want untouched [/leftover] — an overlay response must not touch the pick list", m.repoCandidates)
	}
}

// The pick list has no scroll machinery, so the client-side cap must survive
// the purpose dispatch exactly as it did when discovery ran locally.
func TestApplyGitRepos_PickList_CapsAtMaxRepoCandidates(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePaneSetup
	m.repoScan = repoScanState{cwd: "/a", purpose: repoScanPickList}

	repos := make([]string, maxRepoCandidates+5)
	for i := range repos {
		repos[i] = fmt.Sprintf("/repo-%02d", i)
	}
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Repos: repos}))

	if len(m.repoCandidates) != maxRepoCandidates {
		t.Fatalf("len(repoCandidates) = %d, want %d (capped)", len(m.repoCandidates), maxRepoCandidates)
	}
	if m.repoCandidates[0] != "/repo-00" {
		t.Errorf("repoCandidates[0] = %q, want /repo-00 (cap keeps the head of the list)", m.repoCandidates[0])
	}
}

// A failed pick-list scan must leave the candidate list empty and must not be
// reported the same way an absent repository is — the pair below pins the
// distinction on the one signal available: whether anything is flashed.
func TestApplyGitRepos_PickListError_LeavesListEmpty(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePaneSetup
	m.repoScan = repoScanState{cwd: "/a", purpose: repoScanPickList}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Error: "scan failed"}))

	if len(m.repoCandidates) != 0 {
		t.Errorf("repoCandidates = %v, want empty after a failed scan", m.repoCandidates)
	}
	if m.flashText == "" {
		t.Error("a failed pick-list scan flashed nothing; the failure would look silent")
	}
}

// The other half of the distinction: a real "no repository here" finding
// (no error, empty Repos) must NOT flash — it falls back to the recent/browser
// chain exactly as a plain PromptsCWD plugin does, silently. If this ever
// starts flashing the same way an error does, the two become indistinguishable
// again from the user's seat.
func TestApplyGitRepos_PickList_EmptyResultDoesNotFlash(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePaneSetup
	m.repoScan = repoScanState{cwd: "/a", purpose: repoScanPickList}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a"}))

	if m.flashText != "" {
		t.Errorf("flashText = %q, want empty — an absent repo is a real finding, not a failure", m.flashText)
	}
}

// A response landing after the setup dialog has already closed (Esc, submit,
// or a different plugin selected) must be dropped rather than populate a pick
// list nobody is looking at.
func TestApplyGitRepos_PickList_DroppedAfterDialogClosed(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePane // setup dialog already closed
	m.repoScan = repoScanState{cwd: "/a", purpose: repoScanPickList}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Repos: []string{"/a"}}))

	if m.repoCandidates != nil {
		t.Errorf("repoCandidates = %v, want untouched — the setup dialog had already closed", m.repoCandidates)
	}
}

// Same drop, for the error branch — a closed dialog must not flash for a scan
// nobody is waiting on any more.
func TestApplyGitRepos_PickListError_DroppedAfterDialogClosed(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.dialog = dialogCreatePane
	m.repoScan = repoScanState{cwd: "/a", purpose: repoScanPickList}

	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/a", Error: "boom"}))

	if m.flashText != "" {
		t.Errorf("flashText = %q, want empty — a closed dialog must not flash for it", m.flashText)
	}
}
