package claudehook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/panehistory"
)

// readSpool reads and JSON-decodes every line of a pane's spool file.
func readSpool(t *testing.T, quilDir, paneID string) []hookevents.Payload {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(quilDir, "events", paneID+".jsonl"))
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	var out []hookevents.Payload
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if ln == "" {
			continue
		}
		var p hookevents.Payload
		if err := json.Unmarshal([]byte(ln), &p); err != nil {
			t.Fatalf("decode spool line %q: %v", ln, err)
		}
		out = append(out, p)
	}
	return out
}

func TestRunHook_UserPromptSubmit_SpoolsStartEdge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	stdin := `{"hook_event_name":"UserPromptSubmit","session_id":"11111111-2222-3333-4444-555555555555","prompt":"tell me a joke"}`

	if err := RunHook(strings.NewReader(stdin), env, 1700000000000); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got := readSpool(t, dir, "pane-abc")
	if len(got) != 1 {
		t.Fatalf("spool lines = %d, want 1", len(got))
	}
	p := got[0]
	if p.V != hookevents.SchemaVersion || p.Source != hookevents.SourceClaude {
		t.Errorf("wrong header: v=%d src=%q", p.V, p.Source)
	}
	if p.HookEvent != "UserPromptSubmit" {
		t.Errorf("hook_event = %q, want UserPromptSubmit", p.HookEvent)
	}
	if p.TsMs != 1700000000000 {
		t.Errorf("ts_ms = %d, want injected 1700000000000", p.TsMs)
	}
	if p.Title != "Working on: tell me a joke" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Data["prompt_preview"] != "tell me a joke" {
		t.Errorf("prompt_preview = %q", p.Data["prompt_preview"])
	}
}

func TestRunHook_UserPromptSubmit_AppendsHistory(t *testing.T) {
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default", RecordHistory: true}
	in := `{"hook_event_name":"UserPromptSubmit","session_id":"sess-1","prompt":"fix the parser bug"}`
	if err := RunHook(strings.NewReader(in), env, 12345); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got, err := panehistory.Read(dir, env.PaneID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Text != "fix the parser bug" || got[0].TsMs != 12345 {
		t.Fatalf("unexpected history: %+v", got)
	}
}

func TestRunHook_UserPromptSubmit_NoHistoryWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default", RecordHistory: false}
	in := `{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"hello"}`
	if err := RunHook(strings.NewReader(in), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got, _ := panehistory.Read(dir, env.PaneID)
	if len(got) != 0 {
		t.Fatalf("expected no history when disabled, got %d", len(got))
	}
}

func TestRunHook_Stop_SpoolsStopEdge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-s", QuilDir: dir, Mode: "default"}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"Stop"}`), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got := readSpool(t, dir, "pane-s")
	if len(got) != 1 || got[0].HookEvent != "Stop" || got[0].Title != "Reply ready" {
		t.Fatalf("unexpected spool: %+v", got)
	}
	if got[0].Severity != hookevents.SeverityWarning {
		t.Errorf("sev = %q, want warning", got[0].Severity)
	}
	if got[0].Data != nil {
		t.Errorf("Stop without transcript_path must carry no data, got %+v", got[0].Data)
	}
}

func TestRunHook_Stop_AttachesModelUsageFromTranscript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	transcript := writeTranscript(t,
		`{"type":"user","message":{"content":"hi"}}`,
		assistantLine("claude-opus-4-8", 2, 600000, 1000, false),
	)
	env := HookEnv{PaneID: "pane-m", QuilDir: dir, Mode: "default"}
	stdin := `{"hook_event_name":"Stop","session_id":"s","transcript_path":` + jsonString(transcript) + `}`
	if err := RunHook(strings.NewReader(stdin), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got := readSpool(t, dir, "pane-m")
	if len(got) != 1 {
		t.Fatalf("spool lines = %d, want 1", len(got))
	}
	if got[0].Data["model"] != "claude-opus-4-8" {
		t.Errorf("data.model = %q", got[0].Data["model"])
	}
	if got[0].Data["context_tokens"] != "601002" {
		t.Errorf("data.context_tokens = %q, want 601002", got[0].Data["context_tokens"])
	}
}

