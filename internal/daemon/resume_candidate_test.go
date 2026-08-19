package daemon

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/plugin"
)

const (
	uuidA = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
	uuidB = "5a31389f-55db-408b-aeef-e31dae522bbd"
	uuidC = "b279136b-3610-4096-844a-ad211ebff2eb"
)

// resumeTestPlugin mirrors the shipped claude-code plugin: preassign_id, with
// --continue as the configured fallback and --session-id as the fresh start.
func resumeTestPlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude", Sessions: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}
}

// stubResumeSeams points the two filesystem seams at test doubles. hookRec is
// what the pane's recorded session file holds; probe answers the recorded-path
// stat as (exists, answered).
func stubResumeSeams(t *testing.T, hookRec claudehook.SessionRecord, probe func(path string) (bool, bool)) {
	t.Helper()
	origHook, origProbe := readHookSessionFn, transcriptExistsFn
	t.Cleanup(func() { readHookSessionFn, transcriptExistsFn = origHook, origProbe })
	readHookSessionFn = func(string) (claudehook.SessionRecord, error) { return hookRec, nil }
	transcriptExistsFn = probe
}

// probeNothing answers "not there" for every path.
func probeNothing(string) (bool, bool) { return false, true }

// probeOnly answers "there" for want and "not there" for anything else.
func probeOnly(want string) func(string) (bool, bool) {
	return func(p string) (bool, bool) { return p == want, true }
}

// claimRefusing is a sessionClaimFn that reports every candidate held by holder.
func claimRefusing(holder string) sessionClaimFn {
	return func(*Pane, []resumeCandidate) (resumeCandidate, string, bool) {
		return resumeCandidate{}, holder, false
	}
}

// transcriptFor builds a transcript path the way Claude does — the filename is
// the session id. Joined with filepath so the separator matches the platform the
// test runs on: the id/path binding uses filepath.Base, which only splits on the
// host's separator, so a hard-coded Windows path silently fails to bind on Linux.
func transcriptFor(id string) string {
	return filepath.Join("/home/u/.claude/projects/E--proj--claude-worktrees-faq", id+".jsonl")
}

// TestClaudeResumeTemplate_UnlocatedID_ResumesInsteadOfContinue is the reported
// bug. --continue is not "no resume": it attaches the pane to the most recent
// session in its CWD, which after a restart is whichever sibling pane respawned
// a moment earlier. Failing to LOCATE a session is not evidence it is gone.
func TestClaudeResumeTemplate_UnlocatedID_ResumesInsteadOfContinue(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{ID: uuidA}, probeNothing)

	pane := &Pane{ID: "pane-11111111", CWD: `E:\proj`}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimAny)

	if want := []string{"--resume", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
	if pane.PluginState["session_id"] != uuidA {
		t.Errorf("session_id = %q, want %q", pane.PluginState["session_id"], uuidA)
	}
}

// TestClaudeResumeTemplate_HookIDOutranksLocatedWorkspaceID is the ordering
// rule. The hook record is the only source that tracks /clear, /resume and
// compaction, so a stale workspace id must not win merely because we happened to
// find its transcript — that is the same silent swap in a narrower case.
func TestClaudeResumeTemplate_HookIDOutranksLocatedWorkspaceID(t *testing.T) {
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: uuidA}, // rotated, no path recorded
		probeOnly(transcriptFor(uuidB)),     // the STALE id is the one we can find
	)

	pane := &Pane{ID: "pane-22222222", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":      uuidB,
		"transcript_path": transcriptFor(uuidB),
	}}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimAny)

	if want := []string{"--resume", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
	if pane.PluginState["session_id"] != uuidA {
		t.Errorf("session_id = %q, want the hook-recorded %q", pane.PluginState["session_id"], uuidA)
	}
}

// TestClaudeResumeTemplate_ProvenMissingIsSkipped is the other half: a recorded
// path that NAMES the id and is not there is real evidence of deletion, so the
// next candidate gets its turn.
func TestClaudeResumeTemplate_ProvenMissingIsSkipped(t *testing.T) {
	stubResumeSeams(t,
		claudehook.SessionRecord{ID: uuidA, TranscriptPath: transcriptFor(uuidA)},
		probeOnly(transcriptFor(uuidB)), // A's transcript is gone, B's is there
	)

	pane := &Pane{ID: "pane-33333333", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":      uuidB,
		"transcript_path": transcriptFor(uuidB),
	}}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimAny)

	if want := []string{"--resume", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
	if pane.PluginState["session_id"] != uuidB {
		t.Errorf("session_id = %q, want %q — the hook id was proven gone", pane.PluginState["session_id"], uuidB)
	}
}

