package daemon

import (
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/plugin"
)

// resumeTestPlugin mirrors the shipped claude-code plugin: preassign_id with
// --continue as the configured fallback.
func resumeTestPlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}
}

// stubResumeSeams points every filesystem seam the resume path uses at test
// doubles. hookRec is what the pane's recorded session file holds; cwdHas
// answers the CWD-derived probe; pathHas answers the recorded-transcript probe.
func stubResumeSeams(t *testing.T, hookRec claudehook.SessionRecord, cwdHas func(cwd, id string) bool, pathHas func(path string) bool) {
	t.Helper()
	origHook, origProbe, origPath := readHookSessionFn, claudeSessionExistsFn, transcriptExistsFn
	t.Cleanup(func() {
		readHookSessionFn, claudeSessionExistsFn, transcriptExistsFn = origHook, origProbe, origPath
	})
	readHookSessionFn = func(string) (claudehook.SessionRecord, error) { return hookRec, nil }
	claudeSessionExistsFn = cwdHas
	transcriptExistsFn = pathHas
}

func noClaims(string) (string, bool) { return "", false }

// TestClaudeResumeTemplate_UnverifiableID_ResumesInsteadOfContinue is the
// reported bug. --continue is not "no resume": it attaches the pane to the most
// recent session in its CWD, which after a restart is whichever sibling pane
// respawned a moment earlier. When we know an id, a failed probe must never
// downgrade to a confident wrong answer.
func TestClaudeResumeTemplate_UnverifiableID_ResumesInsteadOfContinue(t *testing.T) {
	const id = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: id},
		func(string, string) bool { return false },
		func(string) bool { return false },
	)

	pane := &Pane{ID: "pane-11111111", CWD: `E:\proj`}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, noClaims)

	if want := []string{"--resume", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
	if pane.PluginState["session_id"] != id {
		t.Errorf("session_id = %q, want %q", pane.PluginState["session_id"], id)
	}
}