func TestRunHook_Stop_MissingTranscript_NoData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-mm", QuilDir: dir, Mode: "default"}
	stdin := `{"hook_event_name":"Stop","transcript_path":"/nonexistent/nope.jsonl"}`
	if err := RunHook(strings.NewReader(stdin), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got := readSpool(t, dir, "pane-mm")
	if len(got) != 1 || got[0].Title != "Reply ready" {
		t.Fatalf("unexpected spool: %+v", got)
	}
	if got[0].Data != nil {
		t.Errorf("unreadable transcript must not add data, got %+v", got[0].Data)
	}
}

// jsonString marshals s as a JSON string literal (handles Windows path
// backslashes in transcript paths).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRunHook_Stop_RetriesTranscriptFlushRace(t *testing.T) {
	// Claude appends the final assistant transcript line ~30 ms AFTER the
	// Stop hook fires. Simulate the race: the transcript appears while the
	// hook is inside its retry loop.
	origDelays := transcriptRetryDelays
	transcriptRetryDelays = []time.Duration{0, 30 * time.Millisecond, 200 * time.Millisecond}
	t.Cleanup(func() { transcriptRetryDelays = origDelays })

	dir := t.TempDir()
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = os.WriteFile(transcript, []byte(assistantLine("claude-fable-5", 5, 80000, 400, false)+"\n"), 0o600)
	}()

	env := HookEnv{PaneID: "pane-race", QuilDir: dir, Mode: "default"}
	stdin := `{"hook_event_name":"Stop","session_id":"s","transcript_path":` + jsonString(transcript) + `}`
	if err := RunHook(strings.NewReader(stdin), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got := readSpool(t, dir, "pane-race")
	if len(got) != 1 || got[0].Data["model"] != "claude-fable-5" || got[0].Data["context_tokens"] != "80405" {
		t.Fatalf("retry did not pick up late transcript: %+v", got)
	}
}

func TestRunHook_SessionStart_WritesSessionFile_NoSpool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-sid", QuilDir: dir, Mode: "default"}
	sid := "abcdef01-2345-6789-abcd-ef0123456789"
	stdin := `{"hook_event_name":"SessionStart","session_id":"` + sid + `"}`
	if err := RunHook(strings.NewReader(stdin), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	// Session file written...
	b, err := os.ReadFile(filepath.Join(dir, "sessions", "pane-sid.id"))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	if strings.TrimSpace(string(b)) != sid {
		t.Errorf("session file = %q, want %q", strings.TrimSpace(string(b)), sid)
	}
	// ...and NO spool line (SessionStart is infrastructure, not a notification).
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-sid.jsonl")); !os.IsNotExist(err) {
		t.Errorf("SessionStart must not write a spool line (err=%v)", err)
	}
}

func TestRunHook_SubagentStop_WithoutAgentType_NotSpooled(t *testing.T) {
	t.Parallel()
	// Claude Code fires one SubagentStop with an EMPTY agent_type at the end
	// of every main turn — the root turn's own completion, not a background
	// subagent. Spooling it produced a sidebar card titled literally " done"
	// on every turn of every AI pane, which the queue then aggregated into
	// `" done" ×N` and re-promoted to the top each time. It also names no
	// agent, so the TUI work ledger discards it regardless. Drop it at the
	// producer; the TUI-side guard stays as defence in depth.
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-nosub", QuilDir: dir, Mode: "default"}
	stdin := `{"hook_event_name":"SubagentStop"}`
	if err := RunHook(strings.NewReader(stdin), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-nosub.jsonl")); !os.IsNotExist(err) {
		t.Errorf("an unnamed SubagentStop must not be spooled (err=%v)", err)
	}

	// A NAMED stop is still spooled — it is a real completion the ledger needs.
	stdin = `{"hook_event_name":"SubagentStop","agent_type":"Explore"}`
	if err := RunHook(strings.NewReader(stdin), env, 2); err != nil {
		t.Fatalf("RunHook (named): %v", err)
	}
	got := readSpool(t, dir, "pane-nosub")
	if len(got) != 1 {
		t.Fatalf("named SubagentStop: want 1 spool line, got %d", len(got))
	}
	if got[0].Data["agent_type"] != "Explore" {
		t.Errorf("agent_type = %q, want %q", got[0].Data["agent_type"], "Explore")
	}
}

func TestRunHook_SessionStart_RejectsNonUUID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-bad", QuilDir: dir, Mode: "default"}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"not-a-uuid"}`), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "pane-bad.id")); !os.IsNotExist(err) {
		t.Errorf("non-uuid session id must not be written (err=%v)", err)
	}
}