// TestTranscriptState_UnansweredProbeIsNotMissing pins the distinction the
// classification turns on. A stat that times out on a dead mount is no evidence
// at all; reading it as "deleted" would let an unreachable filesystem silently
// disown a live session.
func TestTranscriptState_UnansweredProbeIsNotMissing(t *testing.T) {
	orig := transcriptExistsFn
	t.Cleanup(func() { transcriptExistsFn = orig })
	transcriptExistsFn = func(string) (bool, bool) { return false, false }

	if got := transcriptState(uuidA, transcriptFor(uuidA)); got != candidateUnknown {
		t.Errorf("state = %v, want candidateUnknown", got)
	}
}

// TestTranscriptState_PathNamingAnotherSessionIsNotEvidence is the id/path
// binding. The recorded path is an independent string, so without this check any
// existing file would vouch for any id — and the id goes into argv.
func TestTranscriptState_PathNamingAnotherSessionIsNotEvidence(t *testing.T) {
	orig := transcriptExistsFn
	t.Cleanup(func() { transcriptExistsFn = orig })
	transcriptExistsFn = func(string) (bool, bool) { return true, true } // everything exists

	if got := transcriptState(uuidA, transcriptFor(uuidB)); got != candidateUnknown {
		t.Errorf("state = %v, want candidateUnknown — the path names %s, not %s", got, uuidB, uuidA)
	}
	if got := transcriptState(uuidA, filepath.Join("/somewhere", "unrelated.txt")); got != candidateUnknown {
		t.Errorf("state = %v, want candidateUnknown for an unrelated file", got)
	}
}

// TestClaudeResumeTemplate_MalformedID_StartsFreshNotContinue closes the gap
// between "no session recorded" and "a session we refuse to name". The hook
// writes through a looser pattern than the one that gates argv, so a recorded id
// can be rejected here — and falling through to the configured --continue would
// be the hijack.
func TestClaudeResumeTemplate_MalformedID_StartsFreshNotContinue(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{ID: "--dangerously-skip-permissions"}, probeNothing)

	pane := &Pane{ID: "pane-44444444", CWD: `E:\proj`}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimAny)

	if want := []string{"--session-id", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want a fresh session %v", got, want)
	}
	if id := pane.PluginState["session_id"]; !resumeSessionIDRe.MatchString(id) {
		t.Errorf("session_id = %q, want a freshly minted uuid", id)
	}
}

// TestClaudeResumeTemplate_ClaimedByAnotherPane_StartsFresh is the restore-side
// occupancy guard. Falling back to --continue here would be the same hijack, so
// the pane takes a brand-new identity instead — and it must be a NEW one: leaving
// the refused id in PluginState would have the pane advertise a session it is not
// in, and a later Alt+R would hand that id to --session-id for real.
func TestClaudeResumeTemplate_ClaimedByAnotherPane_StartsFresh(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{ID: uuidA}, probeNothing)

	pane := &Pane{ID: "pane-55555555", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":      uuidA,
		"transcript_path": transcriptFor(uuidA),
	}}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimRefusing("pane-99999999"))

	if want := []string{"--session-id", "{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want a fresh session %v", got, want)
	}
	id := pane.PluginState["session_id"]
	if id == uuidA {
		t.Error("session_id still names the refused session — the pane would claim another pane's conversation")
	}
	if !resumeSessionIDRe.MatchString(id) {
		t.Errorf("session_id = %q, want a freshly minted uuid", id)
	}
	if p, ok := pane.PluginState["transcript_path"]; ok {
		t.Errorf("transcript_path = %q, want it dropped — it described the refused session", p)
	}
}

// TestClaimAny_RecordsIDAndTranscriptTogether pins the pair invariant at the
// point of the write: a path that outlives its id would vouch for a transcript
// nobody checked.
func TestClaimAny_RecordsIDAndTranscriptTogether(t *testing.T) {
	pane := &Pane{ID: "pane-66666666", PluginState: map[string]string{
		"session_id":      uuidB,
		"transcript_path": transcriptFor(uuidB),
	}}

	// A candidate WITH a path records both.
	claimAny(pane, []resumeCandidate{{id: uuidA, transcript: transcriptFor(uuidA)}})
	if pane.PluginState["session_id"] != uuidA || pane.PluginState["transcript_path"] != transcriptFor(uuidA) {
		t.Fatalf("pair = (%q, %q), want both updated",
			pane.PluginState["session_id"], pane.PluginState["transcript_path"])
	}

	// A candidate WITHOUT one drops the key rather than inheriting the old path.
	claimAny(pane, []resumeCandidate{{id: uuidC}})
	if pane.PluginState["session_id"] != uuidC {
		t.Errorf("session_id = %q, want %q", pane.PluginState["session_id"], uuidC)
	}
	if p, ok := pane.PluginState["transcript_path"]; ok {
		t.Errorf("transcript_path = %q, want it dropped — it described the previous session", p)
	}
}

