package tui

import (
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