func TestRunHook_OffMode_DropsSpoolButKeepsSessionFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-off", QuilDir: dir, Mode: "off"}

	// Spool event dropped.
	if err := RunHook(strings.NewReader(`{"hook_event_name":"Stop"}`), env, 1); err != nil {
		t.Fatalf("RunHook stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-off.jsonl")); !os.IsNotExist(err) {
		t.Errorf("off mode must drop spool events (err=%v)", err)
	}

	// Session id still tracked (resume infrastructure must survive off mode).
	sid := "abcdef01-2345-6789-abcd-ef0123456789"
	if err := RunHook(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"`+sid+`"}`), env, 1); err != nil {
		t.Fatalf("RunHook sessionstart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "pane-off.id")); err != nil {
		t.Errorf("off mode must still write the session file: %v", err)
	}
}

func TestRunHook_EmptyPaneID_NoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "", QuilDir: dir, Mode: "default"}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"Stop"}`), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("empty pane id must be a no-op, but wrote %d entries", len(entries))
	}
}

func TestRunHook_SpoolLineIsBOMFreeValidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-json", QuilDir: dir, Mode: "default"}
	// A prompt with characters that the old shell producers had to hand-escape
	// (quotes, backslash, control char, non-ASCII) must round-trip cleanly.
	stdin := `{"hook_event_name":"UserPromptSubmit","prompt":"he said \"hi\"\tand\\done café"}`
	if err := RunHook(strings.NewReader(stdin), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events", "pane-json.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		t.Error("spool line must not start with a UTF-8 BOM")
	}
	// readSpool re-decodes it; if escaping were wrong this would fail.
	got := readSpool(t, dir, "pane-json")
	if len(got) != 1 || !strings.Contains(got[0].Data["prompt_preview"], "café") {
		t.Errorf("preview did not round-trip: %+v", got)
	}
}

