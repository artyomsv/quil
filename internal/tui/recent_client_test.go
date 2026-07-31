package tui

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func recentClientModel(t *testing.T) *Model {
	t.Helper()
	return &Model{
		client: &fakeSender{},
		dialog: dialogCreatePaneSetup,
	}
}

// The generation must reach the wire. It is the ONLY correlator this request
// has — unlike browse and git discovery there is no content key, because a path
// list makes a poor staleness key.
func TestRequestExistingDirs_CarriesGenerationOnTheWire(t *testing.T) {
	m := recentClientModel(t)
	cmd := m.requestExistingDirs([]string{"/a", "/b"})
	if cmd == nil {
		t.Fatal("requestExistingDirs returned no command")
	}
	// runCmd, NOT cmd(): the request returns a tea.Batch, and calling a batch
	// yields a BatchMsg without executing its children — nothing would be sent
	// even against a correct implementation.
	runCmd(cmd)

	sent := m.client.(*fakeSender).sent
	if len(sent) == 0 {
		t.Fatal("no message sent")
	}
	msg := sent[len(sent)-1]
	if msg.Type != ipc.MsgDirsExistReq {
		t.Errorf("Type = %q, want %q", msg.Type, ipc.MsgDirsExistReq)
	}
	if msg.ID == "" || msg.ID != m.recentScan.gen {
		t.Errorf("sent ID = %q, want the recorded generation %q — the staleness key "+
			"never reaches the daemon", msg.ID, m.recentScan.gen)
	}
}

