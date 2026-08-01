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

// TestParseSessionRecord covers the on-disk format directly, including the
// promises its doc comment makes.
func TestParseSessionRecord(t *testing.T) {
	long := strings.Repeat("x", maxIDBytes+1)
	tests := []struct {
		name    string
		body    string
		wantID  string
		wantPth string
	}{
		{"id only", recSessID + "\n", recSessID, ""},
		{"id and path", recSessID + "\n/p/" + recSessID + ".jsonl\n", recSessID, "/p/" + recSessID + ".jsonl"},
		{"no trailing newline", recSessID, recSessID, ""},
		{"crlf", recSessID + "\r\n/p/x.jsonl\r\n", recSessID, "/p/x.jsonl"},
		{"empty file", "", "", ""},
		{"whitespace only", "   \n", "", ""},
		// Half a corrupt token is not a better resume target than none, and the
		// value would otherwise reach `claude --resume <id>` argv.
		{"over-long id line", long + "\n", "", ""},
		{"blank path line", recSessID + "\n\n", recSessID, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSessionRecord(tt.body)
			if got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if got.TranscriptPath != tt.wantPth {
				t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, tt.wantPth)
			}
		})
	}
}

// TestWriteSessionFile_NewlineInTranscriptPath_DropsPathKeepsID pins the guard
// that stops a hostile path forging a second record line. The path is the part
// we can afford to lose; the id is not.
func TestWriteSessionFile_NewlineInTranscriptPath_DropsPathKeepsID(t *testing.T) {
	env := recEnv(t)
	in := `{"hook_event_name":"SessionStart","session_id":"` + recSessID +
		`","transcript_path":"/p/a.jsonl\n` + recSessID + `\n/evil/b.jsonl"}`
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
	if rec.TranscriptPath != "" {
		t.Errorf("TranscriptPath = %q, want it dropped", rec.TranscriptPath)
	}
}

// TestRefreshTranscriptPath_NeverRewritesTheIDFile is the structural guarantee
// behind the sidecar. Hook invocations are independent processes, so if a Stop
// refresh could rewrite <paneID>.id it could lose a race with a concurrent
// SessionStart and put the PRE-rotation id back — resurrecting the session the
// user just left. Confining the refresh to the sidecar makes that unreachable.
func TestRefreshTranscriptPath_NeverRewritesTheIDFile(t *testing.T) {
	origDelays := transcriptRetryDelays
	transcriptRetryDelays = []time.Duration{0}
	t.Cleanup(func() { transcriptRetryDelays = origDelays })

	env := recEnv(t)
	start := `{"hook_event_name":"SessionStart","session_id":"` + recSessID + `","transcript_path":"/p/a.jsonl"}`
	if err := RunHook(strings.NewReader(start), env, 0); err != nil {
		t.Fatalf("RunHook(SessionStart): %v", err)
	}
	before, err := os.ReadFile(recFile(env.QuilDir))
	if err != nil {
		t.Fatal(err)
	}

	stop := `{"hook_event_name":"Stop","session_id":"` + recSessID + `","transcript_path":"/moved/b.jsonl"}`
	if err := RunHook(strings.NewReader(stop), env, 1); err != nil {
		t.Fatalf("RunHook(Stop): %v", err)
	}

	after, err := os.ReadFile(recFile(env.QuilDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the id file was rewritten by a Stop refresh:\n before: %q\n after:  %q", before, after)
	}
	// The refresh still has to take effect, via the sidecar.
	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSession: %v", err)
	}
	if rec.TranscriptPath != "/moved/b.jsonl" {
		t.Errorf("TranscriptPath = %q, want the refreshed /moved/b.jsonl", rec.TranscriptPath)
	}
}

// TestReadPersistedSession_StaleSidecarIsIgnored: a sidecar left by a previous
// session names a different id, and a path that outlives its id would point at a
// transcript this session was never in.
func TestReadPersistedSession_StaleSidecarIsIgnored(t *testing.T) {
	env := recEnv(t)
	start := `{"hook_event_name":"SessionStart","session_id":"` + recSessID + `","transcript_path":"/p/a.jsonl"}`
	if err := RunHook(strings.NewReader(start), env, 0); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	stale := "b279136b-3610-4096-844a-ad211ebff2eb"
	if err := os.WriteFile(transcriptFile(env.QuilDir, env.PaneID),
		[]byte(stale+"\n/other/session.jsonl\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec, err := ReadPersistedSession(env.QuilDir, env.PaneID)
	if err != nil {
		t.Fatalf("ReadPersistedSession: %v", err)
	}
	if rec.TranscriptPath != "/p/a.jsonl" {
		t.Errorf("TranscriptPath = %q, want the record's own /p/a.jsonl", rec.TranscriptPath)
	}
}

// TestRefreshTranscriptPath_NoRecordYet_CreatesNothing: a Stop that arrives
// before any SessionStart has nothing to attach a path to, and inventing a
// record here would let a Stop become an id writer by the back door.
func TestRefreshTranscriptPath_NoRecordYet_CreatesNothing(t *testing.T) {
	origDelays := transcriptRetryDelays
	transcriptRetryDelays = []time.Duration{0}
	t.Cleanup(func() { transcriptRetryDelays = origDelays })

	env := recEnv(t)
	stop := `{"hook_event_name":"Stop","session_id":"` + recSessID + `","transcript_path":"/p/a.jsonl"}`
	if err := RunHook(strings.NewReader(stop), env, 0); err != nil {
		t.Fatalf("RunHook(Stop): %v", err)
	}

	if _, err := os.Stat(recFile(env.QuilDir)); !os.IsNotExist(err) {
		t.Errorf("id file exists after a Stop with no prior SessionStart (err=%v)", err)
	}
	if _, err := os.Stat(transcriptFile(env.QuilDir, env.PaneID)); !os.IsNotExist(err) {
		t.Errorf("sidecar exists after a Stop with no prior SessionStart (err=%v)", err)
	}
}