// TestRunHook_AllSpoolBranches covers the per-event title/severity/data
// mapping for every forwarded event that produces a spool line. Each case
// asserts hook_event, title, severity, and (where applicable) the data field.
func TestRunHook_AllSpoolBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		stdin    string
		hookEvt  string
		wantTit  string
		wantSev  string
		dataKey  string
		dataWant string
	}{
		{
			name:    "SessionEnd",
			stdin:   `{"hook_event_name":"SessionEnd"}`,
			hookEvt: "SessionEnd", wantTit: "Session ended", wantSev: "info",
		},
		{
			name:    "Notification",
			stdin:   `{"hook_event_name":"Notification","message":"Claude is waiting"}`,
			hookEvt: "Notification", wantTit: "Claude is waiting", wantSev: "warning",
		},
		{
			// The idle nudge is marked so the TUI can leave a still-working
			// pane alone; every other Notification stays unmarked and parks.
			name:    "Notification idle nudge is marked",
			stdin:   `{"hook_event_name":"Notification","message":"Claude is waiting for your input"}`,
			hookEvt: "Notification", wantTit: "Claude is waiting for your input", wantSev: "warning",
			dataKey: "notify_kind", dataWant: "idle",
		},
		{
			name:    "Notification permission prompt is not marked",
			stdin:   `{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash"}`,
			hookEvt: "Notification", wantTit: "Claude needs your permission to use Bash", wantSev: "warning",
			dataKey: "notify_kind", dataWant: "",
		},
		{
			name:    "PermissionRequest",
			stdin:   `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
			hookEvt: "PermissionRequest", wantTit: "Needs approval: Bash", wantSev: "warning",
			dataKey: "tool", dataWant: "Bash",
		},
		{
			name:    "PreCompact no reason",
			stdin:   `{"hook_event_name":"PreCompact"}`,
			hookEvt: "PreCompact", wantTit: "Compacting context", wantSev: "info",
			dataKey: "reason", dataWant: "",
		},
		{
			name:    "PreCompact with reason",
			stdin:   `{"hook_event_name":"PreCompact","reason":"auto"}`,
			hookEvt: "PreCompact", wantTit: "Compacting context (auto)", wantSev: "info",
			dataKey: "reason", dataWant: "auto",
		},
		{
			name:    "PostCompact",
			stdin:   `{"hook_event_name":"PostCompact"}`,
			hookEvt: "PostCompact", wantTit: "Compaction complete", wantSev: "info",
			dataKey: "compacting", dataWant: "1",
		},
		{
			name:    "SubagentStart",
			stdin:   `{"hook_event_name":"SubagentStart","agent_type":"Explore"}`,
			hookEvt: "SubagentStart", wantTit: "Spawned: Explore", wantSev: "info",
			dataKey: "agent_type", dataWant: "Explore",
		},
		{
			name:    "SubagentStop",
			stdin:   `{"hook_event_name":"SubagentStop","agent_type":"Explore"}`,
			hookEvt: "SubagentStop", wantTit: "Explore done", wantSev: "info",
			dataKey: "agent_type", dataWant: "Explore",
		},
		{
			name:    "TaskCreated",
			stdin:   `{"hook_event_name":"TaskCreated","content":"write tests"}`,
			hookEvt: "TaskCreated", wantTit: "Task: write tests", wantSev: "info",
			dataKey: "content", dataWant: "write tests",
		},
		{
			name:    "TaskCompleted",
			stdin:   `{"hook_event_name":"TaskCompleted","content":"write tests"}`,
			hookEvt: "TaskCompleted", wantTit: "✓ write tests", wantSev: "info",
			dataKey: "content", dataWant: "write tests",
		},
		{
			name:    "PostToolUse prompt tool (resume edge)",
			stdin:   `{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion"}`,
			hookEvt: "PostToolUse", wantTit: "Resumed after AskUserQuestion", wantSev: "info",
			dataKey: "tool", dataWant: "AskUserQuestion",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			env := HookEnv{PaneID: "pane-x", QuilDir: dir, Mode: "default"}
			if err := RunHook(strings.NewReader(tt.stdin), env, 42); err != nil {
				t.Fatalf("RunHook: %v", err)
			}
			got := readSpool(t, dir, "pane-x")
			if len(got) != 1 {
				t.Fatalf("spool lines = %d, want 1", len(got))
			}
			p := got[0]
			if p.HookEvent != tt.hookEvt {
				t.Errorf("hook_event = %q, want %q", p.HookEvent, tt.hookEvt)
			}
			if p.Title != tt.wantTit {
				t.Errorf("title = %q, want %q", p.Title, tt.wantTit)
			}
			if p.Severity != tt.wantSev {
				t.Errorf("sev = %q, want %q", p.Severity, tt.wantSev)
			}
			if tt.dataKey != "" && p.Data[tt.dataKey] != tt.dataWant {
				t.Errorf("data[%q] = %q, want %q", tt.dataKey, p.Data[tt.dataKey], tt.dataWant)
			}
		})
	}
}

// TestNotifyKindData_MarksTheIdleNudgeOnly pins the producer half of the
// Notification split. The match is deliberately one-directional: the idle
// phrase is recognised positively, and EVERYTHING else — a permission prompt,
// a future rewording, an empty message — is left unmarked so the consumer
// parks it. A false "idle" is a permission prompt that never surfaces; a false
// park is an amber tab the next Stop clears.
func TestNotifyKindData_MarksTheIdleNudgeOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
		want    string // "" means "not marked"
	}{
		{"observed idle nudge", "Claude is waiting for your input", hookevents.NotifyKindIdle},
		{"idle nudge, upstream case change", "CLAUDE IS WAITING FOR YOUR INPUT", hookevents.NotifyKindIdle},
		{"permission prompt", "Claude needs your permission to use Bash", ""},
		{"unknown future message", "Claude has something else to say", ""},
		{"empty message", "", ""},
		// The permission message embeds a tool name, and an MCP server names
		// its own tools — the idle phrase must not be reachable that way.
		{"permission prompt for a tool named after the phrase",
			"Claude needs your permission to use waiting for your input", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := notifyKindData(tt.message)
			if tt.want == "" {
				if got != nil {
					t.Errorf("notifyKindData(%q) = %v, want nil — only the idle nudge is marked", tt.message, got)
				}
				return
			}
			if got[hookevents.DataNotifyKind] != tt.want {
				t.Errorf("notifyKindData(%q)[%q] = %q, want %q",
					tt.message, hookevents.DataNotifyKind, got[hookevents.DataNotifyKind], tt.want)
			}
		})
	}
}

