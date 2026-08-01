package claudehook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	recPaneID = "pane-a1b2c3d4"
	recSessID = "8f8c8498-bbe4-41b2-b8e4-817f87f754fe"
)

func recEnv(t *testing.T) HookEnv {
	t.Helper()
	return HookEnv{PaneID: recPaneID, QuilDir: t.TempDir(), Mode: "default"}
}

func recFile(quilDir string) string {
	return filepath.Join(quilDir, "sessions", recPaneID+".id")
}

// TestReadPersistedSession_RecordsTranscriptPathFromSessionStart is the core of
// the worktree fix: the session's transcript directory is derived from the
// session's own working directory, which a git-worktree agent changes at will,
// so the pane's spawn CWD cannot be used to find it. SessionStart already
// carries the real path — record it.
func TestReadPersistedSession_RecordsTranscriptPathFromSessionStart(t *testing.T) {
	env := recEnv(t)
	const transcript = "/home/u/.claude/projects/proj--claude-worktrees-faq/" + recSessID + ".jsonl"

	in := `{"hook_event_name":"SessionStart","session_id":"` + recSessID + `","transcript_path":"` + transcript + `"}`
	if err := RunHook(strings.NewReader(in), env, 0); err != nil {
		t.Fatalf("RunHook: %v", err)
	}

	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSession: %v", err)
	}
	if rec.ID != recSessID {
		t.Errorf("ID = %q, want %q", rec.ID, recSessID)
	}
	if rec.TranscriptPath != transcript {
		t.Errorf("TranscriptPath = %q, want %q", rec.TranscriptPath, transcript)
	}
}

// TestReadPersistedSessionID_TwoLineRecord_ReturnsOnlyID guards the format
// change against the reader that predates it. The old implementation TrimSpaces
// the whole file, so a second line would be returned glued to the id and land
// in argv as one token.
func TestReadPersistedSessionID_TwoLineRecord_ReturnsOnlyID(t *testing.T) {
	env := recEnv(t)
	if err := os.MkdirAll(filepath.Dir(recFile(env.QuilDir)), 0o700); err != nil {
		t.Fatal(err)
	}
	body := recSessID + "\n/some/where/" + recSessID + ".jsonl\n"
	if err := os.WriteFile(recFile(env.QuilDir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	id, _, err := ReadPersistedSessionID(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSessionID: %v", err)
	}
	if id != recSessID {
		t.Errorf("id = %q, want %q", id, recSessID)
	}
}

// TestReadPersistedSession_LegacyIDOnlyFile keeps every pane written by an
// earlier Quil readable: no transcript path is a normal state, not an error.
func TestReadPersistedSession_LegacyIDOnlyFile(t *testing.T) {
	env := recEnv(t)
	if err := os.MkdirAll(filepath.Dir(recFile(env.QuilDir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recFile(env.QuilDir), []byte(recSessID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSession: %v", err)
	}
	if rec.ID != recSessID {
		t.Errorf("ID = %q, want %q", rec.ID, recSessID)
	}
	if rec.TranscriptPath != "" {
		t.Errorf("TranscriptPath = %q, want empty", rec.TranscriptPath)
	}
}

// TestRunHook_StopRefreshesTranscriptPath covers the case SessionStart alone
// cannot: the session moves to another project directory mid-session (an agent
// cd-ing into a git worktree), so the path recorded at start goes stale while
// the id stays the same.
func TestRunHook_StopRefreshesTranscriptPath(t *testing.T) {
	origDelays := transcriptRetryDelays
	transcriptRetryDelays = []time.Duration{0}
	t.Cleanup(func() { transcriptRetryDelays = origDelays })

	env := recEnv(t)
	const startPath = "/home/u/.claude/projects/proj/" + recSessID + ".jsonl"
	const movedPath = "/home/u/.claude/projects/proj--claude-worktrees-faq/" + recSessID + ".jsonl"

	start := `{"hook_event_name":"SessionStart","session_id":"` + recSessID + `","transcript_path":"` + startPath + `"}`
	if err := RunHook(strings.NewReader(start), env, 0); err != nil {
		t.Fatalf("RunHook(SessionStart): %v", err)
	}
	stop := `{"hook_event_name":"Stop","session_id":"` + recSessID + `","transcript_path":"` + movedPath + `"}`
	if err := RunHook(strings.NewReader(stop), env, 1); err != nil {
		t.Fatalf("RunHook(Stop): %v", err)
	}

	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSession: %v", err)
	}
	if rec.ID != recSessID {
		t.Errorf("ID = %q, want %q (Stop must not rotate the id)", rec.ID, recSessID)
	}
	if rec.TranscriptPath != movedPath {
		t.Errorf("TranscriptPath = %q, want %q", rec.TranscriptPath, movedPath)
	}
}

// TestRunHook_StopForDifferentSessionLeavesRecord pins the guard that keeps the
// refresh from becoming a second id writer. SessionStart owns id rotation; a
// Stop carrying some other session must not repoint the pane's record.
func TestRunHook_StopForDifferentSessionLeavesRecord(t *testing.T) {
	origDelays := transcriptRetryDelays
	transcriptRetryDelays = []time.Duration{0}
	t.Cleanup(func() { transcriptRetryDelays = origDelays })

	env := recEnv(t)
	const startPath = "/home/u/.claude/projects/proj/" + recSessID + ".jsonl"
	const otherID = "b279136b-3610-4096-844a-ad211ebff2eb"

	start := `{"hook_event_name":"SessionStart","session_id":"` + recSessID + `","transcript_path":"` + startPath + `"}`
	if err := RunHook(strings.NewReader(start), env, 0); err != nil {
		t.Fatalf("RunHook(SessionStart): %v", err)
	}
	stop := `{"hook_event_name":"Stop","session_id":"` + otherID + `","transcript_path":"/elsewhere/` + otherID + `.jsonl"}`
	if err := RunHook(strings.NewReader(stop), env, 1); err != nil {
		t.Fatalf("RunHook(Stop): %v", err)
	}

	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSession: %v", err)
	}
	if rec.ID != recSessID {
		t.Errorf("ID = %q, want %q", rec.ID, recSessID)
	}
	if rec.TranscriptPath != startPath {
		t.Errorf("TranscriptPath = %q, want %q", rec.TranscriptPath, startPath)
	}
}
