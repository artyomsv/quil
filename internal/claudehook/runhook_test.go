package claudehook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
