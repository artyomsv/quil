package codexhook

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

const sid = "01a05db1-9f44-73b2-b426-8aad5f5232f4"

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
			t.Fatalf("decode %q: %v", ln, err)
		}
		out = append(out, p)
	}
	return out
}

func spoolMissing(t *testing.T, quilDir, paneID string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(quilDir, "events", paneID+".jsonl")); err == nil {
		t.Fatalf("spool exists; want nothing written")
	}
}

func run(t *testing.T, env HookEnv, stdin string, nowMs int64) {
	t.Helper()
	if err := RunHook(strings.NewReader(stdin), env, nowMs); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
}

func TestRunHook_OutsideQuilIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run(t, HookEnv{QuilDir: dir}, `{"hook_event_name":"Stop","session_id":"`+sid+`"}`, 1)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a hook invoked outside Quil must write nothing; got %v", entries)
	}
}

func TestRunHook_SessionStart_WritesRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":"/home/x/.codex/sessions/2026/09/01/rollout-x-`+sid+`.jsonl","source":"startup","cwd":"/w","model":"gpt-5","permission_mode":"default"}`, 1)
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != sid || !strings.HasSuffix(rec.TranscriptPath, sid+".jsonl") {
		t.Errorf("record = %+v", rec)
	}
	spoolMissing(t, dir, "pane-abc")
}

func TestRunHook_SessionStart_NullTranscriptPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":null}`, 1)
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil || rec.ID != sid || rec.TranscriptPath != "" {
		t.Errorf("record = %+v, %v", rec, err)
	}
}

// A /new inside codex mints a new id and fires SessionStart again; the record
// must follow it.
func TestRunHook_SessionStart_RotationOverwrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":null,"source":"startup"}`, 1)
	const next = "01a05db2-6843-7612-8ea6-a7eca009f8b5"
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+next+`","transcript_path":null,"source":"clear"}`, 2)
	rec, err := ReadPersistedSession(dir, "pane-abc")
	if err != nil || rec.ID != next {
		t.Errorf("record = %+v, %v; want id %s", rec, err, next)
	}
}

func TestRunHook_SpoolMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		stdin     string
		wantEvent string
		wantTitle string
		wantSev   string
		wantData  map[string]string
	}{
		{"prompt", `{"hook_event_name":"UserPromptSubmit","session_id":"` + sid + `","prompt":"tell me a joke"}`,
			"UserPromptSubmit", "Working on: tell me a joke", hookevents.SeverityInfo, map[string]string{"prompt_preview": "tell me a joke"}},
		{"permission", `{"hook_event_name":"PermissionRequest","session_id":"` + sid + `","tool_name":"shell","tool_input":{"command":["ls"]}}`,
			"PermissionRequest", "Needs approval: shell", hookevents.SeverityWarning, map[string]string{"tool": "shell"}},
		{"session end", `{"hook_event_name":"SessionEnd","session_id":"` + sid + `","reason":"other"}`,
			"SessionEnd", "Session ended", hookevents.SeverityInfo, nil},
		{"subagent start", `{"hook_event_name":"SubagentStart","session_id":"` + sid + `","agent_id":"t1","agent_type":"explorer"}`,
			"SubagentStart", "Spawned: explorer", hookevents.SeverityInfo, map[string]string{"agent_type": "explorer"}},
		{"subagent stop", `{"hook_event_name":"SubagentStop","session_id":"` + sid + `","agent_id":"t1","agent_type":"explorer"}`,
			"SubagentStop", "explorer done", hookevents.SeverityInfo, map[string]string{"agent_type": "explorer"}},
		{"pre compact", `{"hook_event_name":"PreCompact","session_id":"` + sid + `","trigger":"auto"}`,
			"PreCompact", "Compacting context (auto)", hookevents.SeverityInfo, map[string]string{"trigger": "auto"}},
		{"post compact", `{"hook_event_name":"PostCompact","session_id":"` + sid + `","trigger":"manual"}`,
			"PostCompact", "Compaction complete", hookevents.SeverityInfo, map[string]string{"compacting": "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
			run(t, env, tt.stdin, 1700000000000)
			got := readSpool(t, dir, "pane-abc")
			if len(got) != 1 {
				t.Fatalf("spool lines = %d, want 1", len(got))
			}
			p := got[0]
			if p.V != hookevents.SchemaVersion || p.Source != hookevents.SourceCodex || p.PaneID != "pane-abc" || p.TsMs != 1700000000000 || p.SessionID != sid {
				t.Errorf("header = %+v", p)
			}
			if p.HookEvent != tt.wantEvent || p.Title != tt.wantTitle || p.Severity != tt.wantSev {
				t.Errorf("got %q/%q/%q, want %q/%q/%q", p.HookEvent, p.Title, p.Severity, tt.wantEvent, tt.wantTitle, tt.wantSev)
			}
			for k, v := range tt.wantData {
				if p.Data[k] != v {
					t.Errorf("data[%s] = %q, want %q", k, p.Data[k], v)
				}
			}
			if err := p.Validate(); err != nil {
				t.Errorf("payload does not validate: %v", err)
			}
		})
	}
}