// TestClaudeResumeTemplate_WorktreeTranscript_BeatsStalePluginState is the
// transcript-path fix. The hook id is authoritative (it tracks /clear, /resume
// and compaction), but its transcript lives under the project directory of the
// session's OWN cwd — a git worktree, not the pane's spawn cwd. Probing by cwd
// alone declares the live id missing and hands the pane a pre-rotation id that
// happens to still sit in the spawn directory.
func TestClaudeResumeTemplate_WorktreeTranscript_BeatsStalePluginState(t *testing.T) {
	const (
		liveID     = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
		staleID    = "5a31389f-55db-408b-aeef-e31dae522bbd"
		transcript = `C:\Users\u\.claude\projects\E--proj--claude-worktrees-faq\` + liveID + ".jsonl"
	)
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: liveID, TranscriptPath: transcript},
		func(_, id string) bool { return id == staleID }, // only the stale id is under the pane's cwd
		func(p string) bool { return p == transcript },
	)

	pane := &Pane{
		ID:          "pane-22222222",
		CWD:         `E:\proj`,
		PluginState: map[string]string{"session_id": staleID},
	}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, noClaims)

	if want := []string{"--resume", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
	if pane.PluginState["session_id"] != liveID {
		t.Errorf("session_id = %q, want the hook-recorded %q", pane.PluginState["session_id"], liveID)
	}
}

// TestClaudeResumeTemplate_ClaimedByAnotherPane_StartsFresh is the restore-side
// occupancy guard. The create path already refuses a session a live pane holds;
// restore had no such check, so one transcript could be handed to N panes whose
// claude processes then interleave appends into it. Falling back to --continue
// here would be the same hijack, so the pane starts a fresh session instead.
func TestClaudeResumeTemplate_ClaimedByAnotherPane_StartsFresh(t *testing.T) {
	const id = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: id},
		func(string, string) bool { return true },
		func(string) bool { return true },
	)

	pane := &Pane{ID: "pane-33333333", CWD: `E:\proj`}
	claimed := func(sessionID string) (string, bool) {
		if sessionID == id {
			return "pane-99999999", true
		}
		return "", false
	}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimed)

	if got != nil {
		t.Fatalf("template = %v, want nil (fresh session, never --continue)", got)
	}
	if pane.PluginState["session_id"] == id {
		t.Errorf("session_id was set to the claimed session %q", id)
	}
}

// TestClaudeResumeTemplate_ClaimedBySelf_StillResumes keeps the guard from
// blocking the pane on its own claim — the restore path re-reads a pane that
// already holds its session.
func TestClaudeResumeTemplate_ClaimedBySelf_StillResumes(t *testing.T) {
	const id = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: id},
		func(string, string) bool { return true },
		func(string) bool { return true },
	)

	pane := &Pane{ID: "pane-44444444", CWD: `E:\proj`}
	claimed := func(string) (string, bool) { return pane.ID, true }
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimed)

	if want := []string{"--resume", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
}

// TestClaudeResumeTemplate_NoKnownSession_KeepsConfiguredFallback pins the one
// case --continue still owns: a pane with no recorded session at all has no
// identity to preserve, so the plugin's configured fallback stands.
func TestClaudeResumeTemplate_NoKnownSession_KeepsConfiguredFallback(t *testing.T) {
	stubResumeSeams(t,
		claudehook.SessionRecord{},
		func(string, string) bool { return false },
		func(string) bool { return false },
	)

	pane := &Pane{ID: "pane-55555555", CWD: `E:\proj`}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, noClaims)

	if want := []string{"--continue"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
}

// TestClaudeResumeCandidates_VerifiesFromPersistedTranscriptPath covers the
// pane whose hook file is gone (wiped $QUIL_HOME/sessions, a truncated write).
// workspace.json carries the id/path pair copied at the last clean shutdown, so
// the session is still locatable without inferring anything from the CWD.
func TestClaudeResumeCandidates_VerifiesFromPersistedTranscriptPath(t *testing.T) {
	const (
		id   = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
		path = `C:\Users\u\.claude\projects\E--proj--claude-worktrees-faq\` + id + ".jsonl"
	)
	stubResumeSeams(t,
		claudehook.SessionRecord{}, // no hook file at all
		func(string, string) bool { return false },
		func(p string) bool { return p == path },
	)

	pane := &Pane{ID: "pane-66666666", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":      id,
		"transcript_path": path,
	}}
	got := claudeResumeCandidates(pane)

	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].id != id {
		t.Errorf("id = %q, want %q", got[0].id, id)
	}
	if !got[0].verified {
		t.Error("candidate not verified — the persisted transcript path was not consulted")
	}
}

// TestClaudeResumeCandidates_PersistedPathOnlyVerifiesItsOwnID pins that the
// path is bound to the id it was recorded with. A session that rotated leaves
// the pair describing the OLD session, and letting that path vouch for the new
// id would report a transcript we never found as located.
func TestClaudeResumeCandidates_PersistedPathOnlyVerifiesItsOwnID(t *testing.T) {
	const (
		hookID  = "b279136b-3610-4096-844a-ad211ebff2eb"
		stateID = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
		path    = `C:\Users\u\.claude\projects\E--proj\` + stateID + ".jsonl"
	)
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: hookID}, // rotated; no path recorded yet
		func(string, string) bool { return false },
		func(p string) bool { return p == path },
	)

	pane := &Pane{ID: "pane-77777777", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":      stateID,
		"transcript_path": path,
	}}
	got := claudeResumeCandidates(pane)

	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].id != hookID || got[0].verified {
		t.Errorf("hook candidate = %+v, want id %q unverified (the persisted path belongs to another session)", got[0], hookID)
	}
	if got[1].id != stateID || !got[1].verified {
		t.Errorf("workspace candidate = %+v, want id %q verified", got[1], stateID)
	}
}