// TestClaudeResumeTemplate_NoRecordedSession_KeepsConfiguredFallback pins the
// one case --continue still owns: a pane with no recorded session has no
// identity to preserve.
func TestClaudeResumeTemplate_NoRecordedSession_KeepsConfiguredFallback(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{}, probeNothing)

	pane := &Pane{ID: "pane-77777777", CWD: `E:\proj`}
	got := claudeResumeTemplate(resumeTestPlugin(), pane, claimAny)

	if want := []string{"--continue"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %v, want %v", got, want)
	}
}

// TestClaudeResumeCandidates_VerifiesFromPersistedTranscriptPath covers the pane
// whose hook file is gone (a wiped $QUIL_HOME/sessions). workspace.json carries
// the pair copied at the last clean shutdown, so the session is still locatable
// without inferring anything from the CWD.
func TestClaudeResumeCandidates_VerifiesFromPersistedTranscriptPath(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{}, probeOnly(transcriptFor(uuidA)))

	pane := &Pane{ID: "pane-88888888", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":      uuidA,
		"transcript_path": transcriptFor(uuidA),
	}}
	cands, sawRecorded := claudeResumeCandidates(pane)

	if !sawRecorded {
		t.Error("sawRecorded = false, want true")
	}
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(cands), cands)
	}
	if cands[0].id != uuidA || cands[0].state != candidateLocated {
		t.Errorf("candidate = %+v, want %s located", cands[0], uuidA)
	}
}

// TestClaudeResumeCandidates_DedupesSameIDFromTwoSources keeps one session from
// occupying two slots — it would make an all-held list look like two refusals.
func TestClaudeResumeCandidates_DedupesSameIDFromTwoSources(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{ID: uuidA}, probeNothing)

	pane := &Pane{ID: "pane-99999999", CWD: `E:\proj`, PluginState: map[string]string{
		"session_id":        uuidA,
		"resume_session_id": uuidA,
	}}
	cands, _ := claudeResumeCandidates(pane)

	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1 after dedupe: %+v", len(cands), cands)
	}
	if cands[0].source != "hook" {
		t.Errorf("source = %q, want the most authoritative (hook)", cands[0].source)
	}
}

// TestClaudeResumeCandidates_ResumeSessionIDOnly covers the restart window the
// user-chosen fallback exists for: a pane created to resume a picked session,
// restarted before its first SessionStart hook fired.
func TestClaudeResumeCandidates_ResumeSessionIDOnly(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{}, probeNothing)

	pane := &Pane{ID: "pane-aaaaaaaa", CWD: `E:\proj`, PluginState: map[string]string{
		"resume_session_id": uuidA,
	}}
	cands, sawRecorded := claudeResumeCandidates(pane)

	if !sawRecorded {
		t.Error("sawRecorded = false, want true")
	}
	if len(cands) != 1 || cands[0].id != uuidA || cands[0].source != "user-chosen" {
		t.Fatalf("candidates = %+v, want one user-chosen %s", cands, uuidA)
	}
}

// TestUsableResumeCandidates_DropsOnlyProvenMissing pins that ordering is by
// SOURCE and never rearranged by whether a transcript was located.
func TestUsableResumeCandidates_DropsOnlyProvenMissing(t *testing.T) {
	in := []resumeCandidate{
		{id: uuidA, source: "hook", state: candidateUnknown},
		{id: uuidB, source: "workspace", state: candidateMissing},
		{id: uuidC, source: "user-chosen", state: candidateLocated},
	}
	got := usableResumeCandidates(in)

	if len(got) != 2 {
		t.Fatalf("got %d usable, want 2: %+v", len(got), got)
	}
	if got[0].id != uuidA || got[1].id != uuidC {
		t.Errorf("order = [%s %s], want [%s %s] — source order, not located-first",
			got[0].id, got[1].id, uuidA, uuidC)
	}
}

// TestResolveSpawnArgs_ClaimedSession_SpawnsFreshWithTogglesIntact is the
// caller-level assertion: "the template is nil" was a claim about what
// resolveSpawnArgs produces, so assert it there. A refused pane must carry no
// --resume and no --continue, must get its own fresh --session-id, and must keep
// the runtime toggles the user enabled.
func TestResolveSpawnArgs_ClaimedSession_SpawnsFreshWithTogglesIntact(t *testing.T) {
	stubResumeSeams(t, claudehook.SessionRecord{ID: uuidA}, probeNothing)

	pane := &Pane{
		ID:           "pane-bbbbbbbb",
		CWD:          `E:\proj`,
		InstanceArgs: []string{"--dangerously-skip-permissions"},
		PluginState:  map[string]string{"session_id": uuidA},
	}
	got := resolveSpawnArgs(resumeTestPlugin(), pane, true, "", claimRefusing("pane-99999999"))

	for _, banned := range []string{"--resume", "--continue"} {
		for _, a := range got {
			if a == banned {
				t.Fatalf("args = %v, must not contain %s for a session held by another pane", got, banned)
			}
		}
	}
	want := []string{"--dangerously-skip-permissions", "--session-id", pane.PluginState["session_id"]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