func TestRunHook_Stop_CarriesModelAndContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rollout := writeRollout(t, tokenCountLine)
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	stdin, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop", "session_id": sid, "model": "gpt-5.6-terra",
		"transcript_path": rollout, "last_assistant_message": "done", "stop_hook_active": false,
	})
	run(t, env, string(stdin), 5)
	got := readSpool(t, dir, "pane-abc")
	if len(got) != 1 || got[0].HookEvent != "Stop" || got[0].Title != "Reply ready" || got[0].Severity != hookevents.SeverityWarning {
		t.Fatalf("spool = %+v", got)
	}
	if got[0].Data["model"] != "gpt-5.6-terra" || got[0].Data["context_tokens"] != "14072" {
		t.Errorf("data = %v", got[0].Data)
	}
}

// With no rollout to read, the Stop still goes out — bare, so the status
// segment is left alone rather than fed a model with no count.
func TestRunHook_Stop_NoUsageWhenRolloutUnavailable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"Stop","session_id":"`+sid+`","model":"gpt-5","transcript_path":null}`, 5)
	got := readSpool(t, dir, "pane-abc")
	if len(got) != 1 || got[0].Data != nil {
		t.Errorf("want a bare Stop with no data, got %+v", got)
	}
}

func TestRunHook_UserPromptSubmit_History(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	on := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default", RecordHistory: true}
	run(t, on, `{"hook_event_name":"UserPromptSubmit","session_id":"`+sid+`","prompt":"fix the parser"}`, 12345)
	got, err := panehistory.Read(dir, "pane-abc")
	if err != nil || len(got) != 1 || got[0].Text != "fix the parser" || got[0].TsMs != 12345 || got[0].SessionID != sid {
		t.Fatalf("history = %+v, %v", got, err)
	}
	off := HookEnv{PaneID: "pane-off", QuilDir: dir, Mode: "default"}
	run(t, off, `{"hook_event_name":"UserPromptSubmit","session_id":"`+sid+`","prompt":"hello"}`, 1)
	if h, _ := panehistory.Read(dir, "pane-off"); len(h) != 0 {
		t.Errorf("history recorded without the opt-in: %+v", h)
	}
}

func TestRunHook_PreToolUse_HeartbeatRules(t *testing.T) {
	t.Parallel()
	const call = `{"hook_event_name":"PreToolUse","session_id":"` + sid + `","tool_name":"shell","tool_input":{},"tool_use_id":"c1"}`
	t.Run("first call on a quiet pane spools Working", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		run(t, env, call, time.Now().UnixMilli())
		got := readSpool(t, dir, "pane-abc")
		if len(got) != 1 || got[0].HookEvent != "PreToolUse" || got[0].Title != "Working" || got[0].Data["tool"] != "shell" {
			t.Errorf("spool = %+v", got)
		}
	})
	t.Run("second call within the interval is dropped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		now := time.Now().UnixMilli()
		run(t, env, call, now)
		run(t, env, call, now+1000)
		if got := readSpool(t, dir, "pane-abc"); len(got) != 1 {
			t.Errorf("spool lines = %d, want 1 (throttled)", len(got))
		}
	})
	t.Run("a call after a quiet interval spools again", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		now := time.Now().UnixMilli()
		run(t, env, call, now)
		run(t, env, call, now+int64(workHeartbeatInterval/time.Millisecond)+1000)
		if got := readSpool(t, dir, "pane-abc"); len(got) != 2 {
			t.Errorf("spool lines = %d, want 2", len(got))
		}
	})
	t.Run("subagent tool calls are dropped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
		run(t, env, `{"hook_event_name":"PreToolUse","session_id":"`+sid+`","tool_name":"shell","agent_id":"t1","agent_type":"explorer"}`, time.Now().UnixMilli())
		spoolMissing(t, dir, "pane-abc")
	})
	t.Run("off mode never creates the spool", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "off"}
		run(t, env, call, time.Now().UnixMilli())
		spoolMissing(t, dir, "pane-abc")
	})
}

func TestRunHook_SubagentStop_UnnamedIsDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"SubagentStop","session_id":"`+sid+`","agent_id":"t1","agent_type":""}`, 1)
	spoolMissing(t, dir, "pane-abc")
}