// TestRunHook_PostCompact_SignalsResetNoUsage locks in the compaction fix:
// right after compaction the reduced context size is not yet in the transcript
// (the summary carries no assistant usage), so PostCompact must NOT re-read and
// re-emit the stale pre-compaction usage — it emits a compacting-reset signal
// instead. The next completed turn's Stop reports the true reduced size.
func TestRunHook_PostCompact_SignalsResetNoUsage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-pc", QuilDir: dir, Mode: "default"}
	// Even with a transcript_path present, PostCompact must not attach usage.
	stdin := `{"hook_event_name":"PostCompact","transcript_path":"/whatever.jsonl"}`
	if err := RunHook(strings.NewReader(stdin), env, 7); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got := readSpool(t, dir, "pane-pc")
	if len(got) != 1 {
		t.Fatalf("spool lines = %d, want 1", len(got))
	}
	if got[0].Data["compacting"] != "1" {
		t.Errorf("data.compacting = %q, want 1", got[0].Data["compacting"])
	}
	if v, ok := got[0].Data["context_tokens"]; ok {
		t.Errorf("PostCompact must not carry context_tokens (stale pre-compaction count), got %q", v)
	}
	if v, ok := got[0].Data["model"]; ok {
		t.Errorf("PostCompact must not carry model usage data, got %q", v)
	}
}