func TestRequestExistingDirs_SendsThePathsAsked(t *testing.T) {
	m := recentClientModel(t)
	want := []string{"/one", "/two"}
	runCmd(m.requestExistingDirs(want))

	sent := m.client.(*fakeSender).sent
	if len(sent) == 0 {
		t.Fatal("no message sent")
	}
	var got ipc.DirsExistReqPayload
	if err := sent[len(sent)-1].DecodePayload(&got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if !reflect.DeepEqual(got.Paths, want) {
		t.Errorf("Paths = %v, want %v", got.Paths, want)
	}
}

func TestApplyExistingDirs_PopulatesThePickList(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a", "/b"})
	m.applyExistingDirs(ipc.DirsExistRespPayload{Paths: []string{"/b"}}, m.recentScan.gen)

	if !reflect.DeepEqual(m.recentCandidates, []string{"/b"}) {
		t.Errorf("recentCandidates = %v, want [/b]", m.recentCandidates)
	}
	if m.cwdBrowseDir != "/b" {
		t.Errorf("cwdBrowseDir = %q, want /b — the first candidate is the committed value", m.cwdBrowseDir)
	}
}

// Nothing left to offer is a real finding: hand over to the browser rather than
// leaving the field blank.
func TestApplyExistingDirs_EmptyResultFallsThroughToTheBrowser(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	cmd := m.applyExistingDirs(ipc.DirsExistRespPayload{}, m.recentScan.gen)

	if len(m.recentCandidates) != 0 {
		t.Errorf("recentCandidates = %v, want empty", m.recentCandidates)
	}
	if cmd == nil {
		t.Error("no command returned — the dialog is left with neither a pick list nor a browser")
	}
}

// A failed check is NOT "none of your directories exist". Both land in the
// browser, but only one of them is a finding, and reporting the other as one
// would be a wrong answer stated confidently.
func TestApplyExistingDirs_ErrorFallsThroughToTheBrowser(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	cmd := m.applyExistingDirs(
		ipc.DirsExistRespPayload{Error: "another existence check is already running"},
		m.recentScan.gen,
	)

	if cmd == nil {
		t.Error("no command returned — a failed existence check dead-ended the dialog")
	}
	if len(m.recentCandidates) != 0 {
		t.Errorf("recentCandidates = %v, want empty on failure", m.recentCandidates)
	}
}

func TestApplyExistingDirs_StaleGenerationIsDropped(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	m.applyExistingDirs(
		ipc.DirsExistRespPayload{Paths: []string{"/from-a-superseded-request"}},
		"an-older-gen",
	)

	if len(m.recentCandidates) != 0 {
		t.Errorf("recentCandidates = %v, want empty — a superseded response was applied", m.recentCandidates)
	}
	if m.recentScan.gen == "" {
		t.Error("the live request's state was cleared by a superseded response")
	}
}

// Mirrors TestApplyGitRepos_PickList_DroppedAfterDialogClosed. Without the
// guard, an answer — or this request's own timeout tick — landing after Esc
// still matches its generation and fires a chain of browse RPCs for a dialog
// nobody is in, mutating the pick-list fields outside any dialog.
func TestApplyExistingDirs_DroppedAfterDialogClosed(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	gen := m.recentScan.gen
	m.dialog = dialogNone // user pressed Esc

	if cmd := m.applyExistingDirs(ipc.DirsExistRespPayload{Paths: []string{"/a"}}, gen); cmd != nil {
		t.Error("a response applied after the dialog closed")
	}
	if len(m.recentCandidates) != 0 {
		t.Errorf("recentCandidates = %v, want empty — state mutated outside the dialog", m.recentCandidates)
	}
}

func TestApplyExistingDirsTimeout_FallsThroughToTheBrowser(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	if cmd := m.applyExistingDirsTimeout(m.recentScan.gen); cmd == nil {
		t.Error("no command returned — an unanswered check left the dialog empty")
	}
}

func TestApplyExistingDirsTimeout_DroppedAfterDialogClosed(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	gen := m.recentScan.gen
	m.dialog = dialogNone

	if cmd := m.applyExistingDirsTimeout(gen); cmd != nil {
		t.Error("a timeout tick fired the browser chain after the dialog closed")
	}
}

func TestApplyExistingDirsTimeout_StaleGenerationDoesNotClearALiveScan(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/a"})
	live := m.recentScan.gen

	m.applyExistingDirsTimeout("an-older-gen")

	if m.recentScan.gen != live {
		t.Error("a superseded timeout tick cleared the live scan")
	}
}

// TestApplyExistingDirs_CapsAnOversizedResponse pins the cap-on-receipt rule.
// The daemon caps the REQUEST, but a cap enforced only by the sender is not a
// cap once the sender is a host the user may not control — and paths[0] becomes
// cwdBrowseDir, the pane's spawn directory.
func TestApplyExistingDirs_CapsAnOversizedResponse(t *testing.T) {
	m := recentClientModel(t)

	// Asked about ALL of them, so the subset check passes them through and the
	// cap is what this test actually exercises.
	big := make([]string, recentCWDMax+5)
	for i := range big {
		big[i] = fmt.Sprintf("/dir-%d", i)
	}
	m.requestExistingDirs(big)

	m.applyExistingDirs(ipc.DirsExistRespPayload{Paths: big}, m.recentScan.gen)

	if len(m.recentCandidates) != recentCWDMax {
		t.Errorf("recentCandidates = %d, want capped to %d", len(m.recentCandidates), recentCWDMax)
	}
}

// TestApplyExistingDirs_RejectsPathsNeverAsked pins the subset check. The
// daemon is a host the user may not control; the surviving directories must be
// a subset of what was asked about, not whatever the far side names. paths[0]
// becomes the pane's spawn directory and is persisted into the local recent
// list on submit, so a substituted entry is not merely cosmetic.
func TestApplyExistingDirs_RejectsPathsNeverAsked(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/asked-a", "/asked-b"})

	m.applyExistingDirs(ipc.DirsExistRespPayload{
		Paths: []string{"/never-asked", "/asked-b"},
	}, m.recentScan.gen)

	if !reflect.DeepEqual(m.recentCandidates, []string{"/asked-b"}) {
		t.Errorf("recentCandidates = %v, want only the asked-about path", m.recentCandidates)
	}
	if m.cwdBrowseDir == "/never-asked" {
		t.Error("a path the client never sent became the committed spawn directory")
	}
}

// An answer made entirely of paths that were never asked about is not a list;
// it is a failed check. Hand over to the browser rather than showing it.
func TestApplyExistingDirs_AllUnaskedFallsThroughToTheBrowser(t *testing.T) {
	m := recentClientModel(t)
	m.requestExistingDirs([]string{"/asked"})

	cmd := m.applyExistingDirs(ipc.DirsExistRespPayload{
		Paths: []string{"/spoofed-1", "/spoofed-2"},
	}, m.recentScan.gen)

	if len(m.recentCandidates) != 0 {
		t.Errorf("recentCandidates = %v, want empty", m.recentCandidates)
	}
	if cmd == nil {
		t.Error("no command returned — the dialog is left with neither a pick list nor a browser")
	}
}