func TestRunHook_OffMode_StillRecordsSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "off"}
	run(t, env, `{"hook_event_name":"SessionStart","session_id":"`+sid+`","transcript_path":null}`, 1)
	if _, err := ReadPersistedSession(dir, "pane-abc"); err != nil {
		t.Errorf("session tracking must survive off mode: %v", err)
	}
	rollout := writeRollout(t, tokenCountLine)
	stdin, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": sid, "model": "m", "transcript_path": rollout})
	run(t, env, string(stdin), 2)
	spoolMissing(t, dir, "pane-abc")
}

func TestRunHook_UnknownEventAndBadInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, `{"hook_event_name":"Interrupt","session_id":"`+sid+`"}`, 1)
	spoolMissing(t, dir, "pane-abc")
	if err := RunHook(strings.NewReader("not json"), env, 1); err == nil {
		t.Error("malformed stdin must be reported to the caller (the subcommand still exits 0)")
	}
	if err := RunHook(strings.NewReader(`{"hook_event_name":"Stop"}`), HookEnv{PaneID: "../x", QuilDir: dir}, 1); err == nil {
		t.Error("hostile pane id accepted")
	}
}

func TestRunHook_BOMTolerated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-abc", QuilDir: dir, Mode: "default"}
	run(t, env, "\xEF\xBB\xBF"+`{"hook_event_name":"SessionEnd","session_id":"`+sid+`"}`, 1)
	if got := readSpool(t, dir, "pane-abc"); len(got) != 1 {
		t.Errorf("spool = %+v", got)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("héllo wörld", 8); got != "héll…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abcdef", 2); got != "" {
		t.Errorf("a cap below the ellipsis must yield nothing, got %q", got)
	}
	if got := truncate("short", 60); got != "short" {
		t.Errorf("truncate = %q", got)
	}
}

// TestModelUsageData_RetriesUntilTheRolloutLine: codex appends the final
// token_count line around the moment the Stop hook fires, so a first read
// that finds nothing is not the answer. Not parallel: it scripts the two
// package-var seams every other Stop test reads.
func TestModelUsageData_RetriesUntilTheRolloutLine(t *testing.T) {
	origDelays, origRead := rolloutRetryDelays, readRolloutUsageFn
	t.Cleanup(func() { rolloutRetryDelays, readRolloutUsageFn = origDelays, origRead })
	rolloutRetryDelays = []time.Duration{0, 0, 0}

	calls := 0
	readRolloutUsageFn = func(string) (int64, bool) {
		calls++
		if calls < 3 {
			return 0, false
		}
		return 14072, true
	}
	env := HookEnv{PaneID: "pane-abc", QuilDir: t.TempDir()}
	got := modelUsageData(env, "gpt-5", "/r/rollout.jsonl")
	if calls != 3 || got == nil || got["context_tokens"] != "14072" || got["model"] != "gpt-5" {
		t.Fatalf("calls=%d data=%v", calls, got)
	}

	calls = 0
	readRolloutUsageFn = func(string) (int64, bool) { calls++; return 0, false }
	if got := modelUsageData(env, "gpt-5", "/r/rollout.jsonl"); got != nil || calls != 3 {
		t.Fatalf("exhausted retries must yield nil after every attempt: calls=%d data=%v", calls, got)
	}
}

func TestHookLog_StripsControlCharacters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hookLog(dir, "pane-abc", "unhandled hook_event: Evil\nforged pane=other line\x1b[31m")
	b, err := os.ReadFile(filepath.Join(dir, "codexhook", "hook.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("a control character forged a line: %q", string(b))
	}
	if strings.Contains(lines[0], "\x1b") {
		t.Errorf("escape survived: %q", lines[0])
	}
}