func TestRunHook_RejectsTraversalPaneID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "../escape", QuilDir: dir, Mode: "default"}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"Stop"}`), env, 1); err == nil {
		t.Error("expected error for a path-traversal pane id")
	}
	// No file should have been written outside the events dir.
	if _, err := os.Stat(filepath.Join(dir, "events")); !os.IsNotExist(err) {
		t.Errorf("traversal pane id must not create any spool file (err=%v)", err)
	}
}

// TestRunHook_PostToolUse_NonPromptToolDropped guards the defensive tool gate:
// even though Claude's matcher should only fire PostToolUse for prompt tools, a
// PostToolUse for an ordinary tool (Bash/Read/Edit) must never spool — that was
// the noise the matcher exists to avoid.
// backdateSpool sets a pane's spool mtime so the PreToolUse throttle sees a
// known age. The throttle compares the file's mtime against the INJECTED
// nowMs, never the wall clock, so these tests are deterministic.
func backdateSpool(t *testing.T, quilDir, paneID string, when time.Time) {
	t.Helper()
	p := filepath.Join(quilDir, "events", paneID+".jsonl")
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatalf("chtimes spool: %v", err)
	}
}

const preToolStdin = `{"hook_event_name":"PreToolUse","session_id":"11111111-2222-3333-4444-555555555555","tool_name":"Bash"}`

// TestRunHook_PreToolUse_SpoolsWhenQuilHasNotHeardFromThePane covers the edge
// this event exists for: a turn that began without a user prompt (a teammate
// reported back and the agent resumed on its own). Quil's last word from the
// pane was a Stop, so it believes the pane is idle — the tool call is the only
// evidence otherwise.
func TestRunHook_PreToolUse_SpoolsWhenQuilHasNotHeardFromThePane(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-pre", QuilDir: dir, Mode: "default"}
	const nowMs int64 = 1700000000000

	stop := `{"hook_event_name":"Stop","session_id":"11111111-2222-3333-4444-555555555555"}`
	if err := RunHook(strings.NewReader(stop), env, nowMs-60_000); err != nil {
		t.Fatalf("RunHook(Stop): %v", err)
	}
	backdateSpool(t, dir, "pane-pre", time.UnixMilli(nowMs-60_000))

	if err := RunHook(strings.NewReader(preToolStdin), env, nowMs); err != nil {
		t.Fatalf("RunHook(PreToolUse): %v", err)
	}
	got := readSpool(t, dir, "pane-pre")
	if len(got) != 2 {
		t.Fatalf("spool lines = %d, want 2 (Stop + PreToolUse)", len(got))
	}
	p := got[1]
	if p.HookEvent != "PreToolUse" {
		t.Errorf("hook_event = %q, want PreToolUse", p.HookEvent)
	}
	if p.Data["tool"] != "Bash" {
		t.Errorf("data[tool] = %q, want Bash", p.Data["tool"])
	}
}

// TestRunHook_PreToolUse_ThrottledWhileThePaneIsAlreadyAudible is the half that
// keeps the cost down. PreToolUse is registered for EVERY tool — one hook
// invocation per tool call — but a pane quil heard from moments ago needs no
// further proof it is working, so the line is dropped before it reaches the
// spool, the ingester's rate limiter, or the notification queue.
func TestRunHook_PreToolUse_ThrottledWhileThePaneIsAlreadyAudible(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-thr", QuilDir: dir, Mode: "default"}
	const nowMs int64 = 1700000000000

	prompt := `{"hook_event_name":"UserPromptSubmit","session_id":"11111111-2222-3333-4444-555555555555","prompt":"go"}`
	if err := RunHook(strings.NewReader(prompt), env, nowMs-1_000); err != nil {
		t.Fatalf("RunHook(UserPromptSubmit): %v", err)
	}
	backdateSpool(t, dir, "pane-thr", time.UnixMilli(nowMs-1_000))

	if err := RunHook(strings.NewReader(preToolStdin), env, nowMs); err != nil {
		t.Fatalf("RunHook(PreToolUse): %v", err)
	}
	if got := readSpool(t, dir, "pane-thr"); len(got) != 1 {
		t.Fatalf("spool lines = %d, want 1 — the tool call added nothing quil did not already know", len(got))
	}
}

// TestRunHook_PreToolUse_FirstEventOnAPaneIsNeverThrottled pins the direction
// the throttle fails in. No spool file means quil has heard NOTHING from this
// pane, which is the loudest possible reason to speak — a stat error must
// never be read as "recently audible".
func TestRunHook_PreToolUse_FirstEventOnAPaneIsNeverThrottled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-first", QuilDir: dir, Mode: "default"}

	if err := RunHook(strings.NewReader(preToolStdin), env, 1700000000000); err != nil {
		t.Fatalf("RunHook(PreToolUse): %v", err)
	}
	got := readSpool(t, dir, "pane-first")
	if len(got) != 1 {
		t.Fatalf("spool lines = %d, want 1", len(got))
	}
	if got[0].HookEvent != "PreToolUse" {
		t.Errorf("hook_event = %q, want PreToolUse", got[0].HookEvent)
	}
}

// TestRunHook_PreToolUse_FromASubagentIsDropped keeps the heartbeat a
// statement about the MAIN turn, which is the only thing turnActive means.
// Hooks fire inside subagents too, and a subagent's tool call carries an
// agent_id (verified against Claude Code 2.1.233: main-agent tool events have
// none, a subagent's carry both agent_id and agent_type).
//
// Spooling those would let a background subagent — which by design outlives
// the main turn's Stop — reopen the turn that just ended. Nothing would close
// it again: the subagent's own completion is a SubagentStop, so the pane would
// hold a lit spinner until SessionEnd. The subagent ledger already covers this
// pane correctly via SubagentStart/Stop, so dropping the line costs no signal.
func TestRunHook_PreToolUse_FromASubagentIsDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-sub", QuilDir: dir, Mode: "default"}

	// No spool at all: the throttle would emit this one, so a dropped line can
	// only be the agent_id gate.
	stdin := `{"hook_event_name":"PreToolUse","session_id":"11111111-2222-3333-4444-555555555555","tool_name":"Bash","agent_id":"afdeee7427beccf6d","agent_type":"general-purpose"}`
	if err := RunHook(strings.NewReader(stdin), env, 1700000000000); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-sub.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a subagent's tool call must not spool a main-turn start edge (stat err = %v)", err)
	}
}

func TestRunHook_PostToolUse_NonPromptToolDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-pt", QuilDir: dir, Mode: "default"}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-pt.jsonl")); !os.IsNotExist(err) {
		t.Errorf("PostToolUse for a non-prompt tool must not spool (err=%v)", err)
	}
}

func TestRunHook_UnknownEvent_NoSpool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-u", QuilDir: dir, Mode: "default"}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"SomeFutureEvent"}`), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "pane-u.jsonl")); !os.IsNotExist(err) {
		t.Errorf("unknown event must not spool (err=%v)", err)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in       string
		maxBytes int
		want     string
	}{
		{"short", 200, "short"},
		{"exact", 5, "exact"},
		{"toolong", 6, "too…"}, // 3 bytes kept + 3-byte ellipsis = 6
		{"café", 100, "café"},  // within cap, unchanged (é is 2 bytes)
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.maxBytes)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.maxBytes, got, tt.want)
		}
		if len(got) > tt.maxBytes && len(tt.in) > tt.maxBytes {
			t.Errorf("truncate(%q, %d) = %q exceeds cap (%d bytes)", tt.in, tt.maxBytes, got, len(got))
		}
	}
}
